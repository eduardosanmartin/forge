# ABI v2 — Provider Plugins with Streaming (forge)

Status: frozen (WU2 binding contract). Any signature, packing, export name, error envelope, or
permission change REQUIRES bumping `ABIVersionV2` and a manifest schema review. Wizard adaptation
is OUT OF SCOPE — ABI freezes first (v2 lesson).

## Summary

- `kind = "provider"` manifests (alongside existing `kind = "tool"`) expose a deterministic LLM
  streaming capability via wazero.
- Host-driven PULL streaming: the host pulls chunks by calling a plugin-EXPORTED function
  `forge_llm_next_chunk(req_id)` per chunk from its own goroutine (not nested inside a host import),
  because wazero forbids reentrancy (host import -> same module export on same stack). The plugin is
  passive; cancellation is via `forge_llm_cancel(req_id)`. Poll design was rejected for the same
  reentrancy reason and added latency.
- Same wazero isolation as tool plugins; a pure provider needs no filesystem/network host functions.
  The only new permission is `llm` (capability gate + manifest validation, routed through the perms
  engine vocabulary like every other host capability).

## Manifest

```toml
name = "mock_provider"
version = "0.1.0"
description = "Mock LLM provider for tests"
source = "local"            # or "external" (requires checksum + v2 signed approved.flag)
entrypoint = "mock_provider.wasm"
kind = "provider"           # new; absent defaults to "tool" for ABI v1 compat (hard requirement)
permissions = ["llm"]       # provider MUST declare "llm"; tool plugins MUST NOT use "llm"
# no [[tools]] for provider kind (validation rejects it)
```

- Parser (`internal/plugin/toml.go`) additively allows `kind` as a top-level string; old manifests
  without `kind` keep validating unchanged (default `tool`).
- Validation (`internal/plugin/validate.go`):
  - `kind` ∈ {`tool`,`provider`}
  - `kind=provider` ⇒ `permissions` must contain `llm`, `[[tools]]` must be empty
  - `kind=tool` ⇒ `permissions` must NOT contain `llm`
  - Duplicate / unknown permission handling unchanged.

## ABI versions

| Constant | Value | Meaning |
|----------|-------|---------|
| `plugin.ABIVersion` | 1 | Tool plugins (frozen, WU4) |
| `plugin.ABIVersionV2` | 2 | Provider plugins (LLM streaming) |
| `plugin.SupportedABIVersions` | [1,2] | Host accepts both for backward compat |

Provider plugins MUST export `forge_abi_version() -> i32` returning `2`; tool plugins return `1`.
Mismatched kind/version is `ErrABIMismatch` (e.g., `kind=provider` reporting 1 is rejected).

## Exports / Imports

### Plugin exports (module must provide)

| Name | Signature | Description |
|------|-----------|-------------|
| `forge_abi_version` | `() -> i32\|i64` | ABI version constant (1 or 2). Host reads after instantiation. |
| `forge_alloc` | `(size i32) -> i32 ptr` | Bump-allocate `size` bytes in plugin linear memory; host writes request JSON there and reads response via packed result. |
| `forge_tool_list` | `() -> i64 packed` | Tool plugins only: JSON array of `ToolExport`. Optional when no tools; required when `[[tools]]` present. |
| `forge_tool_invoke` | `(fn_ptr i32, fn_len i32, args_ptr i32, args_len i32) -> i64 packed` | Tool plugins only. |
| `forge_llm_stream_start` | `(req_ptr i32, req_len i32) -> i64 packed` | **Provider only**. Host passes JSON `ChatRequest`. Returns JSON `{"req_id":<u32>}` or `{"error":"...","code":<int>}`. |
| `forge_llm_next_chunk` | `(req_id i32) -> i64 packed` | **Provider only**. Host pulls next `StreamChunk`. Returns JSON `StreamChunk` **or** `{"done":true}` when finished **or** `{"error":"...","code":<int>}` on terminal failure. |
| `forge_llm_cancel` | `(req_id i32) -> i32 errno` | **Provider only**. `0` success. Host may call on `ctx` cancellation or timeout (bounded `PluginLLMCallTimeout = 5s`). |

Memory convention (same as v1, no second convention):
- Packed `i64` = `(uint64(ptr) << 32) | uint64(len)` (see `internal/pluginwasm/abi.go` `pack`/`unpack`).
- Host uses `forge_alloc` to write request bytes; plugin returns JSON via its own `forge_alloc` and packed `ptr:len`. Host copies out via `mod.Memory().Read`. No `forge_free` needed for the dogfood (bump allocator, 4× chunks).

### Host imports (module `forge_host`)

Unchanged for provider: a pure provider needs none. Existing imports (`log`, `fs_read`, `fs_write`,
`shell_exec`, `git_run`, `net_fetch`) remain guarded by manifest permissions and perms engine
before touching the OS. Provider plugins do not gain filesystem/network imports unless explicitly
requested+approved.

## Streaming wire

