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

// bootstrapStartViaHandler is a small helper for the Fase 2 curation tests
// below: runs bootstrap.start through the real handler and returns the
// resulting state, failing the test on any error.
func bootstrapStartViaHandler(t *testing.T, h *Handler, idea string) bootstrap.State {
	t.Helper()
	resp := h.HandleRequest(t.Context(), makeRequest(MethodBootstrapStart, BootstrapStartParams{Idea: idea}))
	if resp.Error != nil {
		t.Fatalf("bootstrap.start returned an error: %+v", resp.Error)
	}
	var st bootstrap.State
	if err := json.Unmarshal(resp.Result, &st); err != nil {
		t.Fatalf("unmarshal bootstrap.start result: %v", err)
	}
	return st
}

// TestHandler_BootstrapSelect_And_Discard exercises Fase 2's curation RPCs
// end to end through the real dispatch, same rigor as the Fase 1 test
// above.
func TestHandler_BootstrapSelect_And_Discard(t *testing.T) {
	mgr, llmReg := newTestSessionManagerForBootstrap()
	setCannedAssistantResponse(llmReg, `[
		{"kind": "RF", "text": "Registrar un gasto"},
		{"kind": "RF", "text": "Calcular el balance"}
	]`)
	h := NewHandler(mgr, slog.New(slog.DiscardHandler), nil, nil)
	st := bootstrapStartViaHandler(t, h, "idea")

	selResp := h.HandleRequest(t.Context(), makeRequest(MethodBootstrapSelect, BootstrapSelectParams{BootstrapID: st.ID, Indices: []int{1}}))
	if selResp.Error != nil {
		t.Fatalf("bootstrap.select returned an error: %+v", selResp.Error)
	}
	var afterSelect bootstrap.State
	if err := json.Unmarshal(selResp.Result, &afterSelect); err != nil {
		t.Fatalf("unmarshal bootstrap.select result: %v", err)
	}
	if afterSelect.Items[0].Status != bootstrap.StatusAccepted {
		t.Errorf("item 1 status = %q, want accepted", afterSelect.Items[0].Status)
	}

	discResp := h.HandleRequest(t.Context(), makeRequest(MethodBootstrapDiscard, BootstrapDiscardParams{BootstrapID: st.ID, Indices: []int{2}}))
	if discResp.Error != nil {
		t.Fatalf("bootstrap.discard returned an error: %+v", discResp.Error)
	}
	var afterDiscard bootstrap.State
	if err := json.Unmarshal(discResp.Result, &afterDiscard); err != nil {
		t.Fatalf("unmarshal bootstrap.discard result: %v", err)
	}
	if afterDiscard.Items[1].Status != bootstrap.StatusDiscarded {
		t.Errorf("item 2 status = %q, want discarded", afterDiscard.Items[1].Status)
	}

	// bad index: mapped to ErrCodeInvalidParams via bootstrap.ErrInvalidRequest
	badResp := h.HandleRequest(t.Context(), makeRequest(MethodBootstrapSelect, BootstrapSelectParams{BootstrapID: st.ID, Indices: []int{99}}))
	if badResp.Error == nil || badResp.Error.Code != ErrCodeInvalidParams {
		t.Fatalf("bootstrap.select with a bad index: got %+v, want ErrCodeInvalidParams", badResp.Error)
	}
}

// TestHandler_BootstrapSuggestOwn adds a user-authored item without any LLM
// call and confirms it lands with correct numbering.
func TestHandler_BootstrapSuggestOwn(t *testing.T) {
	mgr, llmReg := newTestSessionManagerForBootstrap()
	setCannedAssistantResponse(llmReg, `[{"kind": "RF", "text": "Registrar un gasto"}]`)
	h := NewHandler(mgr, slog.New(slog.DiscardHandler), nil, nil)
	st := bootstrapStartViaHandler(t, h, "idea")

	resp := h.HandleRequest(t.Context(), makeRequest(MethodBootstrapSuggestOwn, BootstrapSuggestOwnParams{
		BootstrapID: st.ID, Kind: "RNF", Text: "Debe funcionar sin conexion a internet",
	}))
	if resp.Error != nil {
		t.Fatalf("bootstrap.suggest_own returned an error: %+v", resp.Error)
	}
	var got bootstrap.State
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("unmarshal bootstrap.suggest_own result: %v", err)
	}
	if len(got.Items) != 2 || got.Items[1].ID != "RNF-1" {
		t.Fatalf("unexpected items after suggest_own: %+v", got.Items)
	}

	// invalid kind: mapped to ErrCodeInvalidParams
	badResp := h.HandleRequest(t.Context(), makeRequest(MethodBootstrapSuggestOwn, BootstrapSuggestOwnParams{
		BootstrapID: st.ID, Kind: "functional", Text: "algo",
	}))
	if badResp.Error == nil || badResp.Error.Code != ErrCodeInvalidParams {
		t.Fatalf("bootstrap.suggest_own with an invalid kind: got %+v, want ErrCodeInvalidParams", badResp.Error)
	}
}

