package assistant

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"apothecary/internal/adapters/kube"
)

// Host is where persons' pods run: the manager keeps the policy (when to
// start, recreate, stop, reap), the host does it. The interface is the seam
// the manager's tests fake.
type Host interface {
	// Ensure brings the person's pod up on image with tn as its tunnel:
	// created when absent, recreated when replace. It returns once the pod
	// is ready.
	Ensure(ctx context.Context, person string, tn tunnel, replace bool) error
	// Inspect reports the pod's image and whether it runs (found false:
	// absent).
	Inspect(ctx context.Context, person string) (image string, running, found bool, err error)
	// Stop removes the pod; its volume stays.
	Stop(ctx context.Context, person string) error
	// Running lists the persons whose pod runs.
	Running(ctx context.Context) ([]string, error)
	// Volumes lists the persons with a volume, and when each was last used.
	Volumes(ctx context.Context) (map[string]time.Time, error)
	// Touch records the volume used now.
	Touch(ctx context.Context, person string, now time.Time) error
	// DeleteVolume removes the person's volume (the pod must be gone).
	DeleteVolume(ctx context.Context, person string) error
	// Exec runs argv in one of the pod's containers, stdin fed in.
	Exec(ctx context.Context, person, container string, argv []string, stdin []byte) (stdout, stderr []byte, code int, err error)
	// Dial reaches a socket in the pod's socket folder through its relay.
	Dial(ctx context.Context, person, sock string) (net.Conn, error)
	// Token is the pod's relay token, which its agent host also takes.
	Token(ctx context.Context, person string) (string, error)
}

// K8sHost runs pods with kubectl, as the rest of the Kubernetes adapters do.
type K8sHost struct {
	Config
	mu     sync.Mutex
	tokens map[string]string
	fwds   map[string]*kube.Forward
}

// NewK8sHost makes a host from its configuration.
func NewK8sHost(cfg Config) *K8sHost {
	return &K8sHost{Config: cfg, tokens: map[string]string{}, fwds: map[string]*kube.Forward{}}
}

func (h *K8sHost) Ensure(ctx context.Context, person string, tn tunnel, replace bool) error {
	name := podName(person)
	tok, err := h.token(ctx, person, true)
	if err != nil {
		return err
	}
	_ = tok
	if err := h.Kube.Apply(ctx, h.networkPolicy(person)); err != nil {
		return err
	}
	if found, err := h.Kube.Get(ctx, "pvc", volumeName(person), &kube.PersistentVolumeClaim{}); err != nil {
		return err
	} else if !found {
		if err := h.Kube.Create(ctx, h.volumeManifest(person, time.Now())); err != nil {
			return err
		}
	}
	var pod kube.Pod
	found, err := h.Kube.Get(ctx, "pod", name, &pod)
	if err != nil {
		return err
	}
	if found {
		ended := pod.Status.Phase == "Failed" || pod.Status.Phase == "Succeeded"
		if !ended && !replace {
			return h.Kube.WaitReady(ctx, name, 5*time.Minute)
		}
		if err := h.Kube.Delete(ctx, "pod", name, true); err != nil {
			return err
		}
		h.forget(person)
	}
	if err := h.Kube.Create(ctx, h.podManifest(person, tn)); err != nil {
		return err
	}
	return h.Kube.WaitReady(ctx, name, 5*time.Minute)
}

func (h *K8sHost) Inspect(ctx context.Context, person string) (string, bool, bool, error) {
	var pod kube.Pod
	found, err := h.Kube.Get(ctx, "pod", podName(person), &pod)
	if err != nil || !found {
		return "", false, false, err
	}
	img := ""
	for _, c := range pod.Spec.Containers {
		if c.Name == agentContainer {
			img = c.Image
		}
	}
	return img, pod.Status.Phase == "Running", true, nil
}

func (h *K8sHost) Stop(ctx context.Context, person string) error {
	h.forget(person)
	return h.Kube.Delete(ctx, "pod", podName(person), false)
}

func (h *K8sHost) Running(ctx context.Context) ([]string, error) {
	var list struct {
		Items []kube.Pod `json:"items"`
	}
	if err := h.Kube.List(ctx, "pods", labelAssistant, &list); err != nil {
		return nil, err
	}
	var out []string
	for _, p := range list.Items {
		if p.Status.Phase == "Running" && p.Metadata.Annotations[annoPerson] != "" {
			out = append(out, p.Metadata.Annotations[annoPerson])
		}
	}
	return out, nil
}

func (h *K8sHost) Volumes(ctx context.Context) (map[string]time.Time, error) {
	var list struct {
		Items []kube.PersistentVolumeClaim `json:"items"`
	}
	if err := h.Kube.List(ctx, "pvc", labelAssistant, &list); err != nil {
		return nil, err
	}
	out := map[string]time.Time{}
	for _, v := range list.Items {
		person := v.Metadata.Annotations[annoPerson]
		if person == "" {
			continue
		}
		t, _ := time.Parse(time.RFC3339, v.Metadata.Annotations[annoLastUsed])
		out[person] = t
	}
	return out, nil
}