- `ChatRequest` JSON: `internal/llm.ChatRequest` wire shape (`model`, `messages`, `tools`, `tool_choice`,
  `temperature`, `max_tokens`, `stream`). The mock provider accepts any shape; it inspects the raw JSON
  for the marker substring `mock_tool` to decide on tool-call variant.
- `StreamChunk` JSON: `internal/llm.StreamChunk` wire shape (same as WU3 `consumeStream` contract):
  `{"id","model","choices":[{"index":0,"delta":{"role":"assistant","content":"...","tool_calls":[...]},"finish_reason":"stop"|"tool_calls"|null}],"usage":null,"error":""}`.
- Errors: plugin returns `{"error":"message","code":<LLMErrorCode>}` inside `forge_llm_stream_start` /
  `forge_llm_next_chunk`; bridge surfaces as `StreamChunk{Error:"message"}` / `error` per
  `internal/agent/loop.go:consumeStream`. Mid-stream `Error != ""` is terminal and fails the turn.

## Error codes (frozen)

| Name | Value | Meaning |
|------|-------|---------|
| `LLMErrorCodeOK` | 0 | Success |
| `LLMErrorCodeInvalidRequest` | 1 | Invalid `ChatRequest` JSON / unknown `req_id` |
| `LLMErrorCodeInternal` | 2 | Internal plugin error |
| `LLMErrorCodeCancelled` | 3 | Cancelled via `forge_llm_cancel` |
| `LLMErrorCodeTimeout` | 4 | Per-call timeout (`PluginLLMCallTimeout`) |

Contract tests pin uniqueness and exact values (`internal/plugin/abi_test.go:TestLLMErrorCodes`).

## Bridge (host-driven PULL)

`internal/pluginwasm/provider_bridge.go:providerBridge` implements `llm.Provider`:

- `Chat(ctx, req)` = drain-all of `ChatStream` (no dedicated `forge_llm_chat` export needed; keeps surface minimal and guarantees streaming parity).
- `ChatStream(ctx, req)` marshals `ChatRequest` → `llmStart` → spawns goroutine that `llmNextChunk` per chunk with `req` ctx honored. `ctx` cancel → stop pulling + `llm_cancel` (fire-and-forget with `PluginLLMCallTimeout`). Hung export is bounded by `PluginLLMCallTimeout` per call; timeout surfaces as `StreamChunk{Error}`.
- `ListModels() = [manifest.Name]` (one provider == one model name, selected via daemon config / `stubRegistry` in tests).
- `Close() = no-op` (module lifetime via `Manager.Close`).

Timeout guard: each `forge_llm_*` call uses `context.WithTimeout(ctx, PluginLLMCallTimeout)` so a hung plugin cannot hang the daemon forever.

## Contract tests (RNF-3.3)

- `internal/plugin/abi_test.go:TestWASMExportNames` — uniqueness of all export names across ABI v1+v2.
- `internal/plugin/abi_test.go:TestLLMErrorCodes` — uniqueness + frozen values of error codes.
- `internal/plugin/abi_test.go:TestManifestKindValidation` — `kind=provider` without `llm` rejected, `llm` on `kind=tool` rejected, etc.
- `internal/pluginwasm/provider_test.go` — missing LLM exports rejected, external approval with `WriteV2`, context cancel.
- `internal/e2e/provider_stream_test.go` — committed `mock_provider.wasm` + v2 signed `approved.flag` via isolated `FORGE_KEYS_DIR` (no cargo at runtime), agent `ExecuteTurnWithOptions` streaming → parity with `consumeStream`, `OnDelta` reconstructs final content.

## Dogfood

- Rust crate `internal/pluginwasm/testdata/mock-provider` (`cdylib`, `wasm32-unknown-unknown`):
  deterministic fake LLM streaming `Hello from mock provider!` in 4 chunks (`Hello `,`from `,`mock `,`provider!`) then `stop`; tool variant behind `mock_tool` marker streams 2 text chunks then `mock_provider_echo` tool_call with `tool_calls` finish.
- Committed artifact `internal/pluginwasm/testdata/mock-provider/mock_provider.wasm` (≈37 kB) + `manifest.toml` (`kind=provider`, `permissions=["llm"]`).
- Tests run WITHOUT `cargo`; `cargo build` only for artifact regeneration (target `wasm32-unknown-unknown` installed, invoke via full path `C:\Users\eduar\.cargo\bin\cargo.exe` on Windows).

## Known limitations

- Provider is single-model per plugin (manifest name). Multi-model advertisement deferred.
- Bump allocator: no `forge_free`; sufficient for 3–5 chunk dogfood but not for large streams.
- No filesystem/network imports for pure provider; if requested they still route through same perms engine.

## Host-side concurrency & timing guarantees

- All plugin export invocations (tool AND llm paths) are mutex-serialized per module instance: wazero Function.Call is not goroutine-safe, and the guest bump allocator assumes single-threaded access. Concurrent sessions sharing one provider plugin are serialized, not parallelized.
- Every host->plugin call carries a per-call timeout (PluginLLMCallTimeout = 5s). A hung export can delay a stream step but never hangs the daemon.
