package pluginwasm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/eduardosanmartin/forge/internal/perms"
	"github.com/eduardosanmartin/forge/internal/plugin"
	"github.com/eduardosanmartin/forge/internal/tools"
)

// Options configures a Manager.
type Options struct {
	// Perms is the permission engine used to authorize host calls from plugins.
	// If nil, only capability declarations are checked (no perms engine).
	Perms *perms.Engine
	// NetAllowlist is the list of hosts allowed for net_fetch host imports.
	// The check is done per plugin call; an empty list denies all net_fetch.
	NetAllowlist []string
	// Logger receives plugin log messages with a plugin name attribute.
	Logger *slog.Logger
	// ApproveExternal must be true to load any plugin whose manifest source is "external".
	// This is the WU2 fail-closed human approval flag; WU4 UX will gate this.
	ApproveExternal bool
	// AutoEnableLocal when true makes LoadAll and Reload auto-enable every
	// successfully loaded LOCAL plugin right after load. External plugins
	// ALWAYS require explicit Enable regardless of this flag.
	AutoEnableLocal bool
}

// LoadResult reports the outcome of loading one plugin directory discovered under the root.
type LoadResult struct {
	Name string
	// Loaded is true when the plugin was successfully compiled and instantiated.
	Loaded bool
	// Err is the per-plugin error when Loaded is false. It includes checksum,
	// approval, ABI, and wasm corruption failures.
	Err error
}

// PluginInfo describes a loaded plugin for Info() / RPC listing.
type PluginInfo struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Source    string `json:"source"`
	Enabled   bool   `json:"enabled"`
	ToolCount int    `json:"tool_count"`
}

// Manager loads, enables, and disables WASM plugins, bridging their tools into a tools.Registry.
// The approval record binds the artifact hash (these exact bytes were approved), not the directory.
type Manager struct {
	reg             *tools.Registry
	permsEngine     *perms.Engine
	netAllowlist    []string
	logger          *slog.Logger
	approveExternal bool
	autoEnableLocal bool

	mu      sync.Mutex
	plugins map[string]*wasmPlugin // all loaded (instantiated) plugins by manifest name
	enabled map[string]bool
	root    string // remembered root for Reload()
}

// NewManager creates a Manager that will register plugin tools into reg.
func NewManager(reg *tools.Registry, opts Options) *Manager {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{
		reg:             reg,
		permsEngine:     opts.Perms,
		netAllowlist:    opts.NetAllowlist,
		logger:          logger,
		approveExternal: opts.ApproveExternal,
		autoEnableLocal: opts.AutoEnableLocal,
		plugins:         make(map[string]*wasmPlugin),
		enabled:         make(map[string]bool),
	}
}

// isApproved verifies that dir/approved.flag contains "sha256:<hex>" matching
// the hash of wasmBytes. The approval record binds the artifact hash
// (these exact bytes were approved), not the directory.
func isApproved(dir string, wasmBytes []byte) bool {
	data, err := os.ReadFile(filepath.Join(dir, "approved.flag"))
	if err != nil {
		return false
	}
	want := strings.TrimSpace(string(data))
	if !strings.HasPrefix(want, "sha256:") {
		return false
	}
	sum := sha256.Sum256(wasmBytes)
	got := "sha256:" + hex.EncodeToString(sum[:])
	return strings.EqualFold(want, got)
}