func (h *K8sHost) Touch(ctx context.Context, person string, now time.Time) error {
	var pvc kube.PersistentVolumeClaim
	found, err := h.Kube.Get(ctx, "pvc", volumeName(person), &pvc)
	if err != nil || !found {
		return err
	}
	if pvc.Metadata.Annotations == nil {
		pvc.Metadata.Annotations = map[string]string{}
	}
	pvc.Metadata.Annotations[annoLastUsed] = now.UTC().Format(time.RFC3339)
	pvc.TypeMeta = kube.PVCType()
	return h.Kube.Apply(ctx, pvc)
}

func (h *K8sHost) DeleteVolume(ctx context.Context, person string) error {
	if err := h.Kube.Delete(ctx, "pvc", volumeName(person), false); err != nil {
		return err
	}
	// The relay Secret goes with the volume: the cached token goes too, or
	// the next Ensure would trust it and never make the Secret again.
	h.mu.Lock()
	delete(h.tokens, person)
	h.mu.Unlock()
	_ = h.Kube.Delete(ctx, "secret", secretName(person), false)
	return h.Kube.Delete(ctx, "networkpolicy", podName(person), false)
}

func (h *K8sHost) Exec(ctx context.Context, person, container string, argv []string, stdin []byte) ([]byte, []byte, int, error) {
	args := append(h.Kube.Global(), kube.PodRunner{Pod: podName(person), Container: container}.ExecArgs(stdin != nil, nil, argv)...)
	bin := h.Kube.Bin
	if bin == "" {
		bin = "kubectl"
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code, err = ee.ExitCode(), nil
	}
	return out.Bytes(), errb.Bytes(), code, err
}

// Dial speaks the relay's handshake: "APO1 <token> <socket>", then "OK".
func (h *K8sHost) Dial(ctx context.Context, person, sock string) (net.Conn, error) {
	tok, err := h.token(ctx, person, false)
	if err != nil {
		return nil, err
	}
	addr, err := h.addr(ctx, person)
	if err != nil {
		return nil, err
	}
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		h.forget(person)
		return nil, err
	}
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := fmt.Fprintf(c, "APO1 %s %s\n", tok, sock); err != nil {
		c.Close()
		return nil, err
	}
	br := bufio.NewReaderSize(c, 64)
	ack, err := br.ReadString('\n')
	if err != nil || ack != "OK\n" {
		c.Close()
		return nil, fmt.Errorf("assistant pod: %s is not being served", sock)
	}
	_ = c.SetDeadline(time.Time{})
	return &readerConn{Conn: c, r: br}, nil
}

type readerConn struct {
	net.Conn
	r *bufio.Reader
}

func (r *readerConn) Read(p []byte) (int, error) { return r.r.Read(p) }

// addr is the relay's address: the pod's IP, or a port-forward to it.
func (h *K8sHost) addr(ctx context.Context, person string) (string, error) {
	name := podName(person)
	if h.Forward {
		h.mu.Lock()
		f := h.fwds[person]
		h.mu.Unlock()
		if f == nil {
			var err error
			if f, err = h.Kube.Forward(ctx, "pod/"+name, relayPort); err != nil {
				return "", err
			}
			h.mu.Lock()
			if old := h.fwds[person]; old != nil {
				f.Close()
				f = old
			} else {
				h.fwds[person] = f
			}
			h.mu.Unlock()
		}
		return f.Addr(), nil
	}
	var pod kube.Pod
	found, err := h.Kube.Get(ctx, "pod", name, &pod)
	if err != nil {
		return "", err
	}
	if !found || pod.Status.PodIP == "" {
		return "", fmt.Errorf("assistant pod %s has no address", name)
	}
	return net.JoinHostPort(pod.Status.PodIP, strconv.Itoa(relayPort)), nil
}

func (h *K8sHost) forget(person string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if f := h.fwds[person]; f != nil {
		f.Close()
		delete(h.fwds, person)
	}
}

// Token is the pod's relay token, which its agent host takes too.
func (h *K8sHost) Token(ctx context.Context, person string) (string, error) {
	return h.token(ctx, person, false)
}

// token is the pod's relay token, made once and kept in its Secret.
func (h *K8sHost) token(ctx context.Context, person string, create bool) (string, error) {
	h.mu.Lock()
	if t := h.tokens[person]; t != "" {
		h.mu.Unlock()
		return t, nil
	}
	h.mu.Unlock()
	var sec kube.Secret
	found, err := h.Kube.Get(ctx, "secret", secretName(person), &sec)
	if err != nil {
		return "", err
	}
	tok := strings.TrimSpace(string(sec.Data["relay-token"]))
	if !found || tok == "" {
		if !create {
			return "", fmt.Errorf("assistant pod for %s has no relay token", person)
		}
		b := make([]byte, 24)
		_, _ = rand.Read(b)
		tok = hex.EncodeToString(b)
		if err := h.Kube.Apply(ctx, h.relaySecret(person, tok)); err != nil {
			return "", err
		}
	}
	h.mu.Lock()
	h.tokens[person] = tok
	h.mu.Unlock()
	return tok, nil
}

var (
	_ Host      = (*K8sHost)(nil)
	_ io.Reader = (*readerConn)(nil)
)
