package pluginwasm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"strings"

	"github.com/tetratelabs/wazero/api"

	"github.com/eduardosanmartin/forge/internal/perms"
	"github.com/eduardosanmartin/forge/internal/plugin"
)

// hostEnv holds per-plugin host state that enforces capabilities before touching the OS.
type hostEnv struct {
	manifest     plugin.Manifest
	permSet      map[string]bool
	permsEngine  *perms.Engine
	netAllowlist []string
	logger       *slog.Logger
	pluginName   string
}

// newHostEnv builds a hostEnv from the plugin manifest and manager options.
func newHostEnv(m plugin.Manifest, permsEngine *perms.Engine, netAllowlist []string, logger *slog.Logger) *hostEnv {
	set := make(map[string]bool, len(m.Permissions))
	for _, p := range m.Permissions {
		set[p] = true
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &hostEnv{
		manifest:     m,
		permSet:      set,
		permsEngine:  permsEngine,
		netAllowlist: netAllowlist,
		logger:       logger.With(slog.String("plugin", m.Name)),
		pluginName:   m.Name,
	}
}

// hasPerm reports whether the manifest declares the given permission kind.
func (h *hostEnv) hasPerm(kind string) bool {
	return h.permSet[kind]
}

// helpers to read/write plugin memory.

// readString reads a string from the calling module's memory.
func readString(mod api.Module, ptr uint32, length uint32) (string, bool) {
	if length == 0 {
		return "", true
	}
	buf, ok := mod.Memory().Read(ptr, length)
	if !ok {
		return "", false
	}
	// Copy to avoid aliasing after memory growth.
	out := make([]byte, len(buf))
	copy(out, buf)
	return string(out), true
}

// readBytes reads bytes from the calling module's memory.
func readBytes(mod api.Module, ptr uint32, length uint32) ([]byte, bool) {
	if length == 0 {
		return []byte{}, true
	}
	buf, ok := mod.Memory().Read(ptr, length)
	if !ok {
		return nil, false
	}
	out := make([]byte, len(buf))
	copy(out, buf)
	return out, true
}

// writeJSONResponse encodes v as JSON, allocates plugin memory via forge_alloc, writes it, and returns packed ptr:len.
// On any failure it returns 0.
func writeJSONResponse(ctx context.Context, mod api.Module, v any) uint64 {
	data, err := json.Marshal(v)
	if err != nil {
		data = []byte(`{"error":"host marshal failure"}`)
	}
	return writeRawResponse(ctx, mod, data)
}

// writeRawResponse writes raw bytes via forge_alloc and returns packed.
func writeRawResponse(ctx context.Context, mod api.Module, data []byte) uint64 {
	if len(data) == 0 {
		return pack(0, 0)
	}
	allocFn := mod.ExportedFunction(ExportAlloc)
	if allocFn == nil {
		return pack(0, 0)
	}
	res, err := allocFn.Call(ctx, api.EncodeU32(uint32(len(data))))
	if err != nil || len(res) == 0 {
		return pack(0, 0)
	}
	ptr := api.DecodeU32(res[0])
	if ptr == 0 {
		return pack(0, 0)
	}
	if !mod.Memory().Write(ptr, data) {
		return pack(0, 0)
	}
	return pack(ptr, uint32(len(data)))
}

// errorEnvelope returns a JSON error envelope as packed response.
func errorEnvelope(ctx context.Context, mod api.Module, msg string) uint64 {
	return writeJSONResponse(ctx, mod, map[string]string{"error": msg})
}

// logHost implements forge_host.log(level_ptr,len,msg_ptr,len).
func (h *hostEnv) logHost() api.GoModuleFunc {
	return api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
		levelPtr := api.DecodeU32(stack[0])
		levelLen := api.DecodeU32(stack[1])
		msgPtr := api.DecodeU32(stack[2])
		msgLen := api.DecodeU32(stack[3])
		level, _ := readString(mod, levelPtr, levelLen)
		msg, _ := readString(mod, msgPtr, msgLen)
		// No capability check for logging; route to slog.
		switch strings.ToLower(level) {
		case "error":
			h.logger.Error(msg)
		case "warn":
			h.logger.Warn(msg)
		case "debug":
			h.logger.Debug(msg)
		default:
			h.logger.Info(msg)
		}
	})
}

