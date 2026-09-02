package pluginwasm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"

	"github.com/eduardosanmartin/forge/internal/plugin"
)

// wasmPlugin wraps a single instantiated WASM plugin and its wazero runtime.
// It serves BOTH tool plugins (ABI v1) and provider plugins (ABI v2 LLM streaming)
// differentiated by manifest.Kind. The struct holds exports for both kinds; unused
// function slots are nil.
type wasmPlugin struct {
	manifest  plugin.Manifest
	wasmBytes []byte
	runtime   wazero.Runtime
	mod       api.Module
	env       *hostEnv
	pluginDir string

	mu sync.Mutex

	fnAbiVersion api.Function
	fnToolList   api.Function
	fnToolInvoke api.Function
	fnAlloc      api.Function

	// ABI v2 provider exports (nil for tool plugins)
	fnLLMStreamStart api.Function
	fnLLMNextChunk   api.Function
	fnLLMCancel      api.Function
}

// newWasmPlugin compiles and instantiates wasmBytes under a new wazero Runtime
// with host imports bound to env. It verifies ABI version after instantiation.
func newWasmPlugin(ctx context.Context, m plugin.Manifest, wasmBytes []byte, env *hostEnv) (*wasmPlugin, error) {
	rt := wazero.NewRuntime(ctx)

	// WASI stub for GOOS=wasip1 modules (they import wasi_snapshot_preview1).
	// The standard wasi_snapshot_preview1 implementation closes the module on
	// proc_exit(0) after _start, which would hide plugin exports. For reactor
	// plugins built with //go:wasmexport we stub proc_exit as a no-op and keep
	// the module alive after _start completes. This also avoids pulling the full
	// WASI filesystem handling for these isolated test plugins.
	if _, err := instantiateWasiStub(ctx, rt); err != nil {
		_ = rt.Close(ctx)
		return nil, fmt.Errorf("%w: wasi stub instantiate: %v", ErrCorruptedWASM, err)
	}

	// Register host module.
	hostBuilder := rt.NewHostModuleBuilder(HostModule)
	hostBuilder.NewFunctionBuilder().WithGoModuleFunction(env.logHost(), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).Export("log")
	hostBuilder.NewFunctionBuilder().WithGoModuleFunction(env.fsReadHost(), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI64}).Export("fs_read")
	hostBuilder.NewFunctionBuilder().WithGoModuleFunction(env.fsWriteHost(), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI32}).Export("fs_write")
	hostBuilder.NewFunctionBuilder().WithGoModuleFunction(env.shellExecHost(), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI64}).Export("shell_exec")
	hostBuilder.NewFunctionBuilder().WithGoModuleFunction(env.gitRunHost(), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI64}).Export("git_run")
	hostBuilder.NewFunctionBuilder().WithGoModuleFunction(env.netFetchHost(), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI64}).Export("net_fetch")

	if _, err := hostBuilder.Instantiate(ctx); err != nil {
		_ = rt.Close(ctx)
		return nil, fmt.Errorf("%w: host module instantiate: %v", ErrCorruptedWASM, err)
	}

	compiled, err := rt.CompileModule(ctx, wasmBytes)
	if err != nil {
		_ = rt.Close(ctx)
		return nil, fmt.Errorf("%w: compile: %v", ErrCorruptedWASM, err)
	}
	// Use default module config which calls _start. With our stubbed WASI,
	// _start will init the Go runtime and then call proc_exit(0) as a no-op,
	// leaving the module alive for subsequent export calls. This is required
	// because disabling _start leaves the runtime in notInitialized state.
	// Capture stderr for Go panic diagnostics.
	var errBuf strings.Builder
	mod, err := rt.InstantiateModule(ctx, compiled, wazero.NewModuleConfig().WithName(m.Name).WithStderr(&errBuf))
	if err != nil && errBuf.Len() > 0 {
		// Surface WASI stderr (Go panic) in error for diagnostics.
		err = fmt.Errorf("%w: stderr=%q", err, errBuf.String())
	}
	if err != nil {
		_ = rt.Close(ctx)
		return nil, fmt.Errorf("%w: instantiate: %v", ErrCorruptedWASM, err)
	}

	wp := &wasmPlugin{
		manifest:  m,
		wasmBytes: wasmBytes,
		runtime:   rt,
		mod:       mod,
		env:       env,
	}
	// Resolve exports (both ABIs; unused slots stay nil).
	wp.fnAbiVersion = mod.ExportedFunction(ExportABIVersion)
	wp.fnToolList = mod.ExportedFunction(ExportToolList)
	wp.fnToolInvoke = mod.ExportedFunction(ExportToolInvoke)
	wp.fnAlloc = mod.ExportedFunction(ExportAlloc)
	wp.fnLLMStreamStart = mod.ExportedFunction(ExportLLMStreamStart)
	wp.fnLLMNextChunk = mod.ExportedFunction(ExportLLMNextChunk)
	wp.fnLLMCancel = mod.ExportedFunction(ExportLLMCancel)

	// Common required: abi_version and alloc.
	if wp.fnAbiVersion == nil {
		_ = wp.close(ctx)
		return nil, fmt.Errorf("%w: missing export %q", ErrCorruptedWASM, ExportABIVersion)
	}
	if wp.fnAlloc == nil {
		_ = wp.close(ctx)
		return nil, fmt.Errorf("%w: missing export %q (required for host to allocate buffers)", ErrCorruptedWASM, ExportAlloc)
	}

	// Kind-specific export validation.
	isProvider := m.Kind == plugin.KindProvider
	if isProvider {
		// Provider plugins require llm exports, must NOT have tool invoke (allow but not required? enforce missing tool invoke is okay)
		if wp.fnLLMStreamStart == nil {
			_ = wp.close(ctx)
			return nil, fmt.Errorf("%w: provider plugin %q missing export %q", ErrCorruptedWASM, m.Name, ExportLLMStreamStart)
		}
		if wp.fnLLMNextChunk == nil {
			_ = wp.close(ctx)
			return nil, fmt.Errorf("%w: provider plugin %q missing export %q", ErrCorruptedWASM, m.Name, ExportLLMNextChunk)
		}
		if wp.fnLLMCancel == nil {
			_ = wp.close(ctx)
			return nil, fmt.Errorf("%w: provider plugin %q missing export %q", ErrCorruptedWASM, m.Name, ExportLLMCancel)
		}
	} else {
		// Tool plugin (default).
		if wp.fnToolInvoke == nil {
			_ = wp.close(ctx)
			return nil, fmt.Errorf("%w: missing export %q", ErrCorruptedWASM, ExportToolInvoke)
		}
		// forge_tool_list may be optional if manifest declares no tools, but require it when tools present.
		if len(m.Tools) > 0 && wp.fnToolList == nil {
			_ = wp.close(ctx)
			return nil, fmt.Errorf("%w: missing export %q", ErrCorruptedWASM, ExportToolList)
		}
	}

	// Check ABI version (allow both 1 and 2 for backward compat; enforce kind matches).
	ver, err := wp.abiVersion(ctx)
	if err != nil {
		_ = wp.close(ctx)
		return nil, fmt.Errorf("reading abi version: %w", err)
	}
	// Enforce supported set.
	supported := false
	for _, v := range plugin.SupportedABIVersions {
		if ver == int64(v) {
			supported = true
			break
		}
	}
	if !supported {
		_ = wp.close(ctx)
		return nil, fmt.Errorf("%w: plugin %q reports %d, host supports %v", ErrABIMismatch, m.Name, ver, plugin.SupportedABIVersions)
	}
	if isProvider && ver != plugin.ABIVersionV2 {
		_ = wp.close(ctx)
		return nil, fmt.Errorf("%w: provider plugin %q must report ABI %d, got %d", ErrABIMismatch, m.Name, plugin.ABIVersionV2, ver)
	}
	if !isProvider && ver != plugin.ABIVersion {
		_ = wp.close(ctx)
		return nil, fmt.Errorf("%w: tool plugin %q must report ABI %d, got %d", ErrABIMismatch, m.Name, plugin.ABIVersion, ver)
	}

	// Optionally validate tool list matches manifest when export exists (tool plugins only).
	if !isProvider && wp.fnToolList != nil {
		list, err := wp.toolList(ctx)
		if err != nil {
			_ = wp.close(ctx)
			return nil, fmt.Errorf("reading tool list: %w", err)
		}
		if len(list) != len(m.Tools) {
			_ = wp.close(ctx)
			return nil, fmt.Errorf("%w: tool count mismatch: wasm reports %d, manifest %d", ErrCorruptedWASM, len(list), len(m.Tools))
		}
		// Validate names match manifest order-independently.
		want := make(map[string]bool, len(m.Tools))
		for _, t := range m.Tools {
			want[t.Name] = true
		}
		for _, t := range list {
			if !want[t.Name] {
				_ = wp.close(ctx)
				return nil, fmt.Errorf("%w: wasm tool list contains %q not in manifest", ErrCorruptedWASM, t.Name)
			}
		}
	}

	return wp, nil
}

