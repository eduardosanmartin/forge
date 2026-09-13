package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/eduardosanmartin/forge/internal/agent"
	"github.com/eduardosanmartin/forge/internal/llm"
	"github.com/eduardosanmartin/forge/internal/store"
)

// namedProviderResolver matches LLM registries that can resolve a provider
// by registration name (llm.Registry implements it). Used up-front to
// validate named fanout providers before children spawn.
type namedProviderResolver interface {
	GetProvider(name string) (llm.Provider, bool)
}

// resolvedModel is one parsed --models entry: the provider may be empty
// (bare model entry -> default provider).
type resolvedModel struct {
	Provider string
	Model    string
}

// Note on "provider/model" vs slash-bearing model names: an entry with "/"
// uses the prefix as the provider name only when a provider is registered
// under that exact name; otherwise the whole entry is treated as a model
// name on the default provider (so "org/model-v2" still works).
func (m *SessionManager) resolveFanoutModels(entries []string) ([]resolvedModel, error) {
	if len(entries) == 0 {
		return nil, fmt.Errorf("models is required (one \"provider/model\" entry per child)")
	}
	resolver, hasResolver := m.llmReg.(namedProviderResolver)
	out := make([]resolvedModel, 0, len(entries))
	for i, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			return nil, fmt.Errorf("models[%d] is empty", i)
		}
		if idx := strings.Index(entry, "/"); idx > 0 {
			provider, model := entry[:idx], entry[idx+1:]
			if model == "" {
				return nil, fmt.Errorf("models[%d] %q has an empty model name", i, entry)
			}
			// Only treat the prefix as a provider when it is actually
			// registered; slash-bearing model names stay model-only.
			useProvider := provider != ""
			if hasResolver {
				p, ok := resolver.GetProvider(provider)
				useProvider = ok && p != nil
			}
			if useProvider {
				out = append(out, resolvedModel{Provider: provider, Model: model})
				continue
			}
		}
		out = append(out, resolvedModel{Model: entry})
	}
	return out, nil
}

// validateFanoutProviders checks that every named provider exists. It is
// best-effort for registries without name resolution (e.g. mocks in
// unit tests): those skip validation and children surface the error
// through SpawnChild, which performs the same check at spawn time.
func (m *SessionManager) validateFanoutProviders(specs []resolvedModel) error {
	resolver, ok := m.llmReg.(namedProviderResolver)
	if !ok {
		return nil
	}
	names := map[string]bool{}
	for _, s := range specs {
		if s.Provider != "" {
			names[s.Provider] = true
		}
	}
	for name := range names {
		if _, found := resolver.GetProvider(name); !found {
			return fmt.Errorf("unknown provider %q", name)
		}
	}
	return nil
}

// Fanout executes the same task on N child sessions, one per requested
// model, via the existing bounded SpawnChildren pool (RF-9.3). Children are
// branched from the parent (existing BranchSession lineage metadata) and the
// RPC waits for all of them: children are LLM-bound, so the caller's
// request context is tied to the whole fanout, not to the connection.
func (m *SessionManager) Fanout(ctx context.Context, params FanoutParams) (*FanoutResult, error) {
	if strings.TrimSpace(params.Task) == "" {
		return nil, fmt.Errorf("task is required")
	}
	if m.agent == nil {
		return nil, errors.New("agent not initialized")
	}
	resolved, err := m.resolveFanoutModels(params.Models)
	if err != nil {
		return nil, err
	}

	parentID := params.SessionID
	if parentID == "" {
		parent, err := m.store.CreateSession(ctx, map[string]any{
			"label":       "fanout",
			"fanout":      true,
			"fanout_task": params.Task,
		})
		if err != nil {
			return nil, fmt.Errorf("create fanout parent: %w", err)
		}
		parentID = parent.ID
	} else if _, err := m.store.GetSession(ctx, parentID); err != nil {
		return nil, fmt.Errorf("%w: %s", store.ErrSessionNotFound, params.SessionID)
	}
	if err := m.validateFanoutProviders(resolved); err != nil {
		return nil, err
	}

	specs := make([]agent.ChildSpec, len(resolved))
	for i, r := range resolved {
		specs[i] = agent.ChildSpec{
			Task:          params.Task,
			MaxIterations: params.MaxIterations,
			TokenBudget:   params.TokenBudget,
			Provider:      r.Provider,
			Model:         r.Model,
		}
	}

	results, errs := m.agent.SpawnChildren(ctx, parentID, specs)

	out := &FanoutResult{ParentSessionID: parentID, Children: make([]FanoutChildResult, len(results))}
	for i, res := range results {
		child := FanoutChildResult{
			ChildSessionID: res.ChildSessionID,
			Provider:       resolved[i].Provider,
			Model:          resolved[i].Model,
			Success:        res.Success,
			Summary:        res.Summary,
		}
		if errs[i] != nil {
			child.Success = false
			child.Error = errs[i].Error()
		} else if res.Error != "" {
			child.Error = res.Error
		}
		// Hydrate lineage metadata (children are branched sessions) for
		// follow-up with session.compare.
		if sess, gErr := m.store.GetSession(ctx, res.ChildSessionID); gErr == nil {
			if v, ok := sess.Metadata["branch_parent"].(string); ok {
				child.BranchParent = v
			}
			if v, ok := sess.Metadata["branch_root"].(string); ok {
				child.BranchRoot = v
			}
			if seq, ok := toAnyInt(sess.Metadata["branch_at_seq"]); ok {
				child.BranchAtSeq = seq
			}
		}
		out.Children[i] = child
	}
	return out, nil
}

// toAnyInt converts metadata numeric values (JSON round-trips make them float64).
func toAnyInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	}
	return 0, false
}
