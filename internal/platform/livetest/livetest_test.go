package livetest

import (
	"fmt"
	"strings"
	"testing"
)

// rec records what Skip did instead of ending the test.
type rec struct {
	testing.TB
	skipped, failed string
}

func (r *rec) Helper()                          {}
func (r *rec) Skipf(format string, args ...any) { r.skipped = fmt.Sprintf(format, args...) }
func (r *rec) Fatalf(format string, args ...any) {
	r.failed = fmt.Sprintf(format, args...)
}

func TestSkipSkipsLocally(t *testing.T) {
	t.Setenv(StrictEnv, "")
	r := &rec{}
	Skip(r, "no cluster: %s", "KUBECONFIG unset")
	if r.skipped != "no cluster: KUBECONFIG unset" || r.failed != "" {
		t.Errorf("skipped %q, failed %q", r.skipped, r.failed)
	}
	if Strict() {
		t.Error("strict without the variable")
	}
}

func TestSkipFailsUnderStrict(t *testing.T) {
	t.Setenv(StrictEnv, "1")
	r := &rec{}
	Skip(r, "no cluster: %s", "KUBECONFIG unset")
	if r.skipped != "" || !strings.HasPrefix(r.failed, StrictEnv+"=1") || !strings.HasSuffix(r.failed, "no cluster: KUBECONFIG unset") {
		t.Errorf("skipped %q, failed %q", r.skipped, r.failed)
	}
}
