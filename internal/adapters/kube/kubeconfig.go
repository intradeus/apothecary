package kube

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// Kubeconfig turns an integration's kubeconfig setting into a path for
// --kubeconfig: an absolute path is used as is; anything else is kubeconfig
// CONTENT (a secret field), written 0600 under dir and removed by cleanup.
// "" means the pod's service account when running in a cluster (an explicit
// kubeconfig, see inClusterKubeconfig), else kubectl's own default.
func Kubeconfig(dir, value string) (path string, cleanup func(), err error) {
	value = strings.TrimSpace(value)
	nothing := func() {}
	if value == "" {
		if p, ok, err := inClusterKubeconfig(dir); ok || err != nil {
			return p, nothing, err
		}
		return "", nothing, nil
	}
	if strings.HasPrefix(value, "/") && !strings.ContainsAny(value, "\n:") {
		return value, nothing, nil
	}
	sum := sha256.Sum256([]byte(value))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nothing, err
	}
	path = filepath.Join(dir, hex.EncodeToString(sum[:8])+".kubeconfig")
	if err := os.WriteFile(path, []byte(value+"\n"), 0o600); err != nil {
		return "", nothing, err
	}
	return path, func() { _ = os.Remove(path) }, nil
}

// saDir is a var for tests.
var saDir = "/var/run/secrets/kubernetes.io/serviceaccount"

// inClusterKubeconfig writes a kubeconfig for the pod's own service account.
// kubectl does fall back to it by itself — but only when no config override
// is given, and every API call here passes --request-timeout, which counts as
// one: the fallback is silently skipped and kubectl dials localhost:8080.
// The token is referenced by path (it is rotated), never copied.
func inClusterKubeconfig(dir string) (string, bool, error) {
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return "", false, nil
	}
	if _, err := os.Stat(filepath.Join(saDir, "token")); err != nil {
		return "", false, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", true, err
	}
	content := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: in-cluster
  cluster:
    server: https://%s
    certificate-authority: %s/ca.crt
users:
- name: service-account
  user:
    tokenFile: %s/token
contexts:
- name: in-cluster
  context: {cluster: in-cluster, user: service-account}
current-context: in-cluster
`, net.JoinHostPort(host, port), saDir, saDir)
	path := filepath.Join(dir, "in-cluster.kubeconfig")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return "", true, err
	}
	return path, true, nil
}
