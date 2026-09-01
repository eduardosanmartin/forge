// Package pluginwasm implements the WASM host runtime for forge plugins over wazero.
// It keeps internal/plugin runtime-free; this package owns the wazero dependency.
//
// # Memory ABI (JSON-over-linear-memory)
//
// All cross-boundary strings and JSON payloads are passed as pointer+length
// pairs referencing the plugin's linear memory (exported as "memory").
//
// Packing: a 64-bit value where the high 32 bits are the pointer (i32 offset)
// and the low 32 bits are the length (i32). In Go:
//
//	packed := (uint64(ptr) << 32) | uint64(length)
//	ptr    := uint32(packed >> 32)
//	length := uint32(packed & 0xffffffff)
//
// Plugin exports (module must provide):
//
//   - forge_abi_version() -> i32 or i64 : ABI version constant (must equal plugin.ABIVersion).
//   - forge_tool_list() -> i64 (packed ptr:len) : JSON array of ToolExport objects
//       e.g. [{"name":"my_plugin_greet","description":"Greets","permission":"fs.read"}]
//   - forge_tool_invoke(fn_ptr i32, fn_len i32, args_ptr i32, args_len i32) -> i64 (packed)
//       : JSON result of the tool. Success payload is any JSON whose top-level
//         is not {"error": "..."}; error envelope is exactly {"error":"message"}.
//   - forge_alloc(size i32) -> i32 (ptr) : bump-allocate `size` bytes in plugin memory
//       and return the base pointer. The host uses this to write argument buffers
//       before calling forge_tool_invoke, and to allocate response buffers for
//       host-import returns.
//
// Host imports (module "forge_host"):
//
//   - log(level_ptr i32, level_len i32, msg_ptr i32, msg_len i32)
//   - fs_read(path_ptr i32, path_len i32) -> i64 (packed JSON)
//       Success JSON is the raw file bytes JSON-quoted or base64 envelope; for WU2 the
//       greeter test uses plain UTF-8 bytes returned as JSON string via the same packed convention.
//       Errors are returned as {"error":"..."} (capability or perms denied, or OS error).
//   - fs_write(path_ptr i32, path_len i32, data_ptr i32, data_len i32) -> i32 (errno: 0 success, 1 error)
//       Errno-only by design: the host logs the denial/error detail (path, perms rule)
//       via the plugin-scoped logger, so plugin authors read failure causes from logs.
//       Documented WU2 limitation: unlike the i64 imports, fs_write does not return a
//       JSON error envelope; unifying to packed JSON is a registered WU4 prerequisite
//       (must be finalized before the wizard generates plugins against this ABI).
//   - shell_exec(cmd_ptr i32, cmd_len i32, args_json_ptr i32, args_json_len i32) -> i64 (packed JSON)
//   - git_run(args_json_ptr i32, args_json_len i32) -> i64 (packed JSON)
//   - net_fetch(url_ptr i32, url_len i32) -> i64 (packed JSON)
//
// Error convention: every host import that returns i64 uses the same packed JSON envelope:
// success JSON is the operation's result bytes (often JSON-encoded), failure JSON is
// {"error":"..."} with len>0. A packed value of 0 (ptr=0,len=0) is treated as empty
// success only when the operation legitimately has no data; by default len==0 is not
// used to signal error — the JSON envelope is authoritative.
//
// Capability enforcement order for every host import (RNF-4.2):
//  1. Check that the calling plugin's manifest declares the required capability
//     (fs.read, fs.write, shell.exec, git, net). If not declared, deny without touching OS.
//  2. For fs/shell/git, call the perms.Engine Check with the appropriate perms.Request.
//     Net checks the manager's NetAllowlist. Denied requests are returned as JSON error.
//  3. Only then perform the host OS / network operation.
//
// Documented WU2 limitation: enforcement is capability-KIND level (manifest permission
// granted or not). Path and command glob refinement for plugin calls is deferred; the
// WASM sandbox itself bounds the plugin (spec RF-5.2, RNF-4.2). Path-level globs are
// still enforced by the perms engine when configured, but per-tool path allowlists
// inside the manifest are not yet supported.
package pluginwasm

import (
	"github.com/eduardosanmartin/forge/internal/plugin"
)

// Re-export WU1 ABI names for convenience; the canonical constants live in internal/plugin.
const (
	// ExportABIVersion is the plugin export that reports its ABI version.
	ExportABIVersion = plugin.ExportABIVersion
	// ExportToolList is the plugin export that returns the JSON tool list.
	ExportToolList = plugin.ExportToolList
	// ExportToolInvoke is the plugin export that invokes a tool.
	ExportToolInvoke = plugin.ExportToolInvoke
	// ExportAlloc is the plugin export that allocates memory in plugin linear memory.
	ExportAlloc = plugin.ExportAlloc
)

// HostModule is the wazero host module name that plugins import.
const HostModule = "forge_host"

// pack builds a packed i64 from ptr/len.
func pack(ptr uint32, length uint32) uint64 {
	return (uint64(ptr) << 32) | uint64(length)
}

// unpack splits a packed i64 into ptr/len.
func unpack(packed uint64) (uint32, uint32) {
	return uint32(packed >> 32), uint32(packed & 0xffffffff)
}
