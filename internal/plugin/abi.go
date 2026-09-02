// Package plugin implements the forge plugin ABI and manifest schema.
//
// Manifest TOML shape (manifest.toml per plugin):
//
//	name = "my_plugin"
//	version = "0.1.0"
//	description = "Example plugin."
//	source = "local"
//	entrypoint = "plugin.wasm"
//	permissions = ["fs.read", "git"]
//	dependencies = []
//	checksum = "sha256:..."   # only for external source
//
//	[[tools]]
//	name = "my_plugin_greet"
//	description = "Greets a user."
//	permission = "fs.read"
//
// See manifest.go for the full parsing and validation contract.
package plugin

// ABIVersion is the current plugin ABI version. WU2's WASM runtime checks
// this value via the forge_abi_version export to reject mismatched plugins.
//
// FROZEN (WU4): ABIVersion=1 is now frozen.
//
//   - Host import `fs_write` returns i32 errno-only by deliberate design.
//     Per-call failure detail is emitted through host logs (plugin-scoped logger),
//     not through a JSON error envelope. This keeps the hot write path errno-only
//     and avoids an extra allocation/JSON parse per write.
//   - Any convention change to this ABI (signatures, packing, export names,
//     error envelopes, or fs_write behavior) REQUIRES bumping ABIVersion and a
//     manifest schema review. Plugins built against ABIVersion=1 must continue to
//     validate against the WU1 manifest schema exactly.
const ABIVersion = 1

// ABIVersionV2 is the ABI version for provider plugins (LLM streaming).
// Host accepts BOTH ABIVersion (1, tool plugins, frozen) and ABIVersionV2 (2, provider plugins)
// for backward compatibility. Provider plugins MUST report 2; tool plugins MUST report 1.
const ABIVersionV2 = 2

// SupportedABIVersions is the set of ABI versions the host accepts.
// Used by pluginwasm to allow both v1 (tool) and v2 (provider) plugins.
var SupportedABIVersions = []int{ABIVersion, ABIVersionV2}

// PluginPermissionKinds is the allowed permission vocabulary for plugins.
// Each entry ties to its enforcement point in WU2's wazero host imports:
//
//   - "fs.read"    -> Host.FSRead
//   - "fs.write"   -> Host.FSWrite
//   - "shell.exec" -> Host.ShellExec
//   - "git"        -> Host.GitRun
//   - "net"        -> Host.NetFetch
//   - "llm"        -> provider-plugin marker (ABI v2, no host import; capability gate + manifest validation)
//
// The WU2 runtime enforces these through the perms engine before reaching
// the host OS (RNF-4.2, spec sandbox section). Provider plugins require "llm"
// and must be kind=provider; tool plugins must not declare "llm".
var PluginPermissionKinds = []string{
	"fs.read",
	"fs.write",
	"shell.exec",
	"git",
	"net",
	"llm",
}

// LLMErrorCodes enumerate provider error categories returned in JSON error envelopes
// via llm_next_chunk / llm_stream_start: {"error":"...","code":<int>}. Codes are
// stable ABI v2 contract — wizard generates against them and contract tests pin them.
const (
	LLMErrorCodeOK             = 0
	LLMErrorCodeInvalidRequest = 1
	LLMErrorCodeInternal       = 2
	LLMErrorCodeCancelled      = 3
	LLMErrorCodeTimeout        = 4
)

// AllLLMErrorCodes lists every LLM error code for uniqueness tests (RNF-3.3 pattern).
var AllLLMErrorCodes = []int{
	LLMErrorCodeOK,
	LLMErrorCodeInvalidRequest,
	LLMErrorCodeInternal,
	LLMErrorCodeCancelled,
	LLMErrorCodeTimeout,
}

// Host defines the host functions exposed to WASM plugins via wazero imports.
//
// WU2 implements this interface over wazero host imports; every method MUST
// route through the perms engine before touching the host OS, and the plugin
// can only reach capabilities declared in its manifest and approved by the
// user (RNF-4.2, spec sandbox section).
type Host interface {
	// Log emits a plugin log line through forge's slog logger.
	Log(level string, message string)

	// FSRead proxies a filesystem read through the permission engine.
	// Returns an error when the declared permissions do not grant fs.read for the path.
	FSRead(path string) ([]byte, error)

	// FSWrite proxies a filesystem write through the permission engine.
	FSWrite(path string, data []byte) error

	// ShellExec proxies a shell command through the permission engine.
	ShellExec(command string, args []string) (stdout string, stderr string, err error)

	// GitRun proxies a git invocation through the permission engine.
	GitRun(args []string) (stdout string, err error)

	// NetFetch proxies an HTTP GET through the network allowlist.
	NetFetch(url string) ([]byte, error)
}

