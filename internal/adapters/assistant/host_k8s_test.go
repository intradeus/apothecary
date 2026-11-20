package assistant

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"apothecary/internal/adapters/kube"
)

// kubeScript answers kubectl by its words (flags dropped) and records what
// it was asked and sent.
type kubeScript struct {
	mu      sync.Mutex
	calls   []string
	stdins  [][]byte
	answers map[string]string
}

func (k *kubeScript) Run(_ context.Context, _ string, args []string, stdin []byte, _ io.Writer, _ ...string) ([]byte, []byte, int, error) {
	var words []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--namespace" || a == "-o" || a == "-l" || a == "-f" {
			i++
			continue
		}
		if !strings.HasPrefix(a, "-") {
			words = append(words, a)
		}
	}
	key := strings.Join(words, " ")
	k.mu.Lock()
	defer k.mu.Unlock()
	k.calls = append(k.calls, key)
	k.stdins = append(k.stdins, stdin)
	for prefix, out := range k.answers {
		if strings.HasPrefix(key, prefix) {
			return []byte(out), nil, 0, nil
		}
	}
	return nil, nil, 0, nil
}

func (k *kubeScript) sent(kind string) []map[string]any {
	k.mu.Lock()
	defer k.mu.Unlock()
	var out []map[string]any
	for _, b := range k.stdins {
		var m map[string]any
		if json.Unmarshal(b, &m) == nil && m["kind"] == kind {
			out = append(out, m)
		}
	}
	return out
}

func (k *kubeScript) did(prefix string) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	for _, c := range k.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func newHost(t *testing.T, answers map[string]string) (*K8sHost, *kubeScript) {
	k := &kubeScript{answers: answers}
	cfg := testCfg
	cfg.Kube = &kube.Client{Bin: filepath.Join(t.TempDir(), "absent"), Namespace: "apo", R: k}
	return NewK8sHost(cfg), k
}

func TestK8sEnsureMakesEverything(t *testing.T) {
	h, k := newHost(t, nil)
	if err := h.Ensure(context.Background(), "usr1", testTunnel, false); err != nil {
		t.Fatal(err)
	}
	if s := k.sent("Secret"); len(s) != 1 || s[0]["metadata"].(map[string]any)["name"] != "apo-assistant-usr1-relay" {
		t.Errorf("secrets = %v", s)
	}
	if len(k.sent("NetworkPolicy")) != 1 || len(k.sent("PersistentVolumeClaim")) != 1 || len(k.sent("Pod")) != 1 {
		t.Errorf("calls = %v", k.calls)
	}
	if !k.did("wait pod/apo-assistant-usr1") {
		t.Errorf("never waited for the pod: %v", k.calls)
	}
}

func TestK8sEnsureReusesOrReplaces(t *testing.T) {
	running := `{"metadata":{"name":"apo-assistant-usr1"},"spec":{"containers":[{"name":"agent","image":"apo/assistant:v1"}]},"status":{"phase":"Running"}}`
	answers := map[string]string{
		"get secret apo-assistant-usr1-relay": `{"data":{"relay-token":"` + b64("tok") + `"}}`,
		"get pvc apo-assistant-usr1":          `{"metadata":{"name":"apo-assistant-usr1"}}`,
		"get pod apo-assistant-usr1":          running,
	}
	h, k := newHost(t, answers)
	if err := h.Ensure(context.Background(), "usr1", testTunnel, false); err != nil {
		t.Fatal(err)
	}
	if len(k.sent("Pod")) != 0 || len(k.sent("PersistentVolumeClaim")) != 0 || len(k.sent("Secret")) != 0 {
		t.Errorf("a running pod was remade: %v", k.calls)
	}
	if err := h.Ensure(context.Background(), "usr1", testTunnel, true); err != nil {
		t.Fatal(err)
	}
	if !k.did("delete pod apo-assistant-usr1") || len(k.sent("Pod")) != 1 {
		t.Errorf("replace did not delete and recreate: %v", k.calls)
	}
	img, run, found, err := h.Inspect(context.Background(), "usr1")
	if err != nil || img != "apo/assistant:v1" || !run || !found {
		t.Errorf("inspect = %s %v %v %v", img, run, found, err)
	}
}

func b64(s string) string {
	b, _ := json.Marshal([]byte(s))
	return strings.Trim(string(b), `"`)
}