func (p *wasmPlugin) close(ctx context.Context) error {
	if p.runtime == nil {
		return nil
	}
	// Close module first if not already closed, then runtime.
	if p.mod != nil && !p.mod.IsClosed() {
		_ = p.mod.Close(ctx)
	}
	err := p.runtime.Close(ctx)
	p.runtime = nil
	return err
}

// abiVersion reads forge_abi_version.
func (p *wasmPlugin) abiVersion(ctx context.Context) (int64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fnAbiVersion == nil {
		return 0, fmt.Errorf("missing export %q", ExportABIVersion)
	}
	res, err := p.fnAbiVersion.Call(ctx)
	if err != nil {
		return 0, err
	}
	if len(res) == 0 {
		return 0, fmt.Errorf("forge_abi_version returned no values")
	}
	// Tolerate both i32 and i64 exports: an i32 result arrives zero-extended
	// into the low 32 bits, so re-interpret it as signed 32-bit.
	val := res[0]
	if val <= 0xffffffff {
		return int64(int32(uint32(val))), nil
	}
	return int64(val), nil
}

// toolList calls forge_tool_list and decodes the JSON array.
func (p *wasmPlugin) toolList(ctx context.Context) ([]plugin.ToolExport, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fnToolList == nil {
		return nil, nil
	}
	res, err := p.fnToolList.Call(ctx)
	if err != nil {
		return nil, err
	}
	if len(res) == 0 {
		return nil, fmt.Errorf("forge_tool_list returned no values")
	}
	packed := res[0]
	ptr, ln := unpack(packed)
	if ln == 0 {
		return []plugin.ToolExport{}, nil
	}
	buf, ok := p.mod.Memory().Read(ptr, ln)
	if !ok {
		return nil, fmt.Errorf("forge_tool_list: out of bounds ptr=%d len=%d", ptr, ln)
	}
	data := make([]byte, len(buf))
	copy(data, buf)
	var out []plugin.ToolExport
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("forge_tool_list JSON decode: %w (data=%q)", err, string(data))
	}
	return out, nil
}

