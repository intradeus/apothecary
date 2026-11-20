package assistant

import (
	"strings"
	"testing"
)

// Each harness's skills go to its user-level skills directory, inside the
// session's home.
func TestProfilesHaveASkillsDirectory(t *testing.T) {
	for kind, p := range profiles {
		if p.skills == nil {
			t.Errorf("%s has no skills directory", kind)
			continue
		}
		if dir := p.skills("/state/s1"); !strings.HasPrefix(dir, "/state/s1/.") || !strings.HasSuffix(dir, "/skills") {
			t.Errorf("%s's skills directory = %s", kind, dir)
		}
	}
}
