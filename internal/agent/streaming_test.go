package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/llm"
	"github.com/eduardosanmartin/forge/internal/tools"
)

// streamingMockProvider implements both Chat and ChatStream delivering same final response via different paths.
type streamingMockProvider struct {
	content   string
	toolCalls []llm.ToolCall
	usage     *llm.Usage
	finish    string
	fragmented bool
	streamError string
	notSupported bool
}

func (m *streamingMockProvider) Chat(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	time.Sleep(time.Millisecond)
	return llm.ChatResponse{
		ID:    "chat-id",
		Model: req.Model,
		Choices: []llm.Choice{{
			Message: llm.Message{
				Role:      "assistant",
				Content:   m.content,
				ToolCalls: m.toolCalls,
			},
			FinishReason: m.finish,
		}},
		Usage: m.usage,
	}, nil
}

func (m *streamingMockProvider) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.StreamChunk, error) {
	if m.notSupported {
		return nil, errors.Join(llm.ErrStreamingNotSupported, errors.New("not supported"))
	}
	if m.streamError != "" {
		ch := make(chan llm.StreamChunk, 1)
		ch <- llm.StreamChunk{Error: m.streamError}
		close(ch)
		return ch, nil
	}
	ch := make(chan llm.StreamChunk, 16)
	go func() {
		defer close(ch)
		if m.fragmented && m.content != "" {
			for _, chb := range m.content {
				select {
				case <-ctx.Done():
					return
				case ch <- llm.StreamChunk{
					ID:    "stream-id",
					Model: req.Model,
					Choices: []llm.StreamChoice{{
						Delta: llm.Message{Role: "assistant", Content: string(chb)},
					}},
				}:
				}
			}
		} else if m.content != "" {
			select {
			case <-ctx.Done():
				return
			case ch <- llm.StreamChunk{
				ID:    "stream-id",
				Model: req.Model,
				Choices: []llm.StreamChoice{{
					Delta: llm.Message{Role: "assistant", Content: m.content},
				}},
			}:
			}
		}
		if len(m.toolCalls) > 0 {
			select {
			case <-ctx.Done():
				return
			case ch <- llm.StreamChunk{
				ID:    "stream-id",
				Model: req.Model,
				Choices: []llm.StreamChoice{{
					Delta: llm.Message{Role: "assistant", ToolCalls: m.toolCalls},
				}},
			}:
			}
		}
		fr := m.finish
		if fr == "" {
			if len(m.toolCalls) > 0 {
				fr = "tool_calls"
			} else {
				fr = "stop"
			}
		}
		select {
		case <-ctx.Done():
			return
		case ch <- llm.StreamChunk{
			ID:    "stream-id",
			Model: req.Model,
			Choices: []llm.StreamChoice{{
				FinishReason: &fr,
				Delta:        llm.Message{Role: "assistant"},
			}},
			Usage: m.usage,
		}:
		}
	}()
	return ch, nil
}

func (m *streamingMockProvider) ListModels() ([]string, error) { return []string{"test-model"}, nil }
func (m *streamingMockProvider) Close() error                   { return nil }

type streamingMockRegistry struct {
	provider llm.Provider
}

func (r *streamingMockRegistry) GetDefault() (llm.Provider, string) { return r.provider, "test-model" }
func (r *streamingMockRegistry) Chat(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	return r.provider.Chat(ctx, req)
}
func (r *streamingMockRegistry) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.StreamChunk, error) {
	if s, ok := r.provider.(interface {
		ChatStream(context.Context, llm.ChatRequest) (<-chan llm.StreamChunk, error)
	}); ok {
		return s.ChatStream(ctx, req)
	}
	return nil, errors.New("not supported")
}
func (r *streamingMockRegistry) Close() error { return r.provider.Close() }