// invokeTool calls forge_tool_invoke with fnName and argsJSON and returns raw result bytes.
// It is mutex-serialized per plugin because wazero Function.Call is not goroutine-safe.
func (p *wasmPlugin) invokeTool(ctx context.Context, fnName string, argsJSON []byte) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.fnToolInvoke == nil {
		return nil, fmt.Errorf("missing export %q", ExportToolInvoke)
	}
	fnPtr, fnLen, err := p.allocAndWriteLocked(ctx, []byte(fnName))
	if err != nil {
		return nil, fmt.Errorf("alloc fn name: %w", err)
	}
	argsPtr, argsLen, err := p.allocAndWriteLocked(ctx, argsJSON)
	if err != nil {
		return nil, fmt.Errorf("alloc args: %w", err)
	}
	res, err := p.fnToolInvoke.Call(ctx, api.EncodeU32(fnPtr), api.EncodeU32(fnLen), api.EncodeU32(argsPtr), api.EncodeU32(argsLen))
	if err != nil {
		return nil, fmt.Errorf("forge_tool_invoke call failed: %w", err)
	}
	if len(res) == 0 {
		return nil, fmt.Errorf("forge_tool_invoke returned no values")
	}
	packed := res[0]
	ptr, ln := unpack(packed)
	if ln == 0 {
		return []byte{}, nil
	}
	buf, ok := p.mod.Memory().Read(ptr, ln)
	if !ok {
		return nil, fmt.Errorf("forge_tool_invoke: out of bounds ptr=%d len=%d", ptr, ln)
	}
	out := make([]byte, len(buf))
	copy(out, buf)
	return out, nil
}