// fsReadHost implements forge_host.fs_read(path_ptr,len) -> i64 packed.
func (h *hostEnv) fsReadHost() api.GoModuleFunc {
	return api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
		pathPtr := api.DecodeU32(stack[0])
		pathLen := api.DecodeU32(stack[1])
		path, ok := readString(mod, pathPtr, pathLen)
		if !ok {
			stack[0] = errorEnvelope(ctx, mod, "fs_read: out of bounds memory read")
			return
		}
		// 1. Capability declared?
		if !h.hasPerm("fs.read") {
			stack[0] = errorEnvelope(ctx, mod, fmt.Sprintf("fs_read denied: plugin %q lacks fs.read capability", h.pluginName))
			return
		}
		// 2. Perms engine check.
		if h.permsEngine != nil {
			decision := h.permsEngine.Check(perms.Request{Kind: perms.KindFsRead, Path: path})
			if !decision.Allowed {
				stack[0] = errorEnvelope(ctx, mod, fmt.Sprintf("fs_read denied: %s", decision.Rule))
				return
			}
		}
		// 3. Touch OS.
		data, err := os.ReadFile(path)
		if err != nil {
			stack[0] = errorEnvelope(ctx, mod, err.Error())
			return
		}
		// Return file content as JSON string (quoted) to preserve bytes as UTF-8 where possible.
		// Use JSON envelope {"content":"..."} is not needed; return raw bytes as JSON string.
		// Encode as JSON string to survive JSON transport: json.Marshal(string(data)).
		// The plugin is expected to decode the JSON string.
		// For binary, base64 path would be needed; WU2 test uses UTF-8.
		quoted, _ := json.Marshal(string(data))
		stack[0] = writeRawResponse(ctx, mod, quoted)
	})
}

// fsWriteHost implements forge_host.fs_write(path_ptr,len,data_ptr,len) -> i32 errno.
func (h *hostEnv) fsWriteHost() api.GoModuleFunc {
	return api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
		pathPtr := api.DecodeU32(stack[0])
		pathLen := api.DecodeU32(stack[1])
		dataPtr := api.DecodeU32(stack[2])
		dataLen := api.DecodeU32(stack[3])
		path, ok := readString(mod, pathPtr, pathLen)
		if !ok {
			stack[0] = api.EncodeU32(1)
			return
		}
		data, ok := readBytes(mod, dataPtr, dataLen)
		if !ok {
			stack[0] = api.EncodeU32(1)
			return
		}
		if !h.hasPerm("fs.write") {
			h.logger.Warn("fs_write denied: capability not declared", slog.String("path", path))
			stack[0] = api.EncodeU32(1)
			return
		}
		if h.permsEngine != nil {
			decision := h.permsEngine.Check(perms.Request{Kind: perms.KindFsWrite, Path: path})
			if !decision.Allowed {
				h.logger.Warn("fs_write denied by perms engine", slog.String("path", path), slog.String("rule", decision.Rule))
				stack[0] = api.EncodeU32(1)
				return
			}
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			stack[0] = api.EncodeU32(1)
			return
		}
		stack[0] = api.EncodeU32(0)
	})
}

// shellExecHost implements forge_host.shell_exec(cmd_ptr,len,args_json_ptr,len) -> i64 packed.
func (h *hostEnv) shellExecHost() api.GoModuleFunc {
	return api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
		cmdPtr := api.DecodeU32(stack[0])
		cmdLen := api.DecodeU32(stack[1])
		argsPtr := api.DecodeU32(stack[2])
		argsLen := api.DecodeU32(stack[3])
		cmd, ok := readString(mod, cmdPtr, cmdLen)
		if !ok {
			stack[0] = errorEnvelope(ctx, mod, "shell_exec: out of bounds memory read")
			return
		}
		var args []string
		if argsLen > 0 {
			raw, ok := readBytes(mod, argsPtr, argsLen)
			if !ok {
				stack[0] = errorEnvelope(ctx, mod, "shell_exec: out of bounds args read")
				return
			}
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &args); err != nil {
					stack[0] = errorEnvelope(ctx, mod, "shell_exec: invalid args JSON: "+err.Error())
					return
				}
			}
		}
		if !h.hasPerm("shell.exec") {
			stack[0] = errorEnvelope(ctx, mod, fmt.Sprintf("shell_exec denied: plugin %q lacks shell.exec capability", h.pluginName))
			return
		}
		if h.permsEngine != nil {
			decision := h.permsEngine.Check(perms.Request{Kind: perms.KindShell, Command: cmd, Args: args})
			if !decision.Allowed {
				stack[0] = errorEnvelope(ctx, mod, fmt.Sprintf("shell_exec denied: %s", decision.Rule))
				return
			}
		}
		// Execute with context timeout inherited.
		c := exec.CommandContext(ctx, cmd, args...)
		out, err := c.CombinedOutput()
		if err != nil {
			// Return both output and error as envelope.
			stack[0] = writeJSONResponse(ctx, mod, map[string]any{"output": string(out), "error": err.Error()})
			return
		}
		stack[0] = writeJSONResponse(ctx, mod, map[string]any{"output": string(out)})
	})
}

