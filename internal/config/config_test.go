package config

import (
	"strings"
	"testing"
	"time"
)

// the §13 example, verbatim shape
const full = `
listen: "0.0.0.0:8080"
postgres_dsn: postgres://factory:pw@localhost/factory
repo: { url: "https://gitlab.com/g/p.git", default_branch: main, agents_md_path: AGENTS.md }
project:
  commands:
    setup:  "./scripts/factory-setup.sh"
    verify: "make lint && make test"
gitlab: { base_url: "https://gitlab.com", token_env: GITLAB_TOKEN, project_id: 123 }
coder: { url: "http://localhost:7080", token_env: CODER_SESSION_TOKEN, template: factory }
providers: { anthropic_env: ANTHROPIC_API_KEY, openai_env: OPENAI_API_KEY }
flows_dir: /etc/factory/flows
runs_dir: /var/lib/factory/runs
limits: { max_concurrent_runs: 3, max_environments: 6 }
reaper: { destroy_completed_after: 48h, destroy_abandoned: never }
otel: { endpoint: "" }
`

func TestParseFullExample(t *testing.T) {
	f, err := Parse([]byte(full))
	if err != nil {
		t.Fatal(err)
	}
	if f.Repo.URL != "https://gitlab.com/g/p.git" || f.GitLab.ProjectID != 123 ||
		f.Project.Commands["verify"] != "make lint && make test" {
		t.Errorf("parsed = %+v", f)
	}
	if f.Reaper.DestroyCompletedAfter != 48*time.Hour {
		t.Errorf("reaper window = %v", f.Reaper.DestroyCompletedAfter)
	}
}

func TestDefaults(t *testing.T) {
	min := `
postgres_dsn: postgres://x@localhost/f
repo: { url: "https://gitlab.local/g/p.git" }
project: { commands: { setup: "./s.sh" } }
flows_dir: /tmp/flows
`
	f, err := Parse([]byte(min))
	if err != nil {
		t.Fatal(err)
	}
	if f.Listen != "0.0.0.0:8080" || f.Repo.DefaultBranch != "main" ||
		f.Repo.AgentsMDPath != "AGENTS.md" || f.Limits.MaxConcurrentRuns != 3 ||
		f.Limits.MaxEnvironments != 6 || f.Reaper.DestroyCompletedAfter != 48*time.Hour ||
		f.Reaper.DestroyAbandoned != "never" {
		t.Errorf("defaults = %+v", f)
	}
}

func TestValidationNamesMissingItem(t *testing.T) {
	cases := []struct{ name, doc, want string }{
		{"no repo url", "postgres_dsn: p\nproject: { commands: { setup: s } }\nflows_dir: /f\n", "repo.url"},
		{"no dsn", "repo: { url: u }\nproject: { commands: { setup: s } }\nflows_dir: /f\n", "postgres_dsn"},
		{"no flows dir", "postgres_dsn: p\nrepo: { url: u }\nproject: { commands: { setup: s } }\n", "flows_dir"},
		{"no setup", "postgres_dsn: p\nrepo: { url: u }\nflows_dir: /f\nproject: { commands: { verify: v } }\n", "setup"},
		{"unknown field", full + "kanban: true\n", "kanban"},
		{"bad reaper duration", strings.Replace(full, "48h", "soonish", 1), "destroy_completed_after"},
		{"reaper abandoned not never", strings.Replace(full, "never", "24h", 1), "destroy_abandoned"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.doc))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want mention of %q", err, tc.want)
			}
		})
	}
}