// LoadAll scans root (spec layout: forge-plugins/<name>/manifest.toml + entrypoint).
// For each subdirectory it parses the manifest, verifies checksum before any load (RNF-4.6),
// enforces external approval (global flag OR per-plugin approved.flag), compiles the wasm bytes,
// and instantiates the module.
// It returns per-plugin LoadResults and an aggregated error if any plugin failed.
// Successfully loaded plugins remain available for Enable; failed plugins are not inserted.
// The scan skips non-directories and directories without a manifest.toml.
// Missing root is NOT an error (zero plugins is valid).
func (m *Manager) LoadAll(root string) ([]LoadResult, error) {
	m.mu.Lock()
	m.root = root
	m.mu.Unlock()

	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("plugin root %q: %w", root, err)
	}
	var results []LoadResult
	var errs []string
	ctx := context.Background()
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pluginDir := filepath.Join(root, e.Name())
		manifestPath := filepath.Join(pluginDir, "manifest.toml")
		if _, err := os.Stat(manifestPath); err != nil {
			// Not a plugin directory; skip without error.
			continue
		}
		res := LoadResult{Name: e.Name()}
		if err := m.loadOne(ctx, pluginDir, manifestPath); err != nil {
			res.Err = err
			res.Loaded = false
			errs = append(errs, fmt.Sprintf("%s: %v", e.Name(), err))
		} else {
			res.Loaded = true
		}
		results = append(results, res)
	}
	// Auto-enable locals if policy is enabled. Collect per-plugin errors but do not fail the whole load.
	if m.autoEnableLocal {
		// Collect local names under lock, then Enable outside lock to respect locking.
		m.mu.Lock()
		var locals []string
		for name, wp := range m.plugins {
			if wp.manifest.Source == plugin.SourceLocal {
				locals = append(locals, name)
			}
		}
		m.mu.Unlock()
		for _, name := range locals {
			if err := m.Enable(name); err != nil {
				// Log but do not aggregate into returned error; mirroring runServe's previous per-plugin log.
				if m.logger != nil {
					m.logger.Warn("auto-enable local plugin failed", "plugin", name, "error", err)
				}
				// Do not treat as load failure; the plugin is still loaded, just not enabled.
			}
		}
	}
	if len(errs) > 0 {
		return results, fmt.Errorf("plugin load failures: %s", strings.Join(errs, "; "))
	}
	return results, nil
}

// loadOne loads a single plugin from pluginDir/manifest.toml.
func (m *Manager) loadOne(ctx context.Context, pluginDir, manifestPath string) error {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	manifest, err := plugin.ParseManifest(data)
	if err != nil {
		return fmt.Errorf("parse manifest: %w", err)
	}
	// Resolve entrypoint relative to pluginDir.
	entryPath := filepath.Join(pluginDir, filepath.FromSlash(manifest.Entrypoint))
	wasmBytes, err := os.ReadFile(entryPath)
	if err != nil {
		return fmt.Errorf("read entrypoint %q: %w", manifest.Entrypoint, err)
	}
	if len(wasmBytes) == 0 {
		return fmt.Errorf("%w: entrypoint %q is empty", ErrCorruptedWASM, manifest.Entrypoint)
	}
	// RNF-4.6: checksum verified BEFORE any load for external plugins.
	// The approval record binds the artifact hash (these exact bytes were approved), not the directory.
	if manifest.Source == plugin.SourceExternal {
		if !m.approveExternal && !isApproved(pluginDir, wasmBytes) {
			return fmt.Errorf("%w: external plugin %q requires explicit approval (approval record missing or does not match the current artifact; re-run 'forge plugin install' or start serve with --approve-external-plugins)", ErrApprovalRequired, manifest.Name)
		}
		if err := verifyChecksum(wasmBytes, manifest.Checksum); err != nil {
			return err
		}
	}
	env := newHostEnv(manifest, m.permsEngine, m.netAllowlist, m.logger)
	wp, err := newWasmPlugin(ctx, manifest, wasmBytes, env)
	if err != nil {
		return err
	}
	wp.pluginDir = pluginDir
	m.mu.Lock()
	defer m.mu.Unlock()
	// If a plugin with same name was already loaded, close the old one.
	if old, ok := m.plugins[manifest.Name]; ok {
		_ = old.close(ctx)
	}
	m.plugins[manifest.Name] = wp
	return nil
}

// verifyChecksum checks wasmBytes against "sha256:<64hex>".
func verifyChecksum(wasmBytes []byte, checksum string) error {
	if !strings.HasPrefix(checksum, "sha256:") {
		return fmt.Errorf("%w: checksum %q must have sha256: prefix", ErrChecksumMismatch, checksum)
	}
	want := strings.TrimPrefix(checksum, "sha256:")
	sum := sha256.Sum256(wasmBytes)
	got := hex.EncodeToString(sum[:])
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("%w: want %s got %s", ErrChecksumMismatch, want, got)
	}
	return nil
}

