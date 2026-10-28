package flow

import "fmt"

// deriveParamRoles says how the document reads each parameter — as a model,
// as a harness — and holds the one rule that follows from it: stages that
// share a model parameter share a harness. Two harnesses rarely list the
// same model ids, so one value cannot be right for both; the author gives
// the odd stage its own parameter. "Share a harness" is read off the
// document, not off today's values: the same kind written at both stages,
// or the same $parameter — a literal at one and a parameter at the other
// may agree today and part tomorrow.
//
// What a form needs follows for free: a model parameter's Harness is the
// one harness its stages run on (a kind, or $name), so the form can list
// that harness's models for it (FLOWLANG §1).
func (p *parser) deriveParamRoles(f *Flow) error {
	// harnessOf is each agent stage's harness as written: the kind, or the
	// parameter's $name.
	harnessOf := map[string]string{}
	for i := range f.Stages {
		if st := &f.Stages[i]; st.Type == StageAgent {
			harnessOf[st.ID] = st.Harness
		}
	}
	for _, r := range p.refs {
		if r.Field == "harness" && r.Stage != "" {
			harnessOf[r.Stage] = "$" + r.Param
		}
	}
	type use struct{ stage, harness string }
	modelUses := map[string][]use{}
	roleSite := map[string]string{} // param → where its role was first seen
	for _, r := range p.refs {
		pr := p.params[r.Param]
		if pr == nil {
			continue
		}
		var role, site string
		switch r.Field {
		case "model":
			role, site = RoleModel, fmt.Sprintf("stage %q", r.Stage)
			modelUses[r.Param] = append(modelUses[r.Param], use{r.Stage, harnessOf[r.Stage]})
		case "harness":
			role, site = RoleHarness, fmt.Sprintf("stage %q", r.Stage)
		case "ask.harness":
			role, site = RoleHarness, "the Ask pane"
		default:
			continue
		}
		if pr.Role != "" && pr.Role != role {
			return p.errf(pr.Line, "parameters: $%s is read as a %s (%s) and as a %s (%s): one parameter, one role", pr.Name, pr.Role, roleSite[pr.Name], role, site)
		}
		if pr.Role == "" {
			pr.Role, roleSite[pr.Name] = role, site
		}
	}
	for i := range p.plist {
		pr := &p.plist[i]
		uses := modelUses[pr.Name]
		if len(uses) == 0 {
			continue
		}
		first := uses[0]
		for _, u := range uses[1:] {
			if u.harness != first.harness {
				line := 0
				if st := f.Stage(u.stage); st != nil {
					line = st.Line
				}
				return p.errf(line, "stage %q runs on harness %s and stage %q on %s, yet both read $%s as their model: stages sharing a model parameter must share a harness — two harnesses rarely offer the same model ids; give one of them its own parameter",
					first.stage, harnessWord(first.harness), u.stage, harnessWord(u.harness), pr.Name)
			}
		}
		pr.Harness = first.harness
	}
	return nil
}

// harnessWord spells a stage's harness as written, for an error.
func harnessWord(h string) string {
	if h == "" {
		return "(unbound)"
	}
	return h
}
