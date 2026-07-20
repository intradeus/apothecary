package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"factory/internal/config"
	"factory/internal/confstore"
	"factory/internal/flow"
	"factory/internal/pgstore"
)

// cmdImport is the one-shot v1 -> v2 migration (DESIGN2 §14): reads the
// retired factory.yaml (plus the env vars it names), creates the
// integrations, the one project, flows and settings. The project gets
// workspace.template set to the configured Coder template — the §8.4
// compatibility path, so behavior stays byte-identical until layered
// images are adopted.
func cmdImport(args []string) error {
	fs := flag.NewFlagSet("import-config", flag.ExitOnError)
	cfgPath := fs.String("config", "/etc/factory/factory.yaml", "v1 engine config file")
	force := fs.Bool("force", false, "import even if the DB already holds projects")
	_ = fs.Parse(args)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	boot, err := config.FromEnv()
	if err != nil {
		return err
	}
	key, err := boot.Key()
	if err != nil {
		return err
	}
	ctx := context.Background()
	store, err := pgstore.Open(ctx, boot.PostgresDSN)
	if err != nil {
		return err
	}
	defer store.Close()

	reg := registry(false)
	conf := confstore.New(store, key)
	conf.KindValid = reg.Valid

	existing, err := conf.ListProjects(ctx)
	if err != nil {
		return err
	}
	if len(existing) > 0 && !*force {
		return fmt.Errorf("import-config: DB already holds %d project(s); re-run with -force to import anyway", len(existing))
	}

	created := func(what, name string) { fmt.Printf("created %-12s %s\n", what, name) }

	// Flows first: the project's default_flow must resolve.
	if cfg.FlowsDir != "" {
		paths, err := filepath.Glob(filepath.Join(cfg.FlowsDir, "*.yaml"))
		if err != nil {
			return err
		}
		for _, p := range paths {
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			f, err := flow.Parse(filepath.Base(p), data)
			if err != nil {
				return fmt.Errorf("flow %s: %w", p, err)
			}
			if _, err := conf.SaveFlow(ctx, f.Name, string(data)); err != nil {
				return fmt.Errorf("flow %s: %w", p, err)
			}
			created("flow", f.Name)
		}
	}

	// Integrations. Secrets come from the env vars the yaml names — the
	// import must run with the old environment present.
	forge, err := conf.CreateIntegration(ctx, confstore.Integration{
		Port: confstore.PortForge, Kind: "gitlab", Name: "gitlab",
		Config:  map[string]any{"base_url": cfg.GitLab.BaseURL},
		Secrets: secretIf("token", config.ResolveEnv(cfg.GitLab.TokenEnv)),
	})
	if err != nil {
		return fmt.Errorf("forge: %w", err)
	}
	created("integration", "forge/gitlab "+forge.ID)

	provisioner, err := conf.CreateIntegration(ctx, confstore.Integration{
		Port: confstore.PortProvisioner, Kind: "coder", Name: "coder",
		Config: map[string]any{
			"url":            cfg.Coder.URL,
			"template":       cfg.Coder.Template,
			"extra_forwards": cfg.Coder.ExtraForwards,
		},
		Secrets: secretIf("token", config.ResolveEnv(cfg.Coder.TokenEnv)),
	})
	if err != nil {
		return fmt.Errorf("provisioner: %w", err)
	}
	created("integration", "provisioner/coder "+provisioner.ID)

	harnessSecrets := map[string]string{}
	if cfg.Providers.OpencodeAuthJSON != "" {
		data, err := os.ReadFile(cfg.Providers.OpencodeAuthJSON)
		if err != nil {
			return fmt.Errorf("providers.opencode_auth_json: %w", err)
		}
		harnessSecrets["auth_json"] = string(data)
	}
	harness, err := conf.CreateIntegration(ctx, confstore.Integration{
		Port: confstore.PortHarness, Kind: "opencode", Name: "opencode",
		Secrets: harnessSecrets,
	})
	if err != nil {
		return fmt.Errorf("harness: %w", err)
	}
	created("integration", "harness/opencode "+harness.ID)

	// Model auth is harness-owned (post-M8): provider API keys from the v1
	// env vars are NOT imported — fold them into the harness auth_json
	// (opencode reads its auth.json natively).
	for kind, envName := range map[string]string{
		"anthropic": cfg.Providers.AnthropicEnv,
		"openai":    cfg.Providers.OpenAIEnv,
	} {
		if config.ResolveEnv(envName) != "" {
			fmt.Printf("note: $%s is set — add this %s key to the opencode integration's auth_json in the UI\n", envName, kind)
		}
	}

	template := cfg.Coder.Template
	if template == "" {
		template = "factory"
	}
	project, err := conf.CreateProject(ctx, confstore.Project{
		Name:          "default",
		ForgeID:       forge.ID,
		ProvisionerID: provisioner.ID,
		HarnessID:     harness.ID,
		Repo: confstore.RepoConfig{
			URL:           cfg.Repo.URL,
			Locator:       strconv.Itoa(cfg.GitLab.ProjectID),
			DefaultBranch: cfg.Repo.DefaultBranch,
			AgentsMDPath:  cfg.Repo.AgentsMDPath,
		},
		Commands:    cfg.Project.Commands,
		Workspace:   confstore.WorkspaceConfig{Template: template},
		DefaultFlow: "v1",
	})
	if err != nil {
		return fmt.Errorf("project: %w", err)
	}
	created("project", project.Name+" "+project.ID)

	if err := conf.PutSettings(ctx, confstore.Settings{
		MaxConcurrentRuns:     cfg.Limits.MaxConcurrentRuns,
		MaxEnvironments:       cfg.Limits.MaxEnvironments,
		DestroyCompletedAfter: cfg.Reaper.RawDestroyCompletedAfter,
		DestroyAbandoned:      cfg.Reaper.DestroyAbandoned,
	}); err != nil {
		return fmt.Errorf("settings: %w", err)
	}
	created("settings", "limits + reaper")

	fmt.Println("import complete — factory.yaml is retired; serve boots from FACTORY_* env vars alone")
	return nil
}

func secretIf(key, value string) map[string]string {
	if value == "" {
		return map[string]string{}
	}
	return map[string]string{key: value}
}