func TestAgent_StreamingParity_NoTool(t *testing.T) {
	ctx := context.Background()
	cfgStreaming := config.Defaults()
	cfgStreaming.LLM.Streaming = true
	cfgNon := config.Defaults()
	cfgNon.LLM.Streaming = false

	usage := &llm.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}
	prov := &streamingMockProvider{content: "Hello streaming world", usage: usage, finish: "stop", fragmented: true}

	store1 := newMockStore()
	store2 := newMockStore()
	engine := newTestPermsEngine(t)
	toolsReg1 := tools.NewDefaultRegistry(engine, "", nil)
	toolsReg2 := tools.NewDefaultRegistry(engine, "", nil)

	agentNon := NewAgent(cfgNon, store1, &streamingMockRegistry{provider: prov}, toolsReg1, engine, newTestLogger())
	resNon, err := agentNon.ExecuteTurn(ctx, "session-1", "hi")
	if err != nil {
		t.Fatalf("non-streaming: %v", err)
	}
	var deltas []string
	agentStream := NewAgent(cfgStreaming, store2, &streamingMockRegistry{provider: prov}, toolsReg2, engine, newTestLogger())
	resStream, err := agentStream.ExecuteTurnWithOptions(ctx, "session-1", "hi", TurnOptions{
		StreamingEnabled: true,
		OnDelta: func(d string) { deltas = append(deltas, d) },
	})
	if err != nil {
		t.Fatalf("streaming: %v", err)
	}
	if len(resNon.Messages) != len(resStream.Messages) {
		t.Fatalf("message count parity: non %d stream %d", len(resNon.Messages), len(resStream.Messages))
	}
	nonFinal := resNon.Messages[len(resNon.Messages)-1].Content
	streamFinal := resStream.Messages[len(resStream.Messages)-1].Content
	if nonFinal != streamFinal {
		t.Fatalf("content parity: non %q stream %q", nonFinal, streamFinal)
	}
	if streamFinal != "Hello streaming world" {
		t.Fatalf("unexpected content %q", streamFinal)
	}
	joined := ""
	for _, d := range deltas {
		joined += d
	}
	if joined != streamFinal {
		t.Fatalf("deltas joined %q != content %q", joined, streamFinal)
	}
}

