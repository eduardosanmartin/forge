// Package e2e contains integration tests over the internal API contract.
//
// RNF-3.3 (verbatim from spec-harness-agentic.md):
// "RNF-3.3 Cobertura de tests de integración sobre el contrato de la API interna (no solo unitarios)."
//
// This file pins the contract with compile-time assertions so a breaking
// rename or signature drift fails the build, not just a runtime test.
package e2e

import (
	"testing"

	"github.com/eduardosanmartin/forge/internal/daemon"
	"github.com/eduardosanmartin/forge/internal/pluginwasm"
	"github.com/eduardosanmartin/forge/internal/skill"
)

// pluginManagerContract is the surface the daemon handler requires from
// pluginwasm.Manager. Pinning it here guarantees the exit test and the
// handler stay on the same contract (RNF-3.3).
type pluginManagerContract interface {
	Info() []pluginwasm.PluginInfo
	Reload() ([]pluginwasm.LoadResult, error)
	Enable(name string) error
	Disable(name string) error
	Close() error
}

type skillManagerContract interface {
	Info() []skill.SkillInfo
	Reload() ([]skill.LoadResult, error)
	Enable(name string) error
	Disable(name string) error
	Close() error
}

// Compile-time assertions that the real managers satisfy the contracted surface.
var (
	_ pluginManagerContract = (*pluginwasm.Manager)(nil)
	_ skillManagerContract  = (*skill.Manager)(nil)
)

// PermRequestSource note: the concrete pluginTool type is unexported in
// internal/pluginwasm, so its compile-time assertions live where the type is
// defined — internal/pluginwasm/tool.go already pins both
// `var _ tools.Tool = (*pluginTool)(nil)` and
// `var _ tools.PermRequestSource = (*pluginTool)(nil)`. The behavioral proof
// that PermRequestSource is honored end-to-end is the urlcheck exit test
// (net_fetch capability path only succeeds when the tool supplies its own
// perms.Request).

// TestContract_RPCMethodNamesUnique verifies the daemon RPC method-name
// constants are defined, non-empty, and unique (client wrappers must match
// these exact strings or the daemon rejects the call).
func TestContract_RPCMethodNamesUnique(t *testing.T) {
	methods := map[string]string{
		"plugin.list":    daemon.MethodPluginList,
		"plugin.enable":  daemon.MethodPluginEnable,
		"plugin.disable": daemon.MethodPluginDisable,
		"plugin.reload":  daemon.MethodPluginReload,
		"skill.list":     daemon.MethodSkillList,
		"skill.enable":   daemon.MethodSkillEnable,
		"skill.disable":  daemon.MethodSkillDisable,
		"skill.reload":   daemon.MethodSkillReload,
		"session.mark_success":   daemon.MethodSessionMarkSuccess,
		"session.get_messages":   daemon.MethodGetMessages,
		"session.get_messages_since": daemon.MethodGetMessagesSince,
	}
	for want, got := range methods {
		if got != want {
			t.Errorf("RPC method constant mismatch: want %q got %q", want, got)
		}
		if got == "" {
			t.Errorf("RPC method constant %q is empty", want)
		}
	}
	// Uniqueness within this seam (plugin/skill + session subset).
	seen := make(map[string]string)
	for label, m := range methods {
		if prev, ok := seen[m]; ok {
			t.Errorf("duplicate RPC method %q: %q and %q", m, prev, label)
		}
		seen[m] = label
	}
	// Also assert the full daemon RPC surface is unique (no accidental reuse).
	allMethods := []string{
		daemon.MethodCreateSession,
		daemon.MethodGetSession,
		daemon.MethodListSessions,
		daemon.MethodDeleteSession,
		daemon.MethodExecuteTurn,
		daemon.MethodGetMessages,
		daemon.MethodGetMessagesSince,
		daemon.MethodHaltSession,
		daemon.MethodResumeSession,
		daemon.MethodHaltAll,
		daemon.MethodStatus,
		daemon.MethodSwitchModel,
		daemon.MethodSessionMarkSuccess,
		daemon.MethodPluginList,
		daemon.MethodPluginEnable,
		daemon.MethodPluginDisable,
		daemon.MethodPluginReload,
		daemon.MethodSkillList,
		daemon.MethodSkillEnable,
		daemon.MethodSkillDisable,
		daemon.MethodSkillReload,
	}
	seenAll := make(map[string]bool)
	for _, m := range allMethods {
		if m == "" {
			t.Errorf("empty RPC method in full list")
		}
		if seenAll[m] {
			t.Errorf("duplicate RPC method %q in full surface", m)
		}
		seenAll[m] = true
	}
}

// TestContract_ErrorCodesUnique ensures the approval/already-enabled sentinels
// map to distinct JSON-RPC error codes (daemon handler branches on them).
func TestContract_ErrorCodesUnique(t *testing.T) {
	codes := map[string]int{
		"ErrCodeNotLoaded":       daemon.ErrCodeNotLoaded,
		"ErrCodeAlreadyEnabled":  daemon.ErrCodeAlreadyEnabled,
		"ErrCodeNotEnabled":      daemon.ErrCodeNotEnabled,
		"ErrCodeApprovalRequired": daemon.ErrCodeApprovalRequired,
		"ErrCodeAlreadyExists":   daemon.ErrCodeAlreadyExists,
	}
	seen := make(map[int]string)
	for name, c := range codes {
		if prev, ok := seen[c]; ok {
			t.Errorf("error code %d reused by %q and %q", c, prev, name)
		}
		seen[c] = name
	}
}
