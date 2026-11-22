package envport

import (
	"fmt"
	"slices"
	"strings"
)

// Runtime names what isolates a run's sandbox from the node it runs on, and
// so what can run inside it. It is one of three independent choices about a
// workspace: the host (who provisions the sandbox: Kubernetes), the runtime
// (this) and the definition (how the workspace is set up inside: a
// devcontainer). Runtimes apply to run sandboxes only; the engine, gateways,
// assistant and browser pods stay on the cluster's default runtime.
type Runtime string

const (
	// RuntimePrivileged is a privileged pod on the node's own runtime:
	// everything runs in it, and its root is the node's root. CI and a
	// single-tenant dev cluster, never a shared one.
	RuntimePrivileged Runtime = "privileged"
	// RuntimeSysbox is sysbox-runc: an unprivileged pod in a user namespace
	// of its own that still nests a docker daemon and Kubernetes. It owns
	// 65536 user ids, so nothing inside it gets a user namespace of its own
	// above that range (a user-namespaced pod in a nested cluster).
	RuntimeSysbox Runtime = "sysbox"
	// RuntimeKata is Kata Containers: each sandbox pod is a lightweight VM
	// with a kernel of its own (KVM on the node), the VM the boundary. Inside
	// it everything runs, user namespaces included; its memory is held per
	// VM, so a sandbox under it must be sized.
	RuntimeKata Runtime = "kata"
)

// RuntimeCapabilities is what a runtime gives the sandbox; a definition (and
// a project's settings) state what they need in the same terms (Needs).
type RuntimeCapabilities struct {
	// NestedContainers: a docker daemon (and compose) inside the sandbox.
	NestedContainers bool `json:"nested_containers"`
	// NestedKubernetes: a Kubernetes cluster on that daemon (k3d, kind).
	NestedKubernetes bool `json:"nested_kubernetes"`
	// UserNamespaces: a user-namespaced pod or container inside the sandbox,
	// mapped anywhere in the 32-bit id space (a nested kubelet's choice).
	UserNamespaces bool `json:"user_namespaces"`
	// Contained: sandbox root is not node root. It decides, under the
	// deployment's daemon door left on auto, whether a workspace may take
	// the sandbox's own docker daemon.
	Contained bool `json:"contained"`
}

// runtimes is the table: each runtime's capabilities. A runtime not in it is
// unknown; ParseRuntime refuses it.
var runtimes = map[Runtime]RuntimeCapabilities{
	RuntimePrivileged: {NestedContainers: true, NestedKubernetes: true, UserNamespaces: true},
	RuntimeSysbox:     {NestedContainers: true, NestedKubernetes: true, Contained: true},
	RuntimeKata:       {NestedContainers: true, NestedKubernetes: true, UserNamespaces: true, Contained: true},
}

// Runtimes lists every known runtime, sorted by name.
func Runtimes() []Runtime {
	out := make([]Runtime, 0, len(runtimes))
	for r := range runtimes {
		out = append(out, r)
	}
	slices.Sort(out)
	return out
}

// ParseRuntime reads a runtime's name (case and surrounding space ignored).
func ParseRuntime(s string) (Runtime, error) {
	r := Runtime(strings.ToLower(strings.TrimSpace(s)))
	if _, ok := runtimes[r]; !ok {
		names := make([]string, 0, len(runtimes))
		for _, n := range Runtimes() {
			names = append(names, string(n))
		}
		return "", fmt.Errorf("unknown sandbox runtime %q (known: %s)", s, strings.Join(names, ", "))
	}
	return r, nil
}

// Capabilities is what r gives a sandbox; an unknown runtime gives nothing.
func (r Runtime) Capabilities() RuntimeCapabilities { return runtimes[r] }

