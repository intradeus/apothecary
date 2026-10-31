package kube

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type call struct {
	bin   string
	args  []string
	stdin []byte
}

type resp struct {
	out, errb string
	code      int
	err       error
}

type script struct {
	calls []call
	resps []resp
}

func (s *script) Run(_ context.Context, bin string, args []string, stdin []byte, tee io.Writer, _ ...string) ([]byte, []byte, int, error) {
	s.calls = append(s.calls, call{bin, args, stdin})
	if len(s.resps) == 0 {
		return nil, nil, 0, nil
	}
	r := s.resps[0]
	s.resps = s.resps[1:]
	if tee != nil {
		tee.Write([]byte(r.out))
		tee.Write([]byte(r.errb))
	}
	return []byte(r.out), []byte(r.errb), r.code, r.err
}

func TestPodRunnerArgv(t *testing.T) {
	s := &script{}
	c := &Client{Kubeconfig: "/k", Context: "ctx", Namespace: "ws", R: s}
	r := PodRunner{C: c, Pod: "apo-x", Container: "sandbox"}
	if _, _, _, err := r.Run(context.Background(), "devcontainer", []string{"up", "--x"}, []byte("in"), nil, "A=1"); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(s.calls[0].args, " ")
	want := "--kubeconfig /k --context ctx --namespace ws exec -i --pod-running-timeout=5s apo-x -c sandbox -- env GIT_TERMINAL_PROMPT=0 A=1 devcontainer up --x"
	if s.calls[0].bin != "kubectl" || got != want {
		t.Fatalf("argv:\n got %s\nwant %s", got, want)
	}
	if string(s.calls[0].stdin) != "in" {
		t.Fatalf("stdin = %q", s.calls[0].stdin)
	}
	s.calls = nil
	r.Run(context.Background(), "docker", []string{"ps"}, nil, nil)
	if strings.Contains(strings.Join(s.calls[0].args, " "), " -i ") {
		t.Fatal("-i without stdin")
	}
}

func TestPodRunnerExitMapping(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name     string
		r        resp
		wantCode int
		wantErr  bool
		wantErrb string
	}{
		{"ok", resp{out: "x"}, 0, false, ""},
		{"remote nonzero", resp{errb: "boom\ncommand terminated with exit code 7\n", code: 7}, 7, false, "boom\n"},
		{"remote nonzero, quiet", resp{errb: "command terminated with exit code 44\n", code: 44}, 44, false, ""},
		{"no pod", resp{errb: `Error from server (NotFound): pods "apo-x" not found`, code: 1}, 0, true, ""},
		{"api down", resp{errb: "Unable to connect to the server: dial tcp: i/o timeout", code: 1}, 0, true, ""},
		{"other kubectl failure", resp{errb: "error: something odd", code: 1}, 1, false, "error: something odd"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &script{resps: []resp{tc.r}}
			var tee bytes.Buffer
			_, errb, code, err := PodRunner{C: &Client{R: s}, Pod: "apo-x"}.Run(ctx, "sh", nil, nil, &tee)
			var te *TransportError
			if tc.wantErr != errors.As(err, &te) {
				t.Fatalf("err = %v, want transport error %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if code != tc.wantCode || string(errb) != tc.wantErrb {
				t.Fatalf("code=%d errb=%q, want %d %q", code, errb, tc.wantCode, tc.wantErrb)
			}
			if strings.Contains(tee.String(), "command terminated") {
				t.Fatalf("marker leaked into the tee: %q", tee.String())
			}
		})
	}
}

func TestGetAbsentIsNotAnError(t *testing.T) {
	s := &script{resps: []resp{{out: ""}, {out: `{"metadata":{"name":"p"},"status":{"phase":"Running","conditions":[{"type":"Ready","status":"True"}]}}`}, {errb: "Unable to connect", code: 1}}}
	c := &Client{R: s}
	var p Pod
	if found, err := c.Get(context.Background(), "pod", "p", &p); found || err != nil {
		t.Fatalf("absent: %v %v", found, err)
	}
	if found, err := c.Get(context.Background(), "pod", "p", &p); !found || err != nil || !p.Status.Ready() {
		t.Fatalf("present: %v %v %+v", found, err, p.Status)
	}
	if _, err := c.Get(context.Background(), "pod", "p", &p); err == nil {
		t.Fatal("API failure read as absent")
	}
}

func TestCreateToleratesAlreadyExists(t *testing.T) {
	s := &script{resps: []resp{{errb: `Error from server (AlreadyExists): pods "p" already exists`, code: 1}}}
	if err := (&Client{R: s}).Create(context.Background(), Pod{TypeMeta: PodType()}); err != nil {
		t.Fatal(err)
	}
	var obj map[string]any
	if err := json.Unmarshal(s.calls[0].stdin, &obj); err != nil || obj["kind"] != "Pod" || obj["apiVersion"] != "v1" {
		t.Fatalf("manifest = %s (%v)", s.calls[0].stdin, err)
	}
}

func TestCanI(t *testing.T) {
	s := &script{resps: []resp{{out: "yes\n"}, {out: "no\n", code: 1}, {errb: "Unable to connect", code: 1}}}
	c := &Client{R: s}
	if ok, err := c.CanI(context.Background(), "create", "pods"); !ok || err != nil {
		t.Fatalf("yes: %v %v", ok, err)
	}
	if ok, err := c.CanI(context.Background(), "create", "pods"); ok || err != nil {
		t.Fatalf("no: %v %v", ok, err)
	}
	if _, err := c.CanI(context.Background(), "create", "pods"); err == nil {
		t.Fatal("failure read as no")
	}
}

func TestKubeconfig(t *testing.T) {
	dir := t.TempDir()
	if p, _, _ := Kubeconfig(dir, ""); p != "" {
		t.Fatalf("empty → %q", p)
	}
	if p, _, _ := Kubeconfig(dir, "/etc/k3s.yaml"); p != "/etc/k3s.yaml" {
		t.Fatalf("path → %q", p)
	}
	p, cleanup, err := Kubeconfig(dir, "apiVersion: v1\nkind: Config\n")
	if err != nil || filepath.Dir(p) != dir {
		t.Fatalf("content → %q %v", p, err)
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
	cleanup()
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("not cleaned up")
	}
}
