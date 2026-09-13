package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/llm"
)

// TestJobQueue_List verifies that each detached turn registers a job with session+seq ID.
func TestJobQueue_List(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	st := newTestStore()
	llmReg := newTestLLMRegistry()
	toolsReg := newTestToolsRegistry()
	emergency := NewEmergencyState(logger)
	cfg := config.Defaults()
	permsEng := newTestPermsEngine()
	mgr := NewSessionManager(st, llmReg, toolsReg, emergency, logger, cfg, permsEng, st)

	sess, _ := mgr.CreateSession(context.Background(), nil)
	if _, err := mgr.ExecuteTurn(context.Background(), sess.ID, "hello"); err != nil {
		t.Fatalf("execute turn: %v", err)
	}
	jobs := mgr.ListJobs()
	if len(jobs) != 1 {
		t.Fatalf("want 1 job, got %d", len(jobs))
	}
	j := jobs[0]
	if j.SessionID != sess.ID {
		t.Errorf("job session %q want %q", j.SessionID, sess.ID)
	}
	if j.ID != sess.ID+":1" {
		t.Errorf("job ID %q want %q", j.ID, sess.ID+":1")
	}
	if j.Status != JobDone {
		t.Errorf("status %q want done", j.Status)
	}
	if j.Seq != 1 {
		t.Errorf("seq %d want 1", j.Seq)
	}
	// Second turn should increment seq
	if _, err := mgr.ExecuteTurn(context.Background(), sess.ID, "hello 2"); err != nil {
		t.Fatalf("second turn: %v", err)
	}
	jobs = mgr.ListJobs()
	if len(jobs) != 2 {
		t.Fatalf("want 2 jobs after second turn, got %d", len(jobs))
	}
	found := false
	for _, jj := range jobs {
		if jj.ID == sess.ID+":2" && jj.Status == JobDone {
			found = true
		}
	}
	if !found {
		t.Fatalf("second job not found in %+v", jobs)
	}
}

// TestJobQueue_Follow verifies re-attach via GetMessagesSince while job is running.
func TestJobQueue_Follow(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	st := newTestStore()
	provider := &blockingProvider{
		delay: 200 * time.Millisecond,
		response: llm.ChatResponse{
			ID:    "test-response",
			Model: "test-model",
			Choices: []llm.Choice{{
				Index: 0, Message: llm.Message{Role: "assistant", Content: "follow me"}, FinishReason: "stop",
			}},
			Usage: &llm.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
		},
	}
	llmReg := &blockingRegistry{provider: provider}
	toolsReg := newTestToolsRegistry()
	emergency := NewEmergencyState(logger)
	cfg := config.Defaults()
	permsEng := newTestPermsEngine()
	mgr := NewSessionManager(st, llmReg, toolsReg, emergency, logger, cfg, permsEng, st)

	sess, _ := mgr.CreateSession(context.Background(), nil)

	done := make(chan error, 1)
	go func() {
		_, err := mgr.ExecuteTurn(context.Background(), sess.ID, "hello follow")
		done <- err
	}()
	// Give scheduler time to register the running job
	time.Sleep(20 * time.Millisecond)
	jobs := mgr.ListJobs()
	if len(jobs) != 1 {
		t.Fatalf("want 1 running job, got %d", len(jobs))
	}
	j := jobs[0]
	if j.Status != JobRunning {
		t.Fatalf("want running, got %q", j.Status)
	}
	// Follow via polling GetMessagesSince from StartSeq; initially may see only user message or none
	// Poll until done, simulating CLI follow loop
	startSeq := j.StartSeq
	// Poll loop: wait for turn to finish
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("turn failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("turn timeout")
	}
	// After completion, GetMessagesSince should return the turn's messages
	msgs, err := mgr.GetMessagesSince(context.Background(), sess.ID, startSeq)
	if err != nil {
		t.Fatalf("GetMessagesSince: %v", err)
	}
	if len(msgs) < 2 {
		t.Fatalf("follow: want at least 2 messages since %d, got %d", startSeq, len(msgs))
	}
	// Job should now be done
	j2, ok := mgr.GetJob(j.ID)
	if !ok {
		t.Fatalf("job disappeared after finish")
	}
	if j2.Status != JobDone {
		t.Fatalf("follow: final status %q want done", j2.Status)
	}
}

