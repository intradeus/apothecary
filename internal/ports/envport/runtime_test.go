package envport

import (
	"slices"
	"strings"
	"testing"
)

// The table is the plan's: privileged gives everything but containment,
// sysbox everything but user namespaces inside, kata everything.
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
	if err == nil || !strings.Contains(err.Error(), "kata, privileged, sysbox") {
		t.Errorf("err = %v, want the known names", err)
	}
	if got := Runtimes(); !slices.Equal(got, []Runtime{RuntimeKata, RuntimePrivileged, RuntimeSysbox}) {
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
	if got := Satisfying(need, Runtimes()); !slices.Equal(got, []Runtime{RuntimeKata}) {
		t.Errorf("satisfying = %v, want kata alone", got)
	}
	if got := Satisfying(need, []Runtime{RuntimeSysbox, RuntimePrivileged}); len(got) != 0 {
		t.Errorf("satisfying = %v, want none", got)
	}
	if got := Satisfying(RuntimeCapabilities{NestedContainers: true}, []Runtime{RuntimeSysbox, RuntimePrivileged}); !slices.Equal(got, []Runtime{RuntimePrivileged, RuntimeSysbox}) {
		t.Errorf("satisfying = %v", got)
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

func TestCapabilitiesUnion(t *testing.T) {
	u := RuntimeCapabilities{NestedContainers: true}.Union(RuntimeCapabilities{Contained: true, UserNamespaces: true, NestedKubernetes: true})
	if u != (RuntimeCapabilities{NestedContainers: true, NestedKubernetes: true, UserNamespaces: true, Contained: true}) {
		t.Errorf("union = %+v", u)
	}
}
