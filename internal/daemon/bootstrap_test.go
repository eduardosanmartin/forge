package daemon

import (
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/eduardosanmartin/forge/internal/bootstrap"
	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/llm"
)

// newTestSessionManagerForBootstrap mirrors newTestSessionManagerForRuns but
// keeps a reference to the fake LLM registry so a test can script the
// canned assistant response bootstrapProposer's ExecuteTurn call receives.
func newTestSessionManagerForBootstrap() (*SessionManager, *testLLMRegistry) {
	logger := slog.New(slog.DiscardHandler)
	st := newTestStore()
	llmReg := newTestLLMRegistry()
	toolsReg := newTestToolsRegistry()
	emergency := NewEmergencyState(logger)
	cfg := config.Defaults()
	permsEng := newTestPermsEngine()
	mgr := NewSessionManager(st, llmReg, toolsReg, emergency, logger, cfg, permsEng, st)
	return mgr, llmReg
}

func setCannedAssistantResponse(reg *testLLMRegistry, content string) {
	reg.provider.response = llm.ChatResponse{
		ID:    "test-response",
		Model: "test-model",
		Choices: []llm.Choice{
			{
				Index:        0,
				Message:      llm.Message{Role: "assistant", Content: content},
				FinishReason: "stop",
			},
		},
		Usage: &llm.Usage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30},
	}
}

// TestHandler_BootstrapStart_And_Status exercises the full real path (Fase 1
// of hojaDeRuta-wizard-inteligente.md) end to end through the daemon's own
// JSON-RPC dispatch: bootstrap.start -> Handler -> bootstrap.Manager ->
// bootstrapProposer -> SessionManager.CreateSession/ExecuteTurn -> the fake
// LLM's canned response -> bootstrap.ParseProposedItems -> a stored
// in-memory session, then bootstrap.status reads that same session back
// over the wire. No CLI frontend exists yet (Fase 5), so this is the
// closest equivalent to a live spot-check: real handler, real Manager, real
// session plumbing, only the LLM call itself is faked.
func TestHandler_BootstrapStart_And_Status(t *testing.T) {
	mgr, llmReg := newTestSessionManagerForBootstrap()
	setCannedAssistantResponse(llmReg, `[
		{"kind": "RF", "text": "Registrar un gasto con monto, quien pago, y entre quienes se divide"},
		{"kind": "RF", "text": "Calcular el balance neto de cada persona"},
		{"kind": "RNF", "text": "Los datos persisten localmente"}
	]`)
	h := NewHandler(mgr, slog.New(slog.DiscardHandler), nil, nil)

	startReq := makeRequest(MethodBootstrapStart, BootstrapStartParams{Idea: "una app que trackea gastos compartidos entre roommates"})
	startResp := h.HandleRequest(t.Context(), startReq)
	if startResp.Error != nil {
		t.Fatalf("bootstrap.start returned an error: %+v", startResp.Error)
	}
	var startState bootstrap.State
	if err := json.Unmarshal(startResp.Result, &startState); err != nil {
		t.Fatalf("unmarshal bootstrap.start result: %v", err)
	}
	if startState.ID == "" {
		t.Fatal("expected a non-empty bootstrap id")
	}
	if len(startState.Items) != 3 {
		t.Fatalf("got %d items, want 3", len(startState.Items))
	}
	if startState.Items[0].ID != "RF-1" || startState.Items[2].ID != "RNF-1" {
		t.Fatalf("unexpected item ids: %+v", startState.Items)
	}

	statusReq := makeRequest(MethodBootstrapStatus, BootstrapStatusParams{BootstrapID: startState.ID})
	statusResp := h.HandleRequest(t.Context(), statusReq)
	if statusResp.Error != nil {
		t.Fatalf("bootstrap.status returned an error: %+v", statusResp.Error)
	}
	var statusState bootstrap.State
	if err := json.Unmarshal(statusResp.Result, &statusState); err != nil {
		t.Fatalf("unmarshal bootstrap.status result: %v", err)
	}
	if statusState.Idea != startState.Idea {
		t.Errorf("Idea = %q, want %q", statusState.Idea, startState.Idea)
	}
	if len(statusState.Items) != 3 {
		t.Fatalf("got %d items on status, want 3", len(statusState.Items))
	}
}

// TestHandler_BootstrapStatus_UnknownID confirms the wire-level error
// mapping: an unrecognized bootstrap_id must come back as
// ErrCodeBootstrapNotFound, not a generic internal error.
func TestHandler_BootstrapStatus_UnknownID(t *testing.T) {
	mgr, llmReg := newTestSessionManagerForBootstrap()
	setCannedAssistantResponse(llmReg, `[]`)
	h := NewHandler(mgr, slog.New(slog.DiscardHandler), nil, nil)

	req := makeRequest(MethodBootstrapStatus, BootstrapStatusParams{BootstrapID: "does-not-exist"})
	resp := h.HandleRequest(t.Context(), req)
	if resp.Error == nil {
		t.Fatal("expected an error for an unknown bootstrap id")
	}
	if resp.Error.Code != ErrCodeBootstrapNotFound {
		t.Errorf("Error.Code = %d, want %d (ErrCodeBootstrapNotFound)", resp.Error.Code, ErrCodeBootstrapNotFound)
	}
}

// TestHandler_BootstrapStart_EmptyIdea confirms idea is validated before any
// LLM call is made.
func TestHandler_BootstrapStart_EmptyIdea(t *testing.T) {
	mgr, llmReg := newTestSessionManagerForBootstrap()
	setCannedAssistantResponse(llmReg, `[]`)
	h := NewHandler(mgr, slog.New(slog.DiscardHandler), nil, nil)

	req := makeRequest(MethodBootstrapStart, BootstrapStartParams{Idea: "   "})
	resp := h.HandleRequest(t.Context(), req)
	if resp.Error == nil {
		t.Fatal("expected an error for a blank idea")
	}
	if resp.Error.Code != ErrCodeInvalidParams {
		t.Errorf("Error.Code = %d, want %d (ErrCodeInvalidParams)", resp.Error.Code, ErrCodeInvalidParams)
	}
}
