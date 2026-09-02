package pluginwasm

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/eduardosanmartin/forge/internal/llm"
)

// providerBridge implements llm.Provider over a provider-plugin (ABI v2, host-driven PULL).
//
// Design justification (wazero reentrancy):
//   wazero forbids a host import invoked BY the guest from calling back into the same
//   module's exports on the same call stack. Therefore the streaming bridge MUST be
//   host-driven PULL: the host pulls chunks by invoking the plugin-EXPORTED function
//   `forge_llm_next_chunk(req_id)` from the host's own goroutine (outside any host-import
//   frame). The alternative — plugin-driven PUSH via a host import `llm_push_chunk` — would
//   invert control and force the plugin to drive while the host is inside a host-function
//   frame, violating the reentrancy constraint and requiring shared mutable state.
//   Host-pull gives the host control over backpressure, timeouts, and ctx cancellation.
//
//   Cancellation: ctx cancellation stops pulling and calls `forge_llm_cancel(req_id)`
//   (fire-and-forget with timeout). Per-call timeout PluginLLMCallTimeout bounds hung
//   exports so the daemon never hangs forever (WU2 guard).
//
//   Memory: guest exchange reuses v1 conventions (forge_alloc + packed ptr/len JSON).
//   No second memory convention is invented.
type providerBridge struct {
	wp   *wasmPlugin
	name string // plugin manifest name, also used as model name
}

var _ llm.Provider = (*providerBridge)(nil)

// newProviderBridge creates a Provider adapter for wp (must be kind=provider).
func newProviderBridge(wp *wasmPlugin) *providerBridge {
	return &providerBridge{wp: wp, name: wp.manifest.Name}
}

// Chat implements llm.Provider.Chat by draining the stream (no dedicated export).
// This keeps the plugin surface minimal (3 exports suffice) and guarantees parity
// with the streaming path — documented in abi.go.
func (b *providerBridge) Chat(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	ch, err := b.ChatStream(ctx, req)
	if err != nil {
		return llm.ChatResponse{}, err
	}
	var (
		content   string
		toolCalls []llm.ToolCall
		usage     *llm.Usage
		finish    string
	)
	for {
		select {
		case <-ctx.Done():
			return llm.ChatResponse{}, ctx.Err()
		case chunk, ok := <-ch:
			if !ok {
				if finish == "" {
					if len(toolCalls) > 0 {
						finish = "tool_calls"
					} else {
						finish = "stop"
					}
				}
				id := "provider-" + b.name
				model := req.Model
				if model == "" {
					model = b.name
				}
				return llm.ChatResponse{
					ID:    id,
					Model: model,
					Choices: []llm.Choice{{
						Index:        0,
						Message:      llm.Message{Role: "assistant", Content: content, ToolCalls: toolCalls},
						FinishReason: finish,
					}},
					Usage: usage,
				}, nil
			}
			if chunk.Error != "" {
				return llm.ChatResponse{}, fmt.Errorf("stream error: %s", chunk.Error)
			}
			if chunk.Usage != nil {
				usage = chunk.Usage
			}
			for _, choice := range chunk.Choices {
				if choice.Delta.Content != "" {
					content += choice.Delta.Content
				}
				if len(choice.Delta.ToolCalls) > 0 {
					toolCalls = append(toolCalls, choice.Delta.ToolCalls...)
				}
				if choice.FinishReason != nil && *choice.FinishReason != "" {
					finish = *choice.FinishReason
				}
			}
		}
	}
}

// ChatStream implements llm.Provider.ChatStream with host-driven PULL.
// Host marshals the ChatRequest to JSON, calls llmStart to get a req_id, then
// polls llmNextChunk per chunk in a dedicated goroutine that respects req ctx.
// Errors surface as StreamChunk{Error} per WU3 consumeStream contract.
// ctx cancellation triggers llm_cancel.
func (b *providerBridge) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.StreamChunk, error) {
	// Marshal the request (subset of llm.ChatRequest relevant to providers).
	// The wire shape is documented in abi.go: JSON ChatRequest.
	reqJSON, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal chat request: %w", err)
	}
	reqID, err := b.wp.llmStart(ctx, reqJSON)
	if err != nil {
		// Surface start failure as a single error chunk to honor streaming contract?
		// Instead return error directly so caller can fail the turn; agent's callLLMStream
		// treats any ChatStream error as turn failure (consistent with WU3).
		return nil, err
	}

	ch := make(chan llm.StreamChunk, 8)

	go func() {
		defer close(ch)
		// Ensure cancellation on exit (best-effort).
		defer func() {
			// Use background ctx for cancel to avoid being blocked by cancelled req ctx.
			b.wp.llmCancel(context.Background(), reqID)
		}()

		for {
			select {
			case <-ctx.Done():
				// Context cancelled: emit error chunk to match WU3's StreamChunk{Error} path,
				// but also respect host-driven cancel. The agent's consumeStream treats ctx.Done()
				// as terminal separately; emitting here is defensive.
				select {
				case ch <- llm.StreamChunk{Error: ctx.Err().Error()}:
				case <-ctx.Done():
				default:
				}
				return
			default:
			}

			// Use the original request ctx so per-call timeout + ctx cancellation are honored.
			data, done, pullErr := b.wp.llmNextChunk(ctx, reqID)
			if pullErr != nil {
				// Plugin signaled error (including timeout) — surface as StreamChunk{Error}.
				select {
				case ch <- llm.StreamChunk{Error: pullErr.Error()}:
				case <-ctx.Done():
				}
				return
			}
			if done {
				return
			}
			if len(data) == 0 {
				// Defensive: empty chunk is not valid StreamChunk; treat as done.
				return
			}
			var sc llm.StreamChunk
			if err := json.Unmarshal(data, &sc); err != nil {
				select {
				case ch <- llm.StreamChunk{Error: fmt.Sprintf("plugin chunk JSON decode: %v (data=%q)", err, string(data))}:
				case <-ctx.Done():
				}
				return
			}
			select {
			case ch <- sc:
			case <-ctx.Done():
				return
			}
			// If chunk carries finish_reason, the next pull is expected to return done.
			// We do not optimistically close here; plugin will signal done on next pull.
		}
	}()

	return ch, nil
}

// ListModels returns the single model name exposed by this provider plugin.
// The model is named after the plugin (manifest name), so daemon config can select it
// by name. Future extensions may allow the plugin to advertise multiple models via
// a dedicated export, but the current contract is one provider == one model name.
func (b *providerBridge) ListModels() ([]string, error) {
	return []string{b.name}, nil
}

// Close is a no-op for the bridge; the underlying wasm module lifetime is managed by Manager.Close().
func (b *providerBridge) Close() error { return nil }
