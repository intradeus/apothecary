package envport

import (
	"slices"
	"strings"
	"testing"
)

// The table is the plan's: privileged gives everything but containment,
// sysbox and userns everything but user namespaces inside (userns gets them
// from a cluster that says so: RuntimeChoice.Adds), kata everything.
func TestRuntimeCapabilities(t *testing.T) {
	p := RuntimePrivileged.Capabilities()
	if !p.NestedContainers || !p.NestedKubernetes || !p.UserNamespaces || p.Contained {
		t.Errorf("privileged = %+v", p)
	}
	s := RuntimeSysbox.Capabilities()
	if !s.NestedContainers || !s.NestedKubernetes || s.UserNamespaces || !s.Contained {
		t.Errorf("sysbox = %+v", s)
	}
	if k := RuntimeKata.Capabilities(); k != (RuntimeCapabilities{NestedContainers: true, NestedKubernetes: true, UserNamespaces: true, Contained: true}) {
		t.Errorf("kata = %+v", k)
	}
	if u := RuntimeUserns.Capabilities(); u != (RuntimeCapabilities{NestedContainers: true, NestedKubernetes: true, Contained: true}) {
		t.Errorf("userns = %+v", u)
	}
	if (Runtime("nope").Capabilities() != RuntimeCapabilities{}) {
		t.Error("an unknown runtime gives nothing")
	}
}

func TestParseRuntime(t *testing.T) {
	for in, want := range map[string]Runtime{"sysbox": RuntimeSysbox, " Privileged ": RuntimePrivileged} {
		got, err := ParseRuntime(in)
		if err != nil || got != want {
			t.Errorf("ParseRuntime(%q) = %q, %v", in, got, err)
		}
	}
	_, err := ParseRuntime("runc")
	if err == nil || !strings.Contains(err.Error(), "kata, privileged, sysbox, userns") {
		t.Errorf("err = %v, want the known names", err)
	}
	if got := Runtimes(); !slices.Equal(got, []Runtime{RuntimeKata, RuntimePrivileged, RuntimeSysbox, RuntimeUserns}) {
		t.Errorf("Runtimes() = %v", got)
	}
}

func TestMissingAndSatisfying(t *testing.T) {
	need := RuntimeCapabilities{NestedContainers: true, UserNamespaces: true, Contained: true}
	if got := RuntimeSysbox.Capabilities().Missing(need); !slices.Equal(got, []string{"user namespaces inside the sandbox"}) {
		t.Errorf("sysbox lacks %v", got)
	}
	if got := RuntimePrivileged.Capabilities().Missing(need); !slices.Equal(got, []string{"a sandbox contained from its node"}) {
		t.Errorf("privileged lacks %v", got)
	}
	if got := (RuntimeCapabilities{}).Missing(RuntimeCapabilities{NestedContainers: true, NestedKubernetes: true}); len(got) != 2 {
		t.Errorf("nothing lacks %v", got)
	}
	bare := RuntimeChoice{}
	if got := bare.Satisfying(need, Runtimes()); !slices.Equal(got, []Runtime{RuntimeKata}) {
		t.Errorf("satisfying = %v, want kata alone", got)
	}
	if got := bare.Satisfying(need, []Runtime{RuntimeSysbox, RuntimePrivileged}); len(got) != 0 {
		t.Errorf("satisfying = %v, want none", got)
	}
	if got := bare.Satisfying(RuntimeCapabilities{NestedContainers: true}, []Runtime{RuntimeSysbox, RuntimePrivileged}); !slices.Equal(got, []Runtime{RuntimePrivileged, RuntimeSysbox}) {
		t.Errorf("satisfying = %v", got)
	}
	// A cluster that adds user namespaces to userns makes it an answer too.
	wide := RuntimeChoice{Adds: map[Runtime]RuntimeCapabilities{RuntimeUserns: {UserNamespaces: true}}}
	if got := wide.Satisfying(need, Runtimes()); !slices.Equal(got, []Runtime{RuntimeKata, RuntimeUserns}) {
		t.Errorf("satisfying with userns widened = %v", got)
	}
}

func TestRuntimeChoiceValidate(t *testing.T) {
	ok := RuntimeChoice{Default: RuntimeSysbox, Approved: []Runtime{RuntimeSysbox, RuntimePrivileged}}
	if err := ok.Validate(); err != nil {
		t.Error(err)
	}
	for _, c := range []struct {
		c    RuntimeChoice
		want string
	}{
		{RuntimeChoice{Default: "gvisor", Approved: []Runtime{"gvisor"}}, "the default unknown sandbox runtime"},
		{RuntimeChoice{Default: RuntimeSysbox, Approved: []Runtime{RuntimeSysbox, "gvisor"}}, "an approved unknown"},
		{RuntimeChoice{Default: RuntimeSysbox, Approved: []Runtime{RuntimePrivileged}}, "not among the approved ones (privileged)"},
		{RuntimeChoice{Default: RuntimeSysbox}, "(none)"},
	} {
		if err := c.c.Validate(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: %v, want %q", c.c, err, c.want)
		}
	}
}

