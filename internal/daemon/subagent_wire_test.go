package daemon

import (
	"context"
	"log/slog"
	"testing"

	"github.com/eduardosanmartin/forge/internal/agent"
	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/llm"
	"github.com/eduardosanmartin/forge/internal/perms"
	"github.com/eduardosanmartin/forge/internal/tools"
)

func TestSessionManager_WiresSpawnSubagentTool(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	store := newTestStore()
	llmReg := newTestLLMRegistry()
	tmp := t.TempDir()
	policy := perms.PermissionsPolicy{
		FS: perms.FSPermissions{Read: []string{"./**"}, Write: []string{"./**"}},
		Shell: perms.ShellPermissions{Allow: []string{}},
		Git:   perms.GitPermissions{Allow: []string{}},
	}
	eng, err := perms.New(policy, tmp, logger)
	if err != nil {
		t.Fatalf("perms: %v", err)
	}
	toolsReg := tools.NewDefaultRegistry(eng, tmp, logger)
	emergency := NewEmergencyState(logger)
	cfg := config.Defaults()
	permsEng := &testPermsEngine{}
	mgr := NewSessionManager(store, llmReg, toolsReg, emergency, logger, cfg, permsEng, store, WithV1Deps(agent.V1Deps{}))
	if mgr == nil || mgr.agent == nil {
		t.Fatal("manager/agent not created")
	}
	if _, ok := toolsReg.Get("spawn_subagent"); !ok {
		t.Fatal("spawn_subagent tool not wired")
	}
	mgr.wireSubagentTool()
	if _, ok := toolsReg.Get("spawn_subagent"); !ok {
		t.Fatal("tool missing after second wire")
	}
}

func TestSpawnSubagentTool_PermissionIsCustomAllowed(t *testing.T) {
	tmp := t.TempDir()
	policy := perms.PermissionsPolicy{
		FS: perms.FSPermissions{Read: []string{"./**"}, Write: []string{"./**"}},
		Shell: perms.ShellPermissions{Allow: []string{}},
		Git:   perms.GitPermissions{Allow: []string{}},
		Custom: perms.CustomPermissions{Deny: []string{}},
	}
	eng, _ := perms.New(policy, tmp, nil)
	decision := eng.Check(perms.Request{Kind: perms.KindCustom, Command: "spawn_subagent"})
	if !decision.Allowed {
		t.Fatalf("spawn_subagent should be allowed via custom floor, got %v", decision)
	}
	policy.Custom.Deny = []string{"spawn_subagent"}
	eng2, _ := perms.New(policy, tmp, nil)
	decision2 := eng2.Check(perms.Request{Kind: perms.KindCustom, Command: "spawn_subagent"})
	if decision2.Allowed {
		t.Fatal("deny should block spawn_subagent")
	}
}

func TestSessionManager_SequentialMultiChildViaTool(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	store := newTestStore()
	tmp := t.TempDir()
	policy := perms.PermissionsPolicy{
		FS: perms.FSPermissions{Read: []string{"./**"}, Write: []string{"./**"}},
		Shell: perms.ShellPermissions{Allow: []string{}},
		Git:   perms.GitPermissions{Allow: []string{}},
	}
	eng, _ := perms.New(policy, tmp, logger)
	toolsReg := tools.NewDefaultRegistry(eng, tmp, logger)
	cfg := config.Defaults()
	callNum := 0
	provider := &sequentialSpawnProvider{callNum: &callNum}
	llmReg := &sequentialLLMRegistry{provider: provider}
	emergency := NewEmergencyState(logger)
	permsEng := &testPermsEngine{}
	mgr := NewSessionManager(store, llmReg, toolsReg, emergency, logger, cfg, permsEng, store, WithV1Deps(agent.V1Deps{}))

	session, err := mgr.CreateSession(t.Context(), nil)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	msgs, err := mgr.ExecuteTurn(t.Context(), session.ID, "delegate two tasks")
	if err != nil {
		t.Fatalf("execute turn: %v", err)
	}
	toolCount := 0
	hasA, hasB := false, false
	for _, m := range msgs {
		if m.Role == "tool" {
			toolCount++
			if containsStr(m.Content, "result A") {
				hasA = true
			}
			if containsStr(m.Content, "result B") {
				hasB = true
			}
		}
	}
	if toolCount != 2 {
		t.Fatalf("want 2 sequential child tool results, got %d (%+v)", toolCount, msgs)
	}
	if !hasA || !hasB {
		t.Fatalf("missing child summaries hasA=%v hasB=%v", hasA, hasB)
	}
}

func containsStr(s, sub string) bool { return len(s) >= len(sub) && (s == sub || len(s) > len(sub) && searchSub(s, sub)) }
func searchSub(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

type sequentialSpawnProvider struct {
	callNum *int
}

func (p *sequentialSpawnProvider) Chat(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	*p.callNum++
	lastUser := ""
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			lastUser = req.Messages[i].Content
			break
		}
	}
	if lastUser == "task A" {
		return llm.ChatResponse{Choices: []llm.Choice{{Message: llm.Message{Role: "assistant", Content: "result A"}}}}, nil
	}
	if lastUser == "task B" {
		return llm.ChatResponse{Choices: []llm.Choice{{Message: llm.Message{Role: "assistant", Content: "result B"}}}}, nil
	}
	if *p.callNum == 1 {
		return llm.ChatResponse{
			Choices: []llm.Choice{{Message: llm.Message{
				Role:    "assistant",
				Content: "spawn two",
				ToolCalls: []llm.ToolCall{
					{ID: "c1", Type: "function", Function: llm.ToolCallFunction{Name: "spawn_subagent", Arguments: `{"task":"task A"}`}},
					{ID: "c2", Type: "function", Function: llm.ToolCallFunction{Name: "spawn_subagent", Arguments: `{"task":"task B"}`}},
				},
			}}},
		}, nil
	}
	return llm.ChatResponse{Choices: []llm.Choice{{Message: llm.Message{Role: "assistant", Content: "parent done"}}}}, nil
}
func (p *sequentialSpawnProvider) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.StreamChunk, error) {
	return nil, nil
}
func (p *sequentialSpawnProvider) ListModels() ([]string, error) { return []string{"test-model"}, nil }
func (p *sequentialSpawnProvider) Close() error { return nil }

type sequentialLLMRegistry struct{ provider *sequentialSpawnProvider }

func (r *sequentialLLMRegistry) GetDefault() (llm.Provider, string) { return r.provider, "test-model" }
func (r *sequentialLLMRegistry) SetDefault(model string) error      { return nil }
func (r *sequentialLLMRegistry) ListAll() []llm.ModelInfo          { return nil }
func (r *sequentialLLMRegistry) Close() error                      { return nil }
func (r *sequentialLLMRegistry) Chat(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	return r.provider.Chat(ctx, req)
}
func (r *sequentialLLMRegistry) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.StreamChunk, error) {
	return r.provider.ChatStream(ctx, req)
}
