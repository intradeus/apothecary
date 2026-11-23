package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"apothecary/internal/adapters/egressgw"
	"apothecary/internal/adapters/kube"
	"apothecary/internal/ports/envport"
)

// Golden sandbox pods pin podManifest's output for each runtime and storage,
// so a change to how a pod is built shows up as a diff even when the tests'
// own assertions still pass. GOLDEN=record writes the files
// (testdata/pods/<case>.json); otherwise each case is compared with its
// file. A golden file changes only in a change that means to change the pod.
func TestSandboxPodGolden(t *testing.T) {
	att := egressgw.Attachment{GatewayIP: "10.90.0.1", Subnet: "10.90.0.5/32", WGEndpoint: "10.43.9.9:51820", WGSecret: "apo-wg-apo-x"}
	spec := envport.EnvSpec{Project: "P1", ProjectName: "My App"}
	for _, c := range []struct {
		name    string
		shape   func(*KubeConfig)
		gateway bool
	}{
		{"privileged-pvc", func(*KubeConfig) {}, false},
		{"privileged-emptydir-gateway", func(c *KubeConfig) { c.Storage = "emptydir" }, true},
		{"privileged-class-override-gateway", func(c *KubeConfig) { c.RuntimeClass = "gvisor-dind" }, true},
		{"sysbox-pvc-gateway", func(c *KubeConfig) {
			c.Runtime = envport.RuntimeSysbox
			c.RuntimeClass = "sysbox-runc"
		}, true},
		{"sysbox-default-class-emptydir", func(c *KubeConfig) {
			c.Runtime, c.Storage = envport.RuntimeSysbox, "emptydir"
		}, false},
		{"sized-sysbox-pvc", func(c *KubeConfig) { c.Runtime = envport.RuntimeSysbox }, false},
		{"capped-privileged-pvc", func(*KubeConfig) {}, false},
		{"sized-kata-emptydir-gateway", func(c *KubeConfig) {
			c.Runtime, c.Storage = envport.RuntimeKata, "emptydir"
		}, true},
		{"sized-kata-block-claim-gateway", func(c *KubeConfig) {
			c.Runtime, c.VMStorageClass = envport.RuntimeKata, "block"
		}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			k := &kubeFake{secrets: map[string]string{}}
			a := New(Config{RunsDir: testRunsDir, Kube: goldenKubeConfig(k, c.shape)})
			h := a.host.(*k8sHost)
			var a2 egressgw.Attachment
			if c.gateway {
				a2 = att
			}
			spec := spec
			if strings.HasPrefix(c.name, "sized-") {
				spec.Size = envport.SandboxSize{MilliCPU: 4000, MemoryMiB: 8192}
			}
			if strings.HasPrefix(c.name, "capped-") {
				spec.SizeCap = envport.SandboxSize{MilliCPU: 16000, MemoryMiB: 65536}
			}
			pod, err := h.podManifest(context.Background(), "apo-x", spec, a2)
			if err != nil {
				t.Fatal(err)
			}
			got, err := json.MarshalIndent(pod, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')
			path := filepath.Join("testdata", "pods", c.name+".json")
			if os.Getenv("GOLDEN") == "record" {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (GOLDEN=record writes it)", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("pod for %s differs from %s:\n%s", c.name, path, got)
			}
		})
	}
}

// goldenKubeConfig is the test cluster's integration, shaped by the case.
func goldenKubeConfig(k *kubeFake, shape func(*KubeConfig)) *KubeConfig {
	c := &KubeConfig{
		Kube:         &kube.Client{Namespace: "ws", R: k},
		SandboxImage: "sbx:1", ShimImage: "shim:1", ToolsImage: "tools:1", GatewayImage: "gw:1",
		EngineURL: "http://10.43.0.20:8080", EngineNamespace: "apothecary",
	}
	shape(c)
	return c
}