// Enable registers the plugin's tools into the Registry. The plugin must have been loaded via LoadAll.
// For external plugins it requires either Options.ApproveExternal or an approved.flag file in the plugin dir.
// Enable is idempotent-safe: calling it twice on the same plugin returns ErrAlreadyEnabled.
func (m *Manager) Enable(name string) error {
	m.mu.Lock()
	wp, ok := m.plugins[name]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("%w: %q", ErrNotLoaded, name)
	}
	if m.enabled[name] {
		m.mu.Unlock()
		return fmt.Errorf("%w: %q", ErrAlreadyEnabled, name)
	}
	if wp.manifest.Source == plugin.SourceExternal {
		if !m.approveExternal && !isApproved(wp.pluginDir, wp.wasmBytes) {
			m.mu.Unlock()
			return fmt.Errorf("%w: external plugin %q requires explicit approval (approval record missing or does not match the current artifact; re-run 'forge plugin install' or start serve with --approve-external-plugins)", ErrApprovalRequired, name)
		}
	}
	// Mark enabled before registering to avoid races; rollback on failure.
	m.enabled[name] = true
	m.mu.Unlock()

	// Register each tool export.
	for _, te := range wp.manifest.Tools {
		tool := newPluginTool(wp.manifest, te, wp)
		m.reg.Register(tool)
	}
	return nil
}

// Disable unregisters the plugin's tools from the Registry. It does not unload the wasm module,
// so Enable can be called again without recompiling.
func (m *Manager) Disable(name string) error {
	m.mu.Lock()
	wp, ok := m.plugins[name]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("%w: %q", ErrNotLoaded, name)
	}
	if !m.enabled[name] {
		m.mu.Unlock()
		return fmt.Errorf("%w: %q", ErrNotEnabled, name)
	}
	delete(m.enabled, name)
	m.mu.Unlock()

	for _, te := range wp.manifest.Tools {
		m.reg.Unregister(te.Name)
	}
	return nil
}

// Loaded returns the names of all successfully loaded plugins, sorted.
func (m *Manager) Loaded() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.plugins))
	for n := range m.plugins {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Enabled returns the names of currently enabled plugins, sorted.
func (m *Manager) Enabled() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.enabled))
	for n := range m.enabled {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Info returns sorted PluginInfo for every loaded plugin.
func (m *Manager) Info() []PluginInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]PluginInfo, 0, len(m.plugins))
	for name, wp := range m.plugins {
		out = append(out, PluginInfo{
			Name:      name,
			Version:   wp.manifest.Version,
			Source:    string(wp.manifest.Source),
			Enabled:   m.enabled[name],
			ToolCount: len(wp.manifest.Tools),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Reload re-scans the remembered root, discarding previous state (fresh state), and returns LoadResults.
// If no root has been set (LoadAll never called), it returns nil, nil.
// It closes existing wasm runtimes and unregisters their tools before reloading.
func (m *Manager) Reload() ([]LoadResult, error) {
	m.mu.Lock()
	root := m.root
	m.mu.Unlock()
	if root == "" {
		return nil, nil
	}
	// Capture existing plugins to close after unlocking.
	m.mu.Lock()
	pluginsCopy := make(map[string]*wasmPlugin, len(m.plugins))
	var enabledToolNames []string
	for k, v := range m.plugins {
		pluginsCopy[k] = v
		if m.enabled[k] {
			for _, te := range v.manifest.Tools {
				enabledToolNames = append(enabledToolNames, te.Name)
			}
		}
	}
	// Reset maps for fresh state.
	m.plugins = make(map[string]*wasmPlugin)
	m.enabled = make(map[string]bool)
	m.mu.Unlock()

	for _, name := range enabledToolNames {
		m.reg.Unregister(name)
	}
	ctx := context.Background()
	for _, wp := range pluginsCopy {
		_ = wp.close(ctx)
	}
	return m.LoadAll(root)
}

// Close closes all loaded wasm plugins and their wazero runtimes. It is idempotent.
// Tools of enabled plugins are unregistered from the Registry so it never holds
// stale entries pointing at closed runtimes.
func (m *Manager) Close() error {
	m.mu.Lock()
	plugins := make(map[string]*wasmPlugin, len(m.plugins))
	var enabledToolNames []string
	for k, v := range m.plugins {
		plugins[k] = v
		if m.enabled[k] {
			for _, te := range v.manifest.Tools {
				enabledToolNames = append(enabledToolNames, te.Name)
			}
		}
	}
	m.mu.Unlock()

	// Unregister enabled plugin tools first: their runtimes are about to close.
	for _, name := range enabledToolNames {
		m.reg.Unregister(name)
	}

	ctx := context.Background()
	var errs []string
	for name, wp := range plugins {
		if err := wp.close(ctx); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", name, err))
		}
	}
	m.mu.Lock()
	// Clear maps to make Close idempotent.
	m.plugins = make(map[string]*wasmPlugin)
	m.enabled = make(map[string]bool)
	m.mu.Unlock()

	if len(errs) > 0 {
		return fmt.Errorf("close errors: %s", strings.Join(errs, "; "))
	}
	return nil
}