// Missing lists, in a fixed order, what need asks for that have lacks; empty
// when have covers it.
func (have RuntimeCapabilities) Missing(need RuntimeCapabilities) []string {
	var out []string
	for _, c := range []struct {
		need, have bool
		what       string
	}{
		{need.NestedContainers, have.NestedContainers, "nested containers"},
		{need.NestedKubernetes, have.NestedKubernetes, "nested Kubernetes"},
		{need.UserNamespaces, have.UserNamespaces, "user namespaces inside the sandbox"},
		{need.Contained, have.Contained, "a sandbox contained from its node"},
	} {
		if c.need && !c.have {
			out = append(out, c.what)
		}
	}
	return out
}

// Satisfying lists, sorted, the runtimes among from that give everything
// need asks for: the fix a refusal names.
func Satisfying(need RuntimeCapabilities, from []Runtime) []Runtime {
	var out []Runtime
	for _, r := range from {
		if len(r.Capabilities().Missing(need)) == 0 {
			out = append(out, r)
		}
	}
	slices.Sort(out)
	return out
}

// Union is what either asks for: a definition's needs and a project's.
func (have RuntimeCapabilities) Union(o RuntimeCapabilities) RuntimeCapabilities {
	return RuntimeCapabilities{
		NestedContainers: have.NestedContainers || o.NestedContainers,
		NestedKubernetes: have.NestedKubernetes || o.NestedKubernetes,
		UserNamespaces:   have.UserNamespaces || o.UserNamespaces,
		Contained:        have.Contained || o.Contained,
	}
}

// RuntimeChoice is what a provisioner offers projects: the runtime a project
// that names none gets, the runtimes an admin approved (Default among them),
// and what the provisioner's workspace definition needs of any of them.
type RuntimeChoice struct {
	Default  Runtime   `json:"default"`
	Approved []Runtime `json:"approved"`
	// Needs is the definition's (a devcontainer: nested containers).
	Needs RuntimeCapabilities `json:"-"`
	// NoSleep is why this provisioner's sandboxes cannot be stopped when
	// idle and kept (EnvSleeper.CanSleep); "" when they can.
	NoSleep string `json:"-"`
}

// Validate: a known default, approved, and only known runtimes approved.
func (c RuntimeChoice) Validate() error {
	if _, err := ParseRuntime(string(c.Default)); err != nil {
		return fmt.Errorf("the default %w", err)
	}
	for _, r := range c.Approved {
		if _, err := ParseRuntime(string(r)); err != nil {
			return fmt.Errorf("an approved %w", err)
		}
	}
	if !slices.Contains(c.Approved, c.Default) {
		return fmt.Errorf("the default sandbox runtime %s is not among the approved ones (%s)", c.Default, joinRuntimes(c.Approved))
	}
	return nil
}

// Choose is the runtime a project's sandbox runs under: want, or the
// default when want is ""; refused when the admin did not approve it, or
// when it cannot give what the definition and the project (need) ask for.
// A refusal names the fix: the approved runtimes that would do.
func (c RuntimeChoice) Choose(want Runtime, need RuntimeCapabilities) (Runtime, error) {
	rt := want
	if rt == "" {
		rt = c.Default
	}
	if !slices.Contains(c.Approved, rt) {
		return "", fmt.Errorf("sandbox runtime %s is not approved on this instance (approved: %s; an admin approves runtimes on the provisioner, System → Provisioner)", rt, joinRuntimes(c.Approved))
	}
	all := c.Needs.Union(need)
	if missing := rt.Capabilities().Missing(all); len(missing) > 0 {
		fix := "no approved runtime gives it: ask an admin to approve one that does"
		if ok := Satisfying(all, c.Approved); len(ok) > 0 {
			fix = "choose " + joinRuntimes(ok)
		}
		return "", fmt.Errorf("sandbox runtime %s cannot give this workshop %s: %s", rt, strings.Join(missing, ", "), fix)
	}
	return rt, nil
}

func joinRuntimes(rs []Runtime) string {
	if len(rs) == 0 {
		return "none"
	}
	s := make([]string, len(rs))
	for i, r := range rs {
		s[i] = string(r)
	}
	return strings.Join(s, ", ")
}