// gitRunHost implements forge_host.git_run(args_json_ptr,len) -> i64 packed.
func (h *hostEnv) gitRunHost() api.GoModuleFunc {
	return api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
		argsPtr := api.DecodeU32(stack[0])
		argsLen := api.DecodeU32(stack[1])
		raw, ok := readBytes(mod, argsPtr, argsLen)
		if !ok {
			stack[0] = errorEnvelope(ctx, mod, "git_run: out of bounds memory read")
			return
		}
		var args []string
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &args); err != nil {
				stack[0] = errorEnvelope(ctx, mod, "git_run: invalid args JSON: "+err.Error())
				return
			}
		}
		if len(args) == 0 {
			stack[0] = errorEnvelope(ctx, mod, "git_run: missing subcommand")
			return
		}
		subcommand := args[0]
		gitArgs := []string{}
		if len(args) > 1 {
			gitArgs = args[1:]
		}
		if !h.hasPerm("git") {
			stack[0] = errorEnvelope(ctx, mod, fmt.Sprintf("git_run denied: plugin %q lacks git capability", h.pluginName))
			return
		}
		if h.permsEngine != nil {
			decision := h.permsEngine.Check(perms.Request{Kind: perms.KindGit, Subcommand: subcommand, GitArgs: gitArgs})
			if !decision.Allowed {
				stack[0] = errorEnvelope(ctx, mod, fmt.Sprintf("git_run denied: %s", decision.Rule))
				return
			}
		}
		c := exec.CommandContext(ctx, "git", args...)
		out, err := c.CombinedOutput()
		if err != nil {
			stack[0] = writeJSONResponse(ctx, mod, map[string]any{"output": string(out), "error": err.Error()})
			return
		}
		stack[0] = writeJSONResponse(ctx, mod, map[string]any{"output": string(out)})
	})
}

// netFetchHost implements forge_host.net_fetch(url_ptr,len) -> i64 packed.
func (h *hostEnv) netFetchHost() api.GoModuleFunc {
	return api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
		urlPtr := api.DecodeU32(stack[0])
		urlLen := api.DecodeU32(stack[1])
		rawURL, ok := readString(mod, urlPtr, urlLen)
		if !ok {
			stack[0] = errorEnvelope(ctx, mod, "net_fetch: out of bounds memory read")
			return
		}
		if !h.hasPerm("net") {
			stack[0] = errorEnvelope(ctx, mod, fmt.Sprintf("net_fetch denied: plugin %q lacks net capability", h.pluginName))
			return
		}
		if !isHostAllowed(rawURL, h.netAllowlist) {
			stack[0] = errorEnvelope(ctx, mod, fmt.Sprintf("net_fetch denied: host not in allowlist: %q", rawURL))
			return
		}
		// WU2 does not perform real fetch beyond allowlist; return stub for test.
		// Real network fetch would require net/http with context and timeout; deferred until WU3+ if needed.
		stack[0] = writeJSONResponse(ctx, mod, map[string]string{"url": rawURL, "content": "net_fetch stub: allowlisted"})
	})
}

// isHostAllowed checks whether urlStr's host is in the allowlist.
// Allowlist entries are matched as exact host or suffix (e.g., "example.com" allows "api.example.com").
func isHostAllowed(urlStr string, allowlist []string) bool {
	if len(allowlist) == 0 {
		return false
	}
	u, err := url.Parse(urlStr)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Host)
	// Strip port.
	if idx := strings.Index(host, ":"); idx >= 0 {
		host = host[:idx]
	}
	for _, entry := range allowlist {
		e := strings.ToLower(entry)
		if host == e || strings.HasSuffix(host, "."+e) {
			return true
		}
	}
	return false
}