// WASM export names the plugin module must provide (WU2 runtime contract).
const (
	// ExportABIVersion is the exported global/function exposing the plugin's ABI version.
	// WU2 reads this to enforce ABIVersion compatibility.
	ExportABIVersion = "forge_abi_version"

	// ExportToolList is the exported function returning the plugin's tool list.
	// WU2 calls this to discover tools declared in the manifest.
	ExportToolList = "forge_tool_list"

	// ExportToolInvoke is the exported function invoked to execute a tool.
	// WU2 dispatches tool calls through this entry point.
	ExportToolInvoke = "forge_tool_invoke"

	// ExportAlloc is the exported function the host calls to allocate buffers
	// inside plugin linear memory. Signature: forge_alloc(size i32) i32 (ptr).
	// The plugin implements a bump or heap allocator; the host writes arguments
	// into the returned region and passes its ptr/len to forge_tool_invoke.
	// Added in WU2 as an additive extension of the ABI (RNF-3.2).
	ExportAlloc = "forge_alloc"

	// --- ABI v2: provider (LLM streaming) exports (host-driven PULL) ---
	//
	// Streaming design note (wazero reentrancy constraint):
	//   Two designs were considered:
	//     (A) Host-driven PULL: host calls plugin export llm_next_chunk(req_id) per chunk from its
	//         own goroutine (not nested inside a host-function call). Each export call returns a
	//         packed JSON StreamChunk or error envelope. Cancellation via llm_cancel(req_id).
	//     (B) Plugin-driven PUSH via host import llm_push_chunk: plugin drives and calls a host
	//         import per chunk. This would invert control but suffers from wazero's NO reentrancy:
	//         a host import invoked BY the plugin cannot call back into the same module's exports
	//         on the same stack, and would require complex state sharing or polling.
	//   Chosen: (A) Host-driven PULL — host owns flow control, timeouts, and ctx cancellation;
	//   plugin is passive and only advances on host calls. Justification is documented in
	//   internal/pluginwasm/provider_bridge.go and mirrors the v1 memory-passing conventions
	//   (alloc + packed ptr/len) so no second memory convention is invented.
	//   Poll design alternative was rejected for the same reentrancy reason and added latency.
	//
	// Exports (all for kind=provider, ABIVersion=2):
	//   forge_llm_stream_start(req_ptr i32, req_len i32) -> i64 packed
	//     Host passes JSON ChatRequest (see internal/llm.ChatRequest wire JSON). Plugin returns
	//     JSON {"req_id":<u32>} on success or {"error":"...","code":<int>} on failure (packed).
	//   forge_llm_next_chunk(req_id i32) -> i64 packed
	//     Host pulls next StreamChunk. Plugin returns JSON StreamChunk (internal/llm.StreamChunk wire)
	//     or {"done":true} when finished, or {"error":"...","code":<int>} on terminal failure.
	//     Empty packed (0) is NOT used to signal done — JSON is authoritative.
	//   forge_llm_cancel(req_id i32) -> i32 errno (0 success, non-zero failure)
	//     Host may call on ctx cancellation or timeout. Plugin frees resources for req_id.
	//
	// Memory: same v1 conventions — plugin must export forge_alloc; host writes request JSON via
	// forge_alloc; plugin returns JSON via its own alloc and packed ptr:len. Host copies out and
	// does NOT free (bump allocator sufficient for 3-5 chunk dogfood).
	// Timeout guard: each host->plugin call is bounded by PluginLLMCallTimeout (5s); hung export
	// surfaces as StreamChunk{Error} / Chat error per WU3 consumeStream contract and triggers cancel.
	// Provider plugins MUST NOT grant filesystem/network host imports unless explicitly declared+approved
	// (a pure provider needs none); sandbox is SAME wazero isolation as tool plugins.

	// ExportLLMStreamStart is the provider export to start a streaming LLM request.
	ExportLLMStreamStart = "forge_llm_stream_start"

	// ExportLLMNextChunk is the provider export the host pulls per chunk.
	ExportLLMNextChunk = "forge_llm_next_chunk"

	// ExportLLMCancel is the provider export to cancel an in-flight request.
	ExportLLMCancel = "forge_llm_cancel"
)
