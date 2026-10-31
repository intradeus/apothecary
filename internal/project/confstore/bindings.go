package confstore

// Flow bindings: a project's values for a flow's parameters (flow v4
// `parameters:`). The document declares each parameter with its default and
// is valid on its own; the project binds values here (Workshop → Flows →
// Configure); a run may override. Stored beside the flow, never in it — the
// YAML and its hash are untouched by a binding, and a built-in template is
// bindable without being copied — and validated against the document the
// project actually runs: own copy first, template underneath.

import (
	"context"
	"errors"
	"maps"
	"strings"

	"apothecary/internal/flow"
)

// GetFlowBinding returns the project's binding for the flow; ErrNotFound
// when it bound nothing.
func (s *Service) GetFlowBinding(ctx context.Context, projectID, flowName string) (FlowBinding, error) {
	return s.db.GetFlowBinding(ctx, projectID, flowName)
}

// ListFlowBindings lists the project's bindings, by flow name.
func (s *Service) ListFlowBindings(ctx context.Context, projectID string) ([]FlowBinding, error) {
	return s.db.ListFlowBindings(ctx, projectID)
}

// PutFlowBinding stores the project's values for the flow's parameters:
// every key a parameter the flow declares (the six engine-known names
// included, bindable even when the document omits them), every value one
// its parameter accepts. An empty value unbinds that parameter, and a
// binding left with no values is removed — the project is back to the
// flow's defaults.
func (s *Service) PutFlowBinding(ctx context.Context, projectID, flowName string, params map[string]string) (FlowBinding, error) {
	if _, err := s.db.GetProject(ctx, projectID); err != nil {
		return FlowBinding{}, err
	}
	src, err := s.flowText(ctx, projectID, flowName)
	if err != nil {
		return FlowBinding{}, err
	}
	if _, err := flow.Parse(flowName, []byte(src)); err != nil {
		return FlowBinding{}, invalidf("flow %q does not parse, so it cannot be configured: %v", flowName, err)
	}
	values := map[string]string{}
	for k, v := range params {
		if v = strings.TrimSpace(v); v != "" {
			values[strings.TrimSpace(k)] = v
		}
	}
	// The same check a run makes at creation: the document re-parsed with
	// these values, so every parameter's own rules apply to what is bound —
	// a duration that parses, a model with a provider, an enum member.
	if _, err := flow.ParseBound(flowName, []byte(src), values); err != nil {
		return FlowBinding{}, invalidf("%v", err)
	}
	if len(values) == 0 {
		if err := s.db.DeleteFlowBinding(ctx, projectID, flowName); err != nil && !errors.Is(err, ErrNotFound) {
			return FlowBinding{}, err
		}
		return FlowBinding{ProjectID: projectID, FlowName: flowName, Params: values}, nil
	}
	b := FlowBinding{ProjectID: projectID, FlowName: flowName, Params: values, UpdatedAt: s.now().UTC()}
	if err := s.db.PutFlowBinding(ctx, b); err != nil {
		return FlowBinding{}, err
	}
	return b, nil
}

// DeleteFlowBinding puts the project back to the flow's defaults.
func (s *Service) DeleteFlowBinding(ctx context.Context, projectID, flowName string) error {
	return s.db.DeleteFlowBinding(ctx, projectID, flowName)
}

// Binding is what the engine reads at run creation (engine.FlowBindings):
// the project's values, or nil, nil when it bound nothing.
func (s *Service) Binding(ctx context.Context, projectID, flowName string) (map[string]string, error) {
	b, err := s.db.GetFlowBinding(ctx, projectID, flowName)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return maps.Clone(b.Params), nil
}