func TestRuntimeChoiceChoose(t *testing.T) {
	c := RuntimeChoice{Default: RuntimeSysbox, Approved: []Runtime{RuntimePrivileged, RuntimeSysbox}, Needs: RuntimeCapabilities{NestedContainers: true}}
	if rt, err := c.Choose("", RuntimeCapabilities{}); err != nil || rt != RuntimeSysbox {
		t.Errorf("unset = %s, %v; want the default", rt, err)
	}
	if rt, err := c.Choose(RuntimePrivileged, RuntimeCapabilities{}); err != nil || rt != RuntimePrivileged {
		t.Errorf("privileged = %s, %v", rt, err)
	}
	// Not approved: refused, naming what is.
	only := RuntimeChoice{Default: RuntimeSysbox, Approved: []Runtime{RuntimeSysbox}}
	if _, err := only.Choose(RuntimePrivileged, RuntimeCapabilities{}); err == nil || !strings.Contains(err.Error(), "not approved") || !strings.Contains(err.Error(), "approved: sysbox") {
		t.Errorf("unapproved: %v", err)
	}
	// A need the chosen runtime lacks: refused, naming the approved one that gives it.
	if _, err := c.Choose(RuntimeSysbox, RuntimeCapabilities{UserNamespaces: true}); err == nil ||
		!strings.Contains(err.Error(), "cannot give this workshop user namespaces inside the sandbox") || !strings.Contains(err.Error(), "choose privileged") {
		t.Errorf("missing need: %v", err)
	}
	// None approved gives it.
	if _, err := only.Choose("", RuntimeCapabilities{UserNamespaces: true}); err == nil || !strings.Contains(err.Error(), "no approved runtime gives it") {
		t.Errorf("unsatisfiable: %v", err)
	}
	// The definition's needs count as much as the project's.
	def := RuntimeChoice{Default: RuntimeSysbox, Approved: []Runtime{RuntimeSysbox}, Needs: RuntimeCapabilities{UserNamespaces: true}}
	if _, err := def.Choose("", RuntimeCapabilities{}); err == nil {
		t.Error("the definition's need was ignored")
	}
}

// What a cluster adds to a runtime counts for that runtime alone: a workshop
// that needs user namespaces is told kata where the cluster adds nothing, and
// keeps userns where it adds them.
func TestRuntimeChoiceAdds(t *testing.T) {
	need := RuntimeCapabilities{UserNamespaces: true}
	c := RuntimeChoice{Default: RuntimeUserns, Approved: []Runtime{RuntimeUserns, RuntimeKata, RuntimeSysbox}}
	if _, err := c.Choose("", need); err == nil || !strings.Contains(err.Error(), "sandbox runtime userns cannot give this workshop user namespaces inside the sandbox: choose kata") {
		t.Errorf("not widened: %v", err)
	}
	c.Adds = map[Runtime]RuntimeCapabilities{RuntimeUserns: {UserNamespaces: true}}
	if rt, err := c.Choose("", need); err != nil || rt != RuntimeUserns {
		t.Errorf("widened: %s, %v", rt, err)
	}
	if _, err := c.Choose(RuntimeSysbox, need); err == nil || !strings.Contains(err.Error(), "choose kata, userns") {
		t.Errorf("sysbox: %v", err)
	}
	if got := c.Capabilities(RuntimeUserns); got != (RuntimeCapabilities{NestedContainers: true, NestedKubernetes: true, UserNamespaces: true, Contained: true}) {
		t.Errorf("widened userns = %+v", got)
	}
	if got := c.Capabilities(RuntimeSysbox); got != RuntimeSysbox.Capabilities() {
		t.Errorf("sysbox = %+v, want its row", got)
	}
}

func TestCapabilitiesUnion(t *testing.T) {
	u := RuntimeCapabilities{NestedContainers: true}.Union(RuntimeCapabilities{Contained: true, UserNamespaces: true, NestedKubernetes: true})
	if u != (RuntimeCapabilities{NestedContainers: true, NestedKubernetes: true, UserNamespaces: true, Contained: true}) {
		t.Errorf("union = %+v", u)
	}
}
