package tickets

// Jira's legacy status and type maps (docs/ticket-sync-plan.md § Data model,
// step 3). Before the tracker lists, a Jira integration's config carried two
// free-text maps:
//
//   - status_map "canonical=Jira Status Name, …" — which Jira status each
//     canonical status (todo, in_progress, in_review, done, blocked) meant,
//     laid over the stock workflow (To Do, In Progress, In Review, Done);
//     an empty name took a default away (that status unsupported);
//   - type_map "Jira Type=change type, …" — which change type an issue type
//     stood for.
//
// The migration cannot turn them into list rows (the lists hold the
// tracker's ids, unknown until the first metadata fetch), so it keeps the
// keys, and the first SyncLists that has the lists applies them by name —
// case aside — and drops them from the config: a mapped status holds that
// flow step (taken from whichever status the auto-suggestion gave it), a
// mapped type takes that change type. What names nothing on the tracker,
// or does not parse, is a warning; the keys are dropped all the same.

import (
	"context"
	"fmt"
	"strings"

	"apothecary/internal/ports/trackerport"
	"apothecary/internal/project/confstore"
)

const (
	legacyStatusMap = "status_map"
	legacyTypeMap   = "type_map"
)

// legacyDefaultStatuses is the stock Jira workflow the status map was laid
// over (the Jira adapter's former DefaultStatusMap).
var legacyDefaultStatuses = map[trackerport.FlowStep]string{
	trackerport.StepTodo:       "To Do",
	trackerport.StepInProgress: "In Progress",
	trackerport.StepInReview:   "In Review",
	trackerport.StepDone:       "Done",
}

// legacyEntries splits a map text into its "name=value" entries (commas or
// newlines between them), with a warning for each that has no "=".
func legacyEntries(key, text string) (entries [][2]string, warnings []string) {
	for part := range strings.SplitSeq(strings.ReplaceAll(text, "\n", ","), ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, value, ok := strings.Cut(part, "=")
		if !ok || strings.TrimSpace(name) == "" {
			warnings = append(warnings, fmt.Sprintf("%s: entry %q is not name=value; ignored", key, part))
			continue
		}
		entries = append(entries, [2]string{strings.TrimSpace(name), strings.TrimSpace(value)})
	}
	return entries, warnings
}

// parseLegacyStatusMap reads status_map as the adapter did: the defaults,
// overlaid entry by entry, an empty name deleting the step. given says which
// steps the text named (a default that matches nothing is no warning).
func parseLegacyStatusMap(text string) (m map[trackerport.FlowStep]string, given map[trackerport.FlowStep]bool, warnings []string) {
	m = map[trackerport.FlowStep]string{}
	for step, name := range legacyDefaultStatuses {
		m[step] = name
	}
	given = map[trackerport.FlowStep]bool{}
	entries, warnings := legacyEntries(legacyStatusMap, text)
	for _, e := range entries {
		step, ok := trackerport.ParseFlowStep(e[0])
		if !ok {
			warnings = append(warnings, fmt.Sprintf("%s: %q is not one of %s; ignored", legacyStatusMap, e[0], trackerport.StepNames(trackerport.FlowSteps())))
			continue
		}
		given[step] = true
		if e[1] == "" {
			delete(m, step)
		} else {
			m[step] = e[1]
		}
	}
	return m, given, warnings
}

// applyLegacyMaps applies an integration's status_map / type_map, if its
// config still has them, to its (freshly synced) lists, then drops them.
func applyLegacyMaps(ctx context.Context, conf *confstore.Service, integrationID string) ([]string, error) {
	cfg, err := conf.IntegrationConfig(ctx, integrationID)
	if err != nil {
		return nil, err
	}
	statusText, hasStatus := cfg[legacyStatusMap]
	typeText, hasType := cfg[legacyTypeMap]
	if !hasStatus && !hasType {
		return nil, nil
	}
	var warnings []string
	if hasStatus {
		w, err := applyLegacyStatusMap(ctx, conf, integrationID, fmt.Sprint(statusText))
		if err != nil {
			return nil, err
		}
		warnings = append(warnings, w...)
	}
	if hasType {
		w, err := applyLegacyTypeMap(ctx, conf, integrationID, fmt.Sprint(typeText))
		if err != nil {
			return nil, err
		}
		warnings = append(warnings, w...)
	}
	if err := conf.DropIntegrationConfig(ctx, integrationID, legacyStatusMap, legacyTypeMap); err != nil {
		return nil, err
	}
	return warnings, nil
}

func applyLegacyStatusMap(ctx context.Context, conf *confstore.Service, integrationID, text string) ([]string, error) {
	m, given, warnings := parseLegacyStatusMap(text)
	rows, err := conf.TrackerStatuses(ctx, integrationID)
	if err != nil {
		return nil, err
	}
	find := func(name string) int {
		for i, r := range rows {
			if strings.EqualFold(strings.TrimSpace(r.Name), name) {
				return i
			}
		}
		return -1
	}
	changed := false
	setStep := func(i int, step trackerport.FlowStep) {
		if rows[i].FlowStep != step {
			rows[i].FlowStep, changed = step, true
		}
	}
	held := map[int]trackerport.FlowStep{} // row → the step the map gave it
	for _, step := range trackerport.FlowSteps() {
		name, mapped := m[step]
		if !mapped {
			if given[step] {
				// "step=" — no status means this step any more.
				for i := range rows {
					if rows[i].FlowStep == step {
						setStep(i, "")
					}
				}
			}
			continue
		}
		i := find(name)
		switch {
		case i < 0:
			if given[step] {
				warnings = append(warnings, fmt.Sprintf("%s: %s=%s names no status of this tracker; ignored", legacyStatusMap, step, name))
			}
			continue
		case held[i] != "":
			warnings = append(warnings, fmt.Sprintf("%s: status %q is mapped to both %s and %s; a status holds one step, so it keeps %s", legacyStatusMap, rows[i].Name, held[i], step, held[i]))
			continue
		}
		for j := range rows {
			if j != i && rows[j].FlowStep == step {
				setStep(j, "")
			}
		}
		setStep(i, step)
		held[i] = step
	}
	if !changed {
		return warnings, nil
	}
	_, w, err := conf.PutTrackerStatuses(ctx, integrationID, rows, nil)
	if err != nil {
		return nil, err
	}
	return append(warnings, w...), nil
}

func applyLegacyTypeMap(ctx context.Context, conf *confstore.Service, integrationID, text string) ([]string, error) {
	entries, warnings := legacyEntries(legacyTypeMap, text)
	rows, err := conf.TrackerTypes(ctx, integrationID)
	if err != nil {
		return nil, err
	}
	changed := false
	for _, e := range entries {
		ct, ok := trackerport.ParseChangeType(e[1])
		if !ok {
			warnings = append(warnings, fmt.Sprintf("%s: %s=%s: %q is not one of feat, fix, hotfix, perf, docs, chore; ignored", legacyTypeMap, e[0], e[1], e[1]))
			continue
		}
		found := false
		for i := range rows {
			if strings.EqualFold(strings.TrimSpace(rows[i].Name), e[0]) {
				found = true
				if rows[i].ChangeType != ct {
					rows[i].ChangeType, changed = ct, true
				}
			}
		}
		if !found {
			warnings = append(warnings, fmt.Sprintf("%s: %s=%s names no issue type of this tracker; ignored", legacyTypeMap, e[0], e[1]))
		}
	}
	if !changed {
		return warnings, nil
	}
	if _, err := conf.PutTrackerTypes(ctx, integrationID, rows, nil); err != nil {
		return nil, err
	}
	return warnings, nil
}
