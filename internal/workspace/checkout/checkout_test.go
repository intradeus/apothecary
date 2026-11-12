package checkout

import (
	"strings"
	"testing"
)

func TestBranchScript(t *testing.T) {
	if got, want := BranchScript("feat/x", "main", ""),
		`git switch 'feat/x' 2>/dev/null || git switch -c 'feat/x' 'origin/main'`; got != want {
		t.Errorf("fork = %q", got)
	}
	// Re-entered first in both forms: a restart must never recreate a branch;
	// a merge request's branch is taken from the fetched head, never forked
	// from the target.
	got := BranchScript("feat/x", "main", "refs/pull/4/head")
	if want := `git switch 'feat/x' 2>/dev/null || { git fetch origin 'refs/pull/4/head' && git switch -c 'feat/x' FETCH_HEAD; }`; got != want {
		t.Errorf("fetch = %q", got)
	}
	// Shell-quoted, not Go-quoted: a name the shell would read otherwise
	// stays one word.
	got = BranchScript("it's", "a b", "")
	if !strings.Contains(got, `'it'\''s'`) || !strings.Contains(got, `'origin/a b'`) {
		t.Errorf("quoting = %q", got)
	}
}

func TestCredentialLine(t *testing.T) {
	if got, err := CredentialLine("https://git.example/a.git", "", "p"); err != nil || got != "https://oauth2:p@git.example" {
		t.Errorf("default user: %q, %v", got, err)
	}
	if got, err := CredentialLine("https://git.example:8443/a.git", "u@x", "p:w"); err != nil || got != "https://u%40x:p%3Aw@git.example:8443" {
		t.Errorf("escaping: %q, %v", got, err)
	}
	if got, err := CredentialLine("https://gitlab.local/grp/repo.git", "", "p@ss w"); err != nil || got != "https://oauth2:p%40ss+w@gitlab.local" {
		t.Errorf("space: %q, %v", got, err)
	}
	for _, bad := range []string{"", "not a url", "/just/a/path", "::"} {
		if got, err := CredentialLine(bad, "u", "p"); err == nil {
			t.Errorf("CredentialLine(%q) = %q, want an error", bad, got)
		}
	}
}

func TestCheckBranch(t *testing.T) {
	valid := []string{
		"feat/PROJ-123-login-form",
		"fix/issue_42.hotfix",
		"a/b/c",
		"release-1.0",
	}
	for _, s := range valid {
		if err := CheckBranch(s); err != nil {
			t.Errorf("CheckBranch(%q) = %v, want nil", s, err)
		}
	}
	invalid := []string{
		"/leading", "trailing/", "a//b",
		"a..b", "a@{b",
		"has space", "tab\there",
		"seg/.hidden", "ends.", "x.lock",
		"car^et", "col:on", "ast*erisk", "back\\slash",
	}
	for _, s := range invalid {
		if err := CheckBranch(s); err == nil {
			t.Errorf("CheckBranch(%q) = nil, want error", s)
		}
	}
}