// allocAndWriteLocked is the mutex-held variant used by invokeTool to avoid double-locking.
func (p *wasmPlugin) allocAndWriteLocked(ctx context.Context, data []byte) (uint32, uint32, error) {
	if len(data) == 0 {
		return 0, 0, nil
	}
	res, err := p.fnAlloc.Call(ctx, api.EncodeU32(uint32(len(data))))
	if err != nil {
		return 0, 0, err
	}
	if len(res) == 0 {
		return 0, 0, fmt.Errorf("forge_alloc returned no values")
	}
	ptr := api.DecodeU32(res[0])
	if ptr == 0 {
		return 0, 0, fmt.Errorf("forge_alloc returned 0 for size %d", len(data))
	}
	if !p.mod.Memory().Write(ptr, data) {
		return 0, 0, fmt.Errorf("memory write failed")
	}
	return ptr, uint32(len(data)), nil
}

// --- ABI v2 provider streaming (host-driven PULL, see abi.go design note) ---

// llmStart starts a provider streaming request. It allocates the request JSON,
// calls forge_llm_stream_start, and returns the plugin-assigned reqId.
// It is mutex-serialized and honors ctx + per-call timeout guard.
func (p *wasmPlugin) llmStart(ctx context.Context, reqJSON []byte) (uint32, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fnLLMStreamStart == nil {
		return 0, fmt.Errorf("missing export %q", ExportLLMStreamStart)
	}
	ptr, ln, err := p.allocAndWriteLocked(ctx, reqJSON)
	if err != nil {
		return 0, fmt.Errorf("alloc llm req: %w", err)
	}
	// Bounded call timeout so a hung plugin does not hang daemon forever.
	callCtx, cancel := context.WithTimeout(ctx, PluginLLMCallTimeout)
	defer cancel()
	res, err := p.fnLLMStreamStart.Call(callCtx, api.EncodeU32(ptr), api.EncodeU32(ln))
	if err != nil {
		return 0, fmt.Errorf("forge_llm_stream_start: %w", err)
	}
	if len(res) == 0 {
		return 0, fmt.Errorf("forge_llm_stream_start returned no values")
	}
	packed := res[0]
	rptr, rlen := unpack(packed)
	if rlen == 0 {
		return 0, fmt.Errorf("forge_llm_stream_start returned empty")
	}
	buf, ok := p.mod.Memory().Read(rptr, rlen)
	if !ok {
		return 0, fmt.Errorf("forge_llm_stream_start: out of bounds ptr=%d len=%d", rptr, rlen)
	}
	data := make([]byte, len(buf))
	copy(data, buf)
	// Expect {"req_id": N} or {"error": "...","code": ...}
	var startResp map[string]any
	if err := json.Unmarshal(data, &startResp); err != nil {
		return 0, fmt.Errorf("forge_llm_stream_start JSON decode: %w (data=%q)", err, string(data))
	}
	if errMsg, ok := startResp["error"].(string); ok {
		code := 0
		if c, ok := startResp["code"].(float64); ok {
			code = int(c)
		}
		return 0, fmt.Errorf("plugin llm_stream_start error (code %d): %s", code, errMsg)
	}
	// req_id may be int or float64 per JSON numbers.
	var reqID uint32
	if v, ok := startResp["req_id"]; ok {
		switch n := v.(type) {
		case float64:
			reqID = uint32(n)
		case int:
			reqID = uint32(n)
		case int64:
			reqID = uint32(n)
		default:
			return 0, fmt.Errorf("invalid req_id type %T value %v", v, v)
		}
	} else {
		return 0, fmt.Errorf("forge_llm_stream_start missing req_id: %q", string(data))
	}
	return reqID, nil
}

