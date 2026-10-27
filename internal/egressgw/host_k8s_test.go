package egressgw

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"testing"

	"apothecary/internal/kube"
)

// kubeScript answers kubectl invocations by their verb and object.
type kubeScript struct {
	calls   [][]string
	stdins  [][]byte
	answers map[string]string // key: "get secret apo-gw-acme" etc.
}

func (k *kubeScript) Run(_ context.Context, _ string, args []string, stdin []byte, _ io.Writer, _ ...string) ([]byte, []byte, int, error) {
	k.calls = append(k.calls, args)
	k.stdins = append(k.stdins, stdin)
	var words []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--namespace" || a == "-o" || a == "-l" {
			i++
			continue
		}
		if !strings.HasPrefix(a, "-") {
			words = append(words, a)
		}
	}
	key := strings.Join(words, " ")
	for prefix, out := range k.answers {
		if strings.HasPrefix(key, prefix) {
			return []byte(out), nil, 0, nil
		}
	}
	return nil, nil, 0, nil
}

func (k *kubeScript) applied(kind string) map[string]any {
	for _, b := range k.stdins {
		var m map[string]any
		if json.Unmarshal(b, &m) == nil && m["kind"] == kind {
			return m
		}
	}
	return nil
}

func TestKeypairIsWireGuardShaped(t *testing.T) {
	priv, pub, err := newKeypair()
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{priv, pub} {
		b, err := base64.StdEncoding.DecodeString(k)
		if err != nil || len(b) != 32 {
			t.Fatalf("key %q: %d bytes, %v", k, len(b), err)
		}
	}
	if priv == pub {
		t.Fatal("public key equals private key")
	}
}

func TestFreeIPSkipsUsedAndNetworkAddresses(t *testing.T) {
	used := map[string]bool{"10.90.0.1": true, "10.90.0.2": true}
	if ip, _ := freeIP(used); ip != "10.90.0.3" {
		t.Fatalf("got %s", ip)
	}
	for n := 2; n < 256; n++ {
		used["10.90.0."+strconv.Itoa(n)] = true
	}
	if ip, _ := freeIP(used); ip != "10.90.1.1" {
		t.Fatalf("got %s (must skip .255 and .0)", ip)
	}
}

func TestAttachRunAllocatesAndReuses(t *testing.T) {
	k := &kubeScript{answers: map[string]string{
		"get secret apo-gw-acme":  `{"metadata":{"name":"apo-gw-acme"},"data":{"wg-private-key":"` + b64("gwpriv") + `","wg-public-key":"` + b64("GWPUB") + `","control-token":"` + b64("t") + `"}}`,
		"get service apo-gw-acme": `{"metadata":{"name":"apo-gw-acme"},"spec":{"clusterIP":"10.43.7.7","ports":[]}}`,
		"get secrets":             `{"items":[{"metadata":{"name":"apo-wg-other"},"data":{"wg-ip":"` + b64("10.90.0.2") + `"}}]}`,
	}}
	h := &K8sHost{Kube: &kube.Client{Namespace: "ws", R: k}, Image: "gw:1"}
	att, err := h.AttachRun(context.Background(), "ACME", "", "run1")
	if err != nil {
		t.Fatal(err)
	}
	if att.GatewayIP != "10.90.0.1" || att.Subnet != "10.90.0.3/32" || att.Peer.IP != "10.90.0.3" ||
		att.WGEndpoint != "10.43.7.7:51820" || att.WGSecret != "apo-wg-run1" {
		t.Fatalf("attachment = %+v", att)
	}
	sec := k.applied("Secret")
	sd, _ := sec["stringData"].(map[string]any)
	if sd["gw-public-key"] != "GWPUB" || sd["wg-ip"] != "10.90.0.3" {
		t.Fatalf("run secret = %v", sec)
	}
	// A re-provision of the same run keeps its identity.
	k.answers["get secrets"] = `{"items":[{"metadata":{"name":"apo-wg-run1"},"data":{"wg-ip":"` + b64("10.90.0.9") +
		`","wg-private-key":"` + b64("P") + `","wg-public-key":"` + b64("Q") + `"}}]}`
	att, err = h.AttachRun(context.Background(), "ACME", "", "run1")
	if err != nil || att.Peer.IP != "10.90.0.9" || att.Peer.PublicKey != "Q" {
		t.Fatalf("reuse: %+v %v", att, err)
	}
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func TestGatewayManifests(t *testing.T) {
	h := &K8sHost{Kube: &kube.Client{Namespace: "ws"}, Image: "gw:1",
		ProtectCIDRs: []string{"10.42.0.0/16", "169.254.0.0/16"}, EngineNamespace: "apothecary"}
	labels := gwLabels("ACME")
	d := h.deployment("ACME", labels, nil)
	args := strings.Join(d.Spec.Template.Spec.Containers[0].Args, " ")
	for _, want := range []string{"--wg-key /secrets/wg-private-key", "--wg-addr 10.90.0.1/16", "--control-listen :9041",
		"--state-dir /state", "--dns-upstream resolv.conf", "--protect-cidrs 10.42.0.0/16,169.254.0.0/16"} {
		if !strings.Contains(args, want) {
			t.Errorf("gateway args missing %q: %s", want, args)
		}
	}
	if *d.Spec.Template.Spec.InitContainers[0].SecurityContext.Privileged != true ||
		d.Spec.Template.Spec.Containers[0].SecurityContext.Privileged != nil {
		t.Error("only the sysctl init container may be privileged")
	}
	np := h.networkPolicy("ACME", labels)
	b, _ := json.Marshal(np)
	for _, want := range []string{`"podSelector":{"matchLabels":{"apothecary.io/gateway":"acme"}}`,
		`"apothecary.io/sandbox":"true"`, `"port":51820`, `"kubernetes.io/metadata.name":"apothecary"`,
		`"except":["10.42.0.0/16","169.254.0.0/16"]`, `"k8s-app":"kube-dns"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("network policy missing %s: %s", want, b)
		}
	}
}
