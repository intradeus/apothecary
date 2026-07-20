// Package config holds the deployment-plane boot configuration (DESIGN2.md
// §1: env vars, see env.go) and the retired v1 factory.yaml parser, kept
// only as `factoryd import-config` input. Strict parsing: unknown fields
// are errors; yaml secrets are referenced by env-var name (token_env /
// *_env) and resolved at import time.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type File struct {
	Listen      string  `yaml:"listen"`
	PostgresDSN string  `yaml:"postgres_dsn"`
	Repo        Repo    `yaml:"repo"`
	Project     Project `yaml:"project"`
	GitLab      GitLab  `yaml:"gitlab"`
	Coder       Coder   `yaml:"coder"`
	Providers   Provs   `yaml:"providers"`
	FlowsDir    string  `yaml:"flows_dir"`
	RunsDir     string  `yaml:"runs_dir"`
	// CPUIDir is the built CPUI bundle; when set the engine serves it at /
	// (SPA fallback). Empty = API only.
	CPUIDir string `yaml:"cpui_dir"`
	Limits  Limits `yaml:"limits"`
	Reaper  Reaper `yaml:"reaper"`
	Otel    Otel   `yaml:"otel"`
}

type Repo struct {
	URL           string `yaml:"url"`
	DefaultBranch string `yaml:"default_branch"`
	AgentsMDPath  string `yaml:"agents_md_path"`
}

type Project struct {
	Commands map[string]string `yaml:"commands"`
}

type GitLab struct {
	BaseURL   string `yaml:"base_url"`
	TokenEnv  string `yaml:"token_env"`
	ProjectID int    `yaml:"project_id"`
}

type Coder struct {
	URL      string `yaml:"url"`
	TokenEnv string `yaml:"token_env"`
	Template string `yaml:"template"`
	// ExtraForwards are additional host-loopback ports reverse-forwarded
	// into workspaces alongside the engine port (dev convenience, e.g. a
	// local git daemon).
	ExtraForwards []int `yaml:"extra_forwards"`
}

type Provs struct {
	AnthropicEnv string `yaml:"anthropic_env"`
	OpenAIEnv    string `yaml:"openai_env"`
	// OpencodeAuthJSON is a path to an opencode auth.json seeded into
	// workspaces (subscription/router auth, §11 step 2).
	OpencodeAuthJSON string `yaml:"opencode_auth_json"`
}

type Limits struct {
	MaxConcurrentRuns int `yaml:"max_concurrent_runs"`
	MaxEnvironments   int `yaml:"max_environments"`
}

type Reaper struct {
	DestroyCompletedAfter time.Duration `yaml:"-"`
	// DestroyAbandoned is "never" in v1 (§13/§14); parsed for forward
	// compatibility but only "never" is accepted.
	DestroyAbandoned string `yaml:"destroy_abandoned"`

	RawDestroyCompletedAfter string `yaml:"destroy_completed_after"`
}

type Otel struct {
	Endpoint string `yaml:"endpoint"`
}

func Load(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

func Parse(data []byte) (*File, error) {
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	var f File
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	// Defaults (§13 example values are the defaults where sensible).
	// listen deviates from §13's 0.0.0.0: workspaces arrive via the Coder
	// reverse tunnel, so the API needs no non-loopback bind at all.
	if f.Listen == "" {
		f.Listen = "127.0.0.1:8080"
	}
	if f.Repo.DefaultBranch == "" {
		f.Repo.DefaultBranch = "main"
	}
	if f.Repo.AgentsMDPath == "" {
		f.Repo.AgentsMDPath = "AGENTS.md"
	}
	if f.Limits.MaxConcurrentRuns == 0 {
		f.Limits.MaxConcurrentRuns = 3
	}
	if f.Limits.MaxEnvironments == 0 {
		f.Limits.MaxEnvironments = 6
	}
	if f.Reaper.RawDestroyCompletedAfter == "" {
		f.Reaper.RawDestroyCompletedAfter = "48h"
	}
	d, err := time.ParseDuration(f.Reaper.RawDestroyCompletedAfter)
	if err != nil || d <= 0 {
		return nil, fmt.Errorf("config: reaper.destroy_completed_after: invalid duration %q", f.Reaper.RawDestroyCompletedAfter)
	}
	f.Reaper.DestroyCompletedAfter = d
	if f.Reaper.DestroyAbandoned == "" {
		f.Reaper.DestroyAbandoned = "never"
	}
	if f.Reaper.DestroyAbandoned != "never" {
		return nil, fmt.Errorf("config: reaper.destroy_abandoned must be \"never\" in v1, got %q", f.Reaper.DestroyAbandoned)
	}

	// Validation: name the exact missing item.
	switch {
	case f.Repo.URL == "":
		return nil, fmt.Errorf("config: repo.url is required")
	case f.PostgresDSN == "":
		return nil, fmt.Errorf("config: postgres_dsn is required")
	case f.FlowsDir == "":
		return nil, fmt.Errorf("config: flows_dir is required")
	}
	if _, ok := f.Project.Commands["setup"]; !ok {
		return nil, fmt.Errorf("config: project.commands.setup is required (mandatory, §1)")
	}
	return &f, nil
}

// ResolveEnv reads a secret by its configured env-var name; empty name or
// unset var yields "".
func ResolveEnv(name string) string {
	if name == "" {
		return ""
	}
	return os.Getenv(name)
}