// TestHandler_BootstrapClarify exercises Fase 3's clarification RPC end to
// end: real Handler, real bootstrap.Manager, real
// SessionManager.bootstrapClarifier wiring — only the LLM call itself is
// faked. Confirms the answer comes back and that Clarify never touches the
// item's Status.
func TestHandler_BootstrapClarify(t *testing.T) {
	mgr, llmReg := newTestSessionManagerForBootstrap()
	setCannedAssistantResponse(llmReg, `[{"kind": "RF", "text": "Registrar un gasto"}]`)
	h := NewHandler(mgr, slog.New(slog.DiscardHandler), nil, nil)
	st := bootstrapStartViaHandler(t, h, "idea")

	setCannedAssistantResponse(llmReg, "RF-1 asume un solo grupo por gasto.")
	resp := h.HandleRequest(t.Context(), makeRequest(MethodBootstrapClarify, BootstrapClarifyParams{
		BootstrapID: st.ID, Index: 1, Question: "¿puede un gasto pertenecer a mas de un grupo?",
	}))
	if resp.Error != nil {
		t.Fatalf("bootstrap.clarify returned an error: %+v", resp.Error)
	}
	var result BootstrapClarifyResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal bootstrap.clarify result: %v", err)
	}
	if result.Answer != "RF-1 asume un solo grupo por gasto." {
		t.Errorf("Answer = %q", result.Answer)
	}

	statusResp := h.HandleRequest(t.Context(), makeRequest(MethodBootstrapStatus, BootstrapStatusParams{BootstrapID: st.ID}))
	var afterState bootstrap.State
	if err := json.Unmarshal(statusResp.Result, &afterState); err != nil {
		t.Fatalf("unmarshal bootstrap.status result: %v", err)
	}
	if afterState.Items[0].Status != bootstrap.StatusPending {
		t.Errorf("item 1 status = %q, want pending (clarify must not decide it)", afterState.Items[0].Status)
	}

	// unknown item index: mapped to ErrCodeInvalidParams
	badResp := h.HandleRequest(t.Context(), makeRequest(MethodBootstrapClarify, BootstrapClarifyParams{
		BootstrapID: st.ID, Index: 99, Question: "algo",
	}))
	if badResp.Error == nil || badResp.Error.Code != ErrCodeInvalidParams {
		t.Fatalf("bootstrap.clarify with a bad index: got %+v, want ErrCodeInvalidParams", badResp.Error)
	}

	// unknown bootstrap id: mapped to ErrCodeBootstrapNotFound
	unknownResp := h.HandleRequest(t.Context(), makeRequest(MethodBootstrapClarify, BootstrapClarifyParams{
		BootstrapID: "does-not-exist", Index: 1, Question: "algo",
	}))
	if unknownResp.Error == nil || unknownResp.Error.Code != ErrCodeBootstrapNotFound {
		t.Fatalf("bootstrap.clarify with an unknown bootstrap id: got %+v, want ErrCodeBootstrapNotFound", unknownResp.Error)
	}
}

// TestHandler_BootstrapSuggestMore re-consults the (fake) model for a
// second round and confirms both rounds' items coexist with continuous
// numbering, driven entirely through the real RPC dispatch.
func TestHandler_BootstrapSuggestMore(t *testing.T) {
	mgr, llmReg := newTestSessionManagerForBootstrap()
	setCannedAssistantResponse(llmReg, `[{"kind": "RF", "text": "Registrar un gasto"}]`)
	h := NewHandler(mgr, slog.New(slog.DiscardHandler), nil, nil)
	st := bootstrapStartViaHandler(t, h, "idea")

	setCannedAssistantResponse(llmReg, `[{"kind": "RNF", "text": "Persistencia local"}]`)
	resp := h.HandleRequest(t.Context(), makeRequest(MethodBootstrapSuggestMore, BootstrapSuggestMoreParams{BootstrapID: st.ID}))
	if resp.Error != nil {
		t.Fatalf("bootstrap.suggest_more returned an error: %+v", resp.Error)
	}
	var got bootstrap.State
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("unmarshal bootstrap.suggest_more result: %v", err)
	}
	if len(got.Items) != 2 || got.Items[0].ID != "RF-1" || got.Items[1].ID != "RNF-1" {
		t.Fatalf("unexpected items after suggest_more: %+v", got.Items)
	}
}