// llmNextChunk pulls the next chunk for reqID. Returns (chunk, done, error).
// done==true means stream finished (host should stop pulling). On error, stream is terminal.
// It decodes the JSON packed result: either a StreamChunk, {"done":true}, or {"error":...}.
func (p *wasmPlugin) llmNextChunk(ctx context.Context, reqID uint32) ([]byte, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fnLLMNextChunk == nil {
		return nil, false, fmt.Errorf("missing export %q", ExportLLMNextChunk)
	}
	callCtx, cancel := context.WithTimeout(ctx, PluginLLMCallTimeout)
	defer cancel()
	res, err := p.fnLLMNextChunk.Call(callCtx, api.EncodeU32(reqID))
	if err != nil {
		return nil, false, fmt.Errorf("forge_llm_next_chunk: %w", err)
	}
	if len(res) == 0 {
		return nil, false, fmt.Errorf("forge_llm_next_chunk returned no values")
	}
	packed := res[0]
	ptr, ln := unpack(packed)
	if ln == 0 {
		// Treat empty as terminal with no data? For strict ABI, empty is not valid; but allow as done.
		return nil, true, nil
	}
	buf, ok := p.mod.Memory().Read(ptr, ln)
	if !ok {
		return nil, false, fmt.Errorf("forge_llm_next_chunk: out of bounds ptr=%d len=%d", ptr, ln)
	}
	data := make([]byte, len(buf))
	copy(data, buf)
	// Inspect for done / error before treating as StreamChunk.
	var probe map[string]any
	if err := json.Unmarshal(data, &probe); err == nil {
		if done, ok := probe["done"].(bool); ok && done {
			return nil, true, nil
		}
		if errMsg, ok := probe["error"].(string); ok {
			return nil, false, fmt.Errorf("%s", errMsg)
		}
	}
	// Otherwise it's a StreamChunk JSON — return raw for caller to unmarshal.
	return data, false, nil
}

// llmCancel asks the plugin to cancel reqID. Best-effort; ctx is independent.
func (p *wasmPlugin) llmCancel(ctx context.Context, reqID uint32) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fnLLMCancel == nil {
		return
	}
	callCtx, cancel := context.WithTimeout(ctx, PluginLLMCallTimeout)
	defer cancel()
	_, _ = p.fnLLMCancel.Call(callCtx, api.EncodeU32(reqID))
}

// instantiateWasiStub registers wasi_snapshot_preview1 with the default WASI
// implementation but overrides proc_exit to be a no-op. The default WASI's
// proc_exit closes the module (sys.ExitError) which would hide plugin exports
// after Go's _start calls proc_exit(0). Overriding keeps the module alive so
// subsequent forge_abi_version / forge_tool_invoke calls remain callable while
// preserving full WASI file/clock/random semantics for Go's runtime init.
func instantiateWasiStub(ctx context.Context, rt wazero.Runtime) (api.Module, error) {
	builder := rt.NewHostModuleBuilder("wasi_snapshot_preview1")
	// Export the full default WASI set first.
	wasi_snapshot_preview1.NewFunctionExporter().ExportFunctions(builder)
	// Override proc_exit with a no-op that keeps the module alive.
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(_ context.Context, _ api.Module, stack []uint64) {
			// no-op: do not close module when Go's runtime calls proc_exit(0) after _start
		}), []api.ValueType{api.ValueTypeI32}, []api.ValueType{}).
		Export("proc_exit")
	return builder.Instantiate(ctx)
}