func TestAgent_StreamingParity_WithToolCall(t *testing.T) {
	ctx := context.Background()
	cfgStreaming := config.Defaults()
	cfgStreaming.LLM.Streaming = true
	cfgNon := config.Defaults()
	cfgNon.LLM.Streaming = false

	toolCalls := []llm.ToolCall{
		{ID: "call_1", Type: "function", Function: llm.ToolCallFunction{Name: "fs_read", Arguments: `{"path":"main.go"}`}},
	}
	callCountNon := 0
	provNon := &streamingMockProviderWithCount{
		onChat: func(req llm.ChatRequest) (llm.ChatResponse, error) {
			callCountNon++
			if callCountNon == 1 {
				return llm.ChatResponse{
					Choices: []llm.Choice{{Message: llm.Message{Role: "assistant", Content: "calling tool", ToolCalls: toolCalls}, FinishReason: "tool_calls"}},
					Usage: &llm.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
				}, nil
			}
			return llm.ChatResponse{
				Choices: []llm.Choice{{Message: llm.Message{Role: "assistant", Content: "done"}, FinishReason: "stop"}},
				Usage: &llm.Usage{PromptTokens: 15, CompletionTokens: 5, TotalTokens: 20},
			}, nil
		},
		onStream: func(ctx context.Context, req llm.ChatRequest) (<-chan llm.StreamChunk, error) {
			return nil, llm.ErrStreamingNotSupported
		},
	}
	callCountStream := 0
	provStream := &streamingMockProviderWithCount{
		onChat: func(req llm.ChatRequest) (llm.ChatResponse, error) {
			return llm.ChatResponse{}, errors.New("unexpected Chat")
		},
		onStream: func(ctx context.Context, req llm.ChatRequest) (<-chan llm.StreamChunk, error) {
			callCountStream++
			ch := make(chan llm.StreamChunk, 8)
			go func() {
				defer close(ch)
				if callCountStream == 1 {
					ch <- llm.StreamChunk{Choices: []llm.StreamChoice{{Delta: llm.Message{Role: "assistant", Content: "calling tool"}}}}
					ch <- llm.StreamChunk{Choices: []llm.StreamChoice{{Delta: llm.Message{Role: "assistant", ToolCalls: toolCalls}}}}
					fr := "tool_calls"
					ch <- llm.StreamChunk{Choices: []llm.StreamChoice{{FinishReason: &fr, Delta: llm.Message{Role: "assistant"}}}, Usage: &llm.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}}
				} else {
					ch <- llm.StreamChunk{Choices: []llm.StreamChoice{{Delta: llm.Message{Role: "assistant", Content: "done"}}}}
					fr := "stop"
					ch <- llm.StreamChunk{Choices: []llm.StreamChoice{{FinishReason: &fr, Delta: llm.Message{Role: "assistant"}}}, Usage: &llm.Usage{PromptTokens: 15, CompletionTokens: 5, TotalTokens: 20}}
				}
			}()
			return ch, nil
		},
	}

	store1 := newMockStore()
	store2 := newMockStore()
	engine := newTestPermsEngine(t)
	toolsReg1 := tools.NewDefaultRegistry(engine, "", nil)
	toolsReg2 := tools.NewDefaultRegistry(engine, "", nil)

	agentNon := NewAgent(cfgNon, store1, &streamingMockRegistry{provider: provNon}, toolsReg1, engine, newTestLogger())
	resNon, err := agentNon.ExecuteTurn(ctx, "session-1", "hi")
	if err != nil {
		t.Fatalf("non: %v", err)
	}
	agentStream := NewAgent(cfgStreaming, store2, &streamingMockRegistry{provider: provStream}, toolsReg2, engine, newTestLogger())
	resStream, err := agentStream.ExecuteTurn(ctx, "session-1", "hi")
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if len(resNon.Messages) != len(resStream.Messages) {
		t.Fatalf("messages len non %d stream %d: %+v vs %+v", len(resNon.Messages), len(resStream.Messages), resNon.Messages, resStream.Messages)
	}
	if resNon.Messages[len(resNon.Messages)-1].Content != resStream.Messages[len(resStream.Messages)-1].Content {
		t.Fatalf("final content mismatch non %q stream %q", resNon.Messages[len(resNon.Messages)-1].Content, resStream.Messages[len(resStream.Messages)-1].Content)
	}
	if resStream.Messages[len(resStream.Messages)-1].Content != "done" {
		t.Fatalf("expected done, got %q", resStream.Messages[len(resStream.Messages)-1].Content)
	}
}

func TestAgent_StreamingMidStreamErrorFailsTurn(t *testing.T) {
	ctx := context.Background()
	cfg := config.Defaults()
	cfg.LLM.Streaming = true
	prov := &streamingMockProvider{streamError: "mid error"}
	store := newMockStore()
	engine := newTestPermsEngine(t)
	toolsReg := tools.NewDefaultRegistry(engine, "", nil)
	agent := NewAgent(cfg, store, &streamingMockRegistry{provider: prov}, toolsReg, engine, newTestLogger())
	res, err := agent.ExecuteTurn(ctx, "session-1", "hi")
	if err == nil {
		t.Fatalf("expected error")
	}
	if !res.Halted {
		t.Fatalf("expected halted on stream error")
	}
	if !streamContains(res.Error.Error(), "mid error") {
		t.Fatalf("error should contain mid error, got %v", res.Error)
	}
}

func TestAgent_StreamingFallbackOnNotSupported(t *testing.T) {
	ctx := context.Background()
	cfg := config.Defaults()
	cfg.LLM.Streaming = true
	prov := &streamingMockProvider{content: "fallback ok", finish: "stop", notSupported: true}
	store := newMockStore()
	engine := newTestPermsEngine(t)
	toolsReg := tools.NewDefaultRegistry(engine, "", nil)
	agent := NewAgent(cfg, store, &streamingMockRegistry{provider: prov}, toolsReg, engine, newTestLogger())
	res, err := agent.ExecuteTurn(ctx, "session-1", "hi")
	if err != nil {
		t.Fatalf("fallback should succeed, got %v", err)
	}
	if res.Messages[len(res.Messages)-1].Content != "fallback ok" {
		t.Fatalf("fallback content %q", res.Messages[len(res.Messages)-1].Content)
	}
}

