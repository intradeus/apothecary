package acpagent

import (
	"context"
	"fmt"
	"path"
	"strings"

	"apothecary/internal/flow/agentspec"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/ports/envport"
)

// RolesDir holds one role file per agent spec, for harnesses with no
// named-agent registry a session can start as (codex, omp): the role prompt
// is delivered in-band, prepended to the session's first prompt.
const RolesDir = "/apothecary/agents"

// WriteRoles writes one role file per spec (bootstrap step 4).
func WriteRoles(ctx context.Context, envp envport.EnvProvisioner, env envport.EnvID, specs []*agentspec.Spec) error {
	for _, s := range specs {
		if err := envp.WriteFile(ctx, env, path.Join(RolesDir, s.Name+".role"), []byte(RoleText(s)), 0o644); err != nil {
			return fmt.Errorf("write role %s: %w", s.Name, err)
		}
	}
	return nil
}

// RolePreamble is a Profile.Preamble reading the stage agent's role file. An
// agent with no role file fails rather than running role-less.
func RolePreamble(envp envport.EnvProvisioner, harness string) func(context.Context, envport.EnvID, agentport.AgentSpec) (string, error) {
	return func(ctx context.Context, env envport.EnvID, spec agentport.AgentSpec) (string, error) {
		if spec.Agent == "" {
			return "", nil
		}
		role, err := envp.ReadFile(ctx, env, path.Join(RolesDir, spec.Agent+".role"))
		if err != nil {
			return "", fmt.Errorf("%s: role for agent %q: %w", harness, spec.Agent, err)
		}
		return string(role), nil
	}
}

// RoleText renders the in-band system preamble. read_only/allow_bash are
// instruction-level only here — weaker than opencode's tool config; a
// documented residual (§13), mitigated as ever by human review of the
// branch. Every role but the Ask pane's ends with AskHumanRules: the name
// codex and omp list the tool under is not known here, so the text says it
// may be prefixed.
func RoleText(s *agentspec.Spec) string {
	var b strings.Builder
	b.WriteString("# ROLE\n")
	b.WriteString(strings.TrimSpace(s.Prompt))
	b.WriteString("\n")
	if s.ReadOnly {
		b.WriteString("\nIMPORTANT: This role is READ-ONLY with respect to the repository: do not modify, create or delete repository files except the explicitly required output artifacts.\n")
	}
	if !s.AllowBash {
		b.WriteString("Do not run shell commands for this task.\n")
	}
	if s.Name != agentspec.AskAgent {
		b.WriteString("\n")
		b.WriteString(AskHumanRules(""))
	}
	b.WriteString("\n")
	return b.String()
}