func TestK8sListsAndVolumes(t *testing.T) {
	used := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	h, k := newHost(t, map[string]string{
		"get pods": `{"items":[{"metadata":{"annotations":{"apothecary.io/person":"usr1"}},"status":{"phase":"Running"}},
			{"metadata":{"annotations":{"apothecary.io/person":"usr2"}},"status":{"phase":"Pending"}},
			{"metadata":{},"status":{"phase":"Running"}}]}`,
		"get pvc apo-assistant-usr1": `{"metadata":{"name":"apo-assistant-usr1"}}`,
		"get pvc": `{"items":[{"metadata":{"annotations":{"apothecary.io/person":"usr1","apothecary.io/last-used":"` + used.Format(time.RFC3339) + `"}}},
			{"metadata":{}}]}`,
	})
	ctx := context.Background()
	if run, err := h.Running(ctx); err != nil || len(run) != 1 || run[0] != "usr1" {
		t.Errorf("running = %v, %v", run, err)
	}
	if vols, err := h.Volumes(ctx); err != nil || len(vols) != 1 || !vols["usr1"].Equal(used) {
		t.Errorf("volumes = %v, %v", vols, err)
	}
	if err := h.Touch(ctx, "usr1", used.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if p := k.sent("PersistentVolumeClaim"); len(p) != 1 || !strings.Contains(fmt.Sprint(p[0]["metadata"]), "2026-11-01T01:00:00Z") {
		t.Errorf("touch = %v", p)
	}
	if err := h.Touch(ctx, "nobody", used); err != nil {
		t.Errorf("touch of an absent volume = %v", err)
	}
	h.tokens["usr1"] = "cached"
	if err := h.DeleteVolume(ctx, "usr1"); err != nil || !k.did("delete pvc apo-assistant-usr1") || !k.did("delete secret apo-assistant-usr1-relay") {
		t.Errorf("delete volume = %v, %v", err, k.calls)
	}
	// The cached token went with the Secret: the next Ensure makes both.
	if _, ok := h.tokens["usr1"]; ok {
		t.Error("the relay token outlived its Secret")
	}
	if _, err := h.Token(ctx, "usr1"); err == nil {
		t.Error("a token with no Secret")
	}
	if err := h.Stop(ctx, "usr1"); err != nil || !k.did("delete pod apo-assistant-usr1") {
		t.Errorf("stop = %v", err)
	}
	if _, _, found, err := h.Inspect(ctx, "nobody"); found || err != nil {
		t.Errorf("inspect of nobody = %v %v", found, err)
	}
}

// fakeKubectl is a kubectl binary: `exec` echoes its stdin and argv and
// exits with EXEC_CODE; `port-forward` reports port and sleeps.
func fakeKubectl(t *testing.T, port string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "kubectl")
	script := `#!/bin/sh
for a in "$@"; do
  case "$a" in
    exec) cat; echo "argv: $*"; echo "to stderr" >&2; exit 7 ;;
    port-forward) echo "Forwarding from 127.0.0.1:` + port + ` -> 7071"; exec sleep 60 ;;
  esac
done
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestK8sExec(t *testing.T) {
	h, _ := newHost(t, nil)
	h.Kube.Bin = fakeKubectl(t, "1")
	out, errb, code, err := h.Exec(context.Background(), "usr1", gitContainer, []string{shimBin, "fetch"}, []byte("the request\n"))
	if err != nil || code != 7 || !strings.HasPrefix(string(out), "the request\n") || !strings.Contains(string(out), "apo-assistant-usr1 -c git -- "+shimBin+" fetch") || string(errb) != "to stderr\n" {
		t.Errorf("exec = %q %q %d %v", out, errb, code, err)
	}
}

// The relay's handshake, through a port-forward.
func TestK8sDialThroughTheRelay(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var hello string
	var hmu sync.Mutex
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				br := bufio.NewReader(c)
				line, _ := br.ReadString('\n')
				hmu.Lock()
				hello = line
				hmu.Unlock()
				if strings.Contains(line, "sidecar.sock") {
					io.WriteString(c, "OK\nhello from the agent host")
				} else {
					io.WriteString(c, "NO\n")
				}
			}(c)
		}
	}()
	port := ln.Addr().String()[strings.LastIndex(ln.Addr().String(), ":")+1:]
	h, _ := newHost(t, map[string]string{"get secret apo-assistant-usr1-relay": `{"data":{"relay-token":"` + b64("tok") + `"}}`})
	h.Kube.Bin = fakeKubectl(t, port)
	h.Forward = true
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := h.Dial(ctx, "usr1", agentSock)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(c)
	c.Close()
	hmu.Lock()
	if hello != "APO1 tok sidecar.sock\n" || string(data) != "hello from the agent host" {
		t.Errorf("hello %q, data %q", hello, data)
	}
	hmu.Unlock()
	if _, err := h.Dial(ctx, "usr1", "other.sock"); err == nil {
		t.Error("a socket the relay refused was dialed")
	}
	h.Stop(ctx, "usr1")
	// No token: no dial.
	h2, _ := newHost(t, nil)
	if _, err := h2.Dial(ctx, "usr1", agentSock); err == nil {
		t.Error("dialed with no relay token")
	}
	// No forward and no pod address.
	h3, _ := newHost(t, map[string]string{"get secret": `{"data":{"relay-token":"` + b64("tok") + `"}}`})
	if _, err := h3.Dial(ctx, "usr1", agentSock); err == nil {
		t.Error("dialed a pod with no address")
	}
}