type streamingMockProviderWithCount struct {
	onChat   func(llm.ChatRequest) (llm.ChatResponse, error)
	onStream func(context.Context, llm.ChatRequest) (<-chan llm.StreamChunk, error)
}

func (m *streamingMockProviderWithCount) Chat(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	return m.onChat(req)
}
func (m *streamingMockProviderWithCount) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.StreamChunk, error) {
	return m.onStream(ctx, req)
}
func (m *streamingMockProviderWithCount) ListModels() ([]string, error) { return []string{"test-model"}, nil }
func (m *streamingMockProviderWithCount) Close() error                   { return nil }

func streamContains(s, substr string) bool {
	return len(s) >= len(substr) && (func() bool {
		for i := 0; i <= len(s)-len(substr); i++ {
			if s[i:i+len(substr)] == substr {
				return true
			}
		}
		return false
	})()
}

// TestConsumeStream_MergesToolCallFragments is the retest-4 regression test:
// OpenAI-style streaming sends one call across chunks (name first, argument
// slices after). Fragments must merge into a single call — verbatim appends
// executed fs_list with no args plus an unnamed call, so the real call never
// ran and the turn died empty.
func TestConsumeStream_MergesToolCallFragments(t *testing.T) {
	ctx := context.Background()
	head := llm.ToolCall{ID: "call_1", Type: "function", Function: llm.ToolCallFunction{Name: "fs_list", Arguments: ""}}
	tail := llm.ToolCall{Function: llm.ToolCallFunction{Arguments: `{"path":"."}`}}
	fr := "tool_calls"
	ch := make(chan llm.StreamChunk, 8)
	go func() {
		defer close(ch)
		ch <- llm.StreamChunk{Choices: []llm.StreamChoice{{Delta: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{head}}}}}
		ch <- llm.StreamChunk{Choices: []llm.StreamChoice{{Delta: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{tail}}}}}
		ch <- llm.StreamChunk{Choices: []llm.StreamChoice{{FinishReason: &fr, Delta: llm.Message{Role: "assistant"}}}}
	}()
	_, calls, _, finish, err := consumeStream(ctx, ch, nil)
	if err != nil {
		t.Fatalf("consumeStream: %v", err)
	}
	if finish != "tool_calls" {
		t.Fatalf("finish = %q, want tool_calls", finish)
	}
	if len(calls) != 1 {
		t.Fatalf("fragments should merge into 1 call, got %d: %+v", len(calls), calls)
	}
	if calls[0].Function.Name != "fs_list" {
		t.Fatalf("merged name = %q, want fs_list", calls[0].Function.Name)
	}
	if calls[0].Function.Arguments != `{"path":"."}` {
		t.Fatalf("merged args = %q, want path call", calls[0].Function.Arguments)
	}
	if calls[0].ID != "call_1" {
		t.Fatalf("merged id = %q, want call_1", calls[0].ID)
	}
}

// TestConsumeStream_KeepsDistinctCallsSeparate guards the merge: two named
// calls (parallel) and pre-assembled provider calls must never fuse.
func TestConsumeStream_KeepsDistinctCallsSeparate(t *testing.T) {
	ctx := context.Background()
	ch := make(chan llm.StreamChunk, 8)
	go func() {
		defer close(ch)
		ch <- llm.StreamChunk{Choices: []llm.StreamChoice{{Delta: llm.Message{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{
				{ID: "call_1", Type: "function", Function: llm.ToolCallFunction{Name: "fs_read", Arguments: `{"path":"a"}`}},
				{ID: "call_2", Type: "function", Function: llm.ToolCallFunction{Name: "fs_read", Arguments: `{"path":"b"}`}},
			},
		}}}}
		fr := "tool_calls"
		ch <- llm.StreamChunk{Choices: []llm.StreamChoice{{FinishReason: &fr, Delta: llm.Message{Role: "assistant"}}}}
	}()
	_, calls, _, _, err := consumeStream(ctx, ch, nil)
	if err != nil {
		t.Fatalf("consumeStream: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("distinct calls must stay separate, got %d: %+v", len(calls), calls)
	}
}
