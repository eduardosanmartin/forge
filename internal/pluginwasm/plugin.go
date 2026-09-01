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
	// Resolve exports.
	wp.fnAbiVersion = mod.ExportedFunction(ExportABIVersion)
	wp.fnToolList = mod.ExportedFunction(ExportToolList)
	wp.fnToolInvoke = mod.ExportedFunction(ExportToolInvoke)
	wp.fnAlloc = mod.ExportedFunction(ExportAlloc)

	// Validate required exports exist (ABI). Missing alloc is allowed to degrade gracefully
	// but abi_version, tool_list, tool_invoke are mandatory for tool plugins.
	if wp.fnAbiVersion == nil {
		_ = wp.close(ctx)
		return nil, fmt.Errorf("%w: missing export %q", ErrCorruptedWASM, ExportABIVersion)
	}
	if wp.fnToolInvoke == nil {
		_ = wp.close(ctx)
		return nil, fmt.Errorf("%w: missing export %q", ErrCorruptedWASM, ExportToolInvoke)
	}
	if wp.fnAlloc == nil {
		_ = wp.close(ctx)
		return nil, fmt.Errorf("%w: missing export %q (required for host to allocate buffers)", ErrCorruptedWASM, ExportAlloc)
	}
	// forge_tool_list may be optional if manifest declares no tools, but require it when tools present.
	if len(m.Tools) > 0 && wp.fnToolList == nil {
		_ = wp.close(ctx)
		return nil, fmt.Errorf("%w: missing export %q", ErrCorruptedWASM, ExportToolList)
	}

	// Check ABI version.
	ver, err := wp.abiVersion(ctx)
	if err != nil {
		_ = wp.close(ctx)
		return nil, fmt.Errorf("reading abi version: %w", err)
	}
	if ver != plugin.ABIVersion {
		_ = wp.close(ctx)
		return nil, fmt.Errorf("%w: plugin %q reports %d, host expects %d", ErrABIMismatch, m.Name, ver, plugin.ABIVersion)
	}

	// Optionally validate tool list matches manifest when export exists.
	if wp.fnToolList != nil {
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
