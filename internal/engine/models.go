package engine

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"apothecary/internal/flow"
	"apothecary/internal/ports"
)

// modelCheck is what checking a flow's models found.
type modelCheck struct {
	// missing: stages whose model their harness cannot reach — a run would
	// fail when that stage opens its session.
	missing []string
	// unverified: harnesses whose model list could not be fetched (provider
	// unreachable, no credentials yet). Not a verdict on the flow.
	unverified []string
}

// modelListTimeout bounds the lookups: they hit the providers' live model
// endpoints, and run creation must not hang on a slow one.
const modelListTimeout = 15 * time.Second

// checkModels verifies that every agent stage's model is one its harness can
// reach with the project's credentials. Providers retire models (a flow
// pinning yesterday's model fails mid-run, after the workspace was built and
// earlier stages spent their budget) — this catches it before any of that.
func checkModels(ctx context.Context, res *Resolution, fl *flow.Flow) modelCheck {
	var out modelCheck
	if res.Models == nil || fl == nil {
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, modelListTimeout)
	defer cancel()
	lists := map[string][]string{}
	failed := map[string]bool{}
	for i := range fl.Stages {
		st := &fl.Stages[i]
		if st.Type != flow.StageAgent || st.Model == "" {
			continue
		}
		kind := st.Harness
		if kind == "" {
			kind = res.HarnessKind
		}
		if failed[kind] {
			continue
		}
		models, ok := lists[kind]
		if !ok {
			var err error
			models, err = res.Models(ctx, kind)
			if err != nil {
				failed[kind] = true
				out.unverified = append(out.unverified, fmt.Sprintf("%s: %v", kind, err))
				continue
			}
			lists[kind] = models
		}
		if models == nil {
			continue // the harness takes any model
		}
		if !contains(models, st.Model) {
			msg := fmt.Sprintf("stage %q: model %q is not available to harness %s", st.ID, st.Model, kind)
			if near := closestModels(st.Model, models, 3); len(near) > 0 {
				msg += " (closest: " + strings.Join(near, ", ") + ")"
			}
			out.missing = append(out.missing, msg)
		}
	}
	return out
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// closestModels suggests replacements: the same provider first, ranked by how
// long a prefix they share with the missing id (glm-5.1 → glm-5.2, glm-5.3).
func closestModels(want string, models []string, n int) []string {
	provider, _, _ := strings.Cut(want, "/")
	type cand struct {
		id    string
		score int
	}
	var cs []cand
	for _, m := range models {
		p, _, _ := strings.Cut(m, "/")
		if p != provider {
			continue
		}
		score := 0
		for score < len(m) && score < len(want) && m[score] == want[score] {
			score++
		}
		if score > len(provider)+1 {
			cs = append(cs, cand{m, score})
		}
	}
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].score != cs[j].score {
			return cs[i].score > cs[j].score
		}
		return cs[i].id < cs[j].id
	})
	var out []string
	for i := 0; i < len(cs) && i < n; i++ {
		out = append(out, cs[i].id)
	}
	return out
}

// agentAccepts reports whether an agent's advertised ids cover a flow model:
// exactly, by its bare id (codex spells models without the provider), or as
// a variant of it ("gpt-5/high").
func agentAccepts(advertised []string, model string) bool {
	bare := model
	if _, rest, ok := strings.Cut(model, "/"); ok {
		bare = rest
	}
	for _, a := range advertised {
		if a == model || a == bare || strings.HasPrefix(a, model+"/") || strings.HasPrefix(a, bare+"/") {
			return true
		}
	}
	return false
}

// agentModelTimeout bounds asking the workspace's agent for its models (it
// may have to start first).
const agentModelTimeout = 90 * time.Second

// agentModelCheck asks each harness's agent, IN the workspace, which models
// it accepts, and reports what the stages pin that it does not — the
// authoritative check: the provider may still serve a model the installed
// harness version does not know. unasked lists harnesses whose agent could
// not be asked (or said nothing); their stages are not judged.
func agentModelCheck(ctx context.Context, res *Resolution, env ports.EnvID, stages []flow.Stage) (missing, unasked []string) {
	asked := map[string][]string{}
	skipped := map[string]bool{}
	for i := range stages {
		st := &stages[i]
		if st.Type != flow.StageAgent || st.Model == "" {
			continue
		}
		kind := st.Harness
		if kind == "" {
			kind = res.HarnessKind
		}
		if skipped[kind] {
			continue
		}
		models, ok := asked[kind]
		if !ok {
			lister, can := res.runnerFor(kind).(ports.AgentModelLister)
			if !can {
				skipped[kind] = true
				continue
			}
			actx, cancel := context.WithTimeout(ctx, agentModelTimeout)
			got, err := lister.AgentModels(actx, env)
			cancel()
			if err != nil || len(got) == 0 {
				why := "it reports no model list"
				if err != nil {
					why = err.Error()
				}
				unasked = append(unasked, fmt.Sprintf("%s: %s", kind, why))
				skipped[kind] = true
				continue
			}
			asked[kind], models = got, got
		}
		if !agentAccepts(models, st.Model) {
			msg := fmt.Sprintf("stage %q: model %q is not offered by the %s agent in the workspace", st.ID, st.Model, kind)
			if near := closestModels(st.Model, models, 3); len(near) > 0 {
				msg += " (closest: " + strings.Join(near, ", ") + ")"
			}
			missing = append(missing, msg)
		}
	}
	return missing, unasked
}

// checkAgentModels is agentModelCheck for the run's flow and workspace.
// Harnesses that cannot be asked are logged, not failed: their stage
// surfaces any problem itself.
func (x *exec) checkAgentModels() []string {
	missing, unasked := agentModelCheck(x.ctx, x.res, x.rs.envID, x.rs.fl.Stages)
	if len(unasked) > 0 {
		x.e.log.Warn("could not ask the workspace agent for its models",
			"run", x.rs.runID, "harnesses", strings.Join(unasked, "; "))
	}
	return missing
}
