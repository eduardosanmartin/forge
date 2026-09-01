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
const ABIVersion = 1

// PluginPermissionKinds is the allowed permission vocabulary for plugins.
// Each entry ties to its enforcement point in WU2's wazero host imports:
//
//   - "fs.read"    -> Host.FSRead
//   - "fs.write"   -> Host.FSWrite
//   - "shell.exec" -> Host.ShellExec
//   - "git"        -> Host.GitRun
//   - "net"        -> Host.NetFetch
//
// The WU2 runtime enforces these through the perms engine before reaching
// the host OS (RNF-4.2, spec sandbox section).
var PluginPermissionKinds = []string{
	"fs.read",
	"fs.write",
	"shell.exec",
	"git",
	"net",
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
)