// TestJobQueue_Cancel verifies job cancel terminates a running turn.
func TestJobQueue_Cancel(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	st := newTestStore()
	provider := &blockingProvider{
		delay: 500 * time.Millisecond,
		response: llm.ChatResponse{
			ID:    "test-response",
			Model: "test-model",
			Choices: []llm.Choice{{
				Index: 0, Message: llm.Message{Role: "assistant", Content: "should be canceled"}, FinishReason: "stop",
			}},
		},
	}
	llmReg := &blockingRegistry{provider: provider}
	toolsReg := newTestToolsRegistry()
	emergency := NewEmergencyState(logger)
	cfg := config.Defaults()
	permsEng := newTestPermsEngine()
	mgr := NewSessionManager(st, llmReg, toolsReg, emergency, logger, cfg, permsEng, st)

	sess, _ := mgr.CreateSession(context.Background(), nil)

	done := make(chan error, 1)
	go func() {
		_, err := mgr.ExecuteTurn(context.Background(), sess.ID, "hello cancel")
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	jobs := mgr.ListJobs()
	if len(jobs) != 1 {
		t.Fatalf("want 1 job, got %d", len(jobs))
	}
	jobID := jobs[0].ID

	// Cancel via manager
	res, err := mgr.CancelJob(jobID)
	if err != nil {
		t.Fatalf("CancelJob: %v", err)
	}
	if res.Status != JobCanceled {
		t.Fatalf("cancel status %q want canceled", res.Status)
	}

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected canceled turn to return error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled turn did not return")
	}

	// Verify persisted status is canceled
	j2, ok := mgr.GetJob(jobID)
	if !ok {
		t.Fatalf("job not found after cancel")
	}
	if j2.Status != JobCanceled {
		t.Fatalf("final job status %q want canceled", j2.Status)
	}
}

// TestJobQueue_HandlerRPC verifies the three RPC methods.
func TestJobQueue_HandlerRPC(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	st := newTestStore()
	llmReg := newTestLLMRegistry()
	toolsReg := newTestToolsRegistry()
	emergency := NewEmergencyState(logger)
	cfg := config.Defaults()
	permsEng := newTestPermsEngine()
	mgr := NewSessionManager(st, llmReg, toolsReg, emergency, logger, cfg, permsEng, st)
	h := NewHandler(mgr, logger, nil, nil)

	sess, _ := mgr.CreateSession(context.Background(), nil)
	if _, err := mgr.ExecuteTurn(context.Background(), sess.ID, "hello rpc"); err != nil {
		t.Fatalf("execute turn: %v", err)
	}

	// job.list
	reqList := &JSONRPCRequest{JSONRPC: "2.0", Method: MethodJobList}
	resp := h.HandleRequest(context.Background(), reqList)
	if resp.Error != nil {
		t.Fatalf("job.list error: %v", resp.Error)
	}
	var list JobListResult
	if err := json.Unmarshal(resp.Result, &list); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if len(list.Jobs) != 1 {
		t.Fatalf("want 1 job via RPC, got %d", len(list.Jobs))
	}
	jobID := list.Jobs[0].ID

	// job.get
	paramsGet, _ := json.Marshal(JobGetParams{JobID: jobID})
	reqGet := &JSONRPCRequest{JSONRPC: "2.0", Method: MethodJobGet, Params: paramsGet}
	resp = h.HandleRequest(context.Background(), reqGet)
	if resp.Error != nil {
		t.Fatalf("job.get error: %v", resp.Error)
	}
	var got JobResult
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("unmarshal get: %v", err)
	}
	if got.ID != jobID {
		t.Errorf("get ID %q want %q", got.ID, jobID)
	}

	// job.cancel on already-done job should return non-running but not error
	paramsCancel, _ := json.Marshal(JobCancelParams{JobID: jobID})
	reqCancel := &JSONRPCRequest{JSONRPC: "2.0", Method: MethodJobCancel, Params: paramsCancel}
	resp = h.HandleRequest(context.Background(), reqCancel)
	if resp.Error != nil {
		t.Fatalf("job.cancel error: %v", resp.Error)
	}
	var cancelRes JobCancelResult
	if err := json.Unmarshal(resp.Result, &cancelRes); err != nil {
		t.Fatalf("unmarshal cancel: %v", err)
	}
	if cancelRes.JobID != jobID {
		t.Errorf("cancel JobID %q want %q", cancelRes.JobID, jobID)
	}

	// job.get on missing should be 404
	paramsGet2, _ := json.Marshal(JobGetParams{JobID: "missing:99"})
	reqGet2 := &JSONRPCRequest{JSONRPC: "2.0", Method: MethodJobGet, Params: paramsGet2}
	resp = h.HandleRequest(context.Background(), reqGet2)
	if resp.Error == nil || resp.Error.Code != ErrCodeJobNotFound {
		t.Fatalf("want job not found, got %+v", resp.Error)
	}
}
