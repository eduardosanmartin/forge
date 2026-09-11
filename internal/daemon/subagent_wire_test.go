package daemon

import (
	"log/slog"
	"testing"

	"github.com/eduardosanmartin/forge/internal/agent"
	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/perms"
	"github.com/eduardosanmartin/forge/internal/tools"
)

func TestSessionManager_WiresSpawnSubagentTool(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	store := newTestStore()
	llmReg := newTestLLMRegistry()
	tmp := t.TempDir()
	policy := perms.PermissionsPolicy{
		FS: perms.FSPermissions{Read: []string{"./**"}, Write: []string{"./**"}},
		Shell: perms.ShellPermissions{Allow: []string{}},
		Git:   perms.GitPermissions{Allow: []string{}},
	}
	eng, err := perms.New(policy, tmp, logger)
	if err != nil {
		t.Fatalf("perms: %v", err)
	}
	toolsReg := tools.NewDefaultRegistry(eng, tmp, logger)
	emergency := NewEmergencyState(logger)
	cfg := config.Defaults()
	permsEng := &testPermsEngine{}
	mgr := NewSessionManager(store, llmReg, toolsReg, emergency, logger, cfg, permsEng, store, WithV1Deps(agent.V1Deps{}))
	if mgr == nil || mgr.agent == nil {
		t.Fatal("manager/agent not created")
	}
	if _, ok := toolsReg.Get("spawn_subagent"); !ok {
		t.Fatal("spawn_subagent tool not wired")
	}
	// Second wire should be idempotent.
	mgr.wireSubagentTool()
	if _, ok := toolsReg.Get("spawn_subagent"); !ok {
		t.Fatal("tool missing after second wire")
	}
}

func TestSpawnSubagentTool_PermissionIsCustomAllowed(t *testing.T) {
	tmp := t.TempDir()
	policy := perms.PermissionsPolicy{
		FS: perms.FSPermissions{Read: []string{"./**"}, Write: []string{"./**"}},
		Shell: perms.ShellPermissions{Allow: []string{}},
		Git:   perms.GitPermissions{Allow: []string{}},
		Custom: perms.CustomPermissions{Deny: []string{}},
	}
	eng, _ := perms.New(policy, tmp, nil)
	decision := eng.Check(perms.Request{Kind: perms.KindCustom, Command: "spawn_subagent"})
	if !decision.Allowed {
		t.Fatalf("spawn_subagent should be allowed via custom floor, got %v", decision)
	}
	// Deny should block.
	policy.Custom.Deny = []string{"spawn_subagent"}
	eng2, _ := perms.New(policy, tmp, nil)
	decision2 := eng2.Check(perms.Request{Kind: perms.KindCustom, Command: "spawn_subagent"})
	if decision2.Allowed {
		t.Fatal("deny should block spawn_subagent")
	}
}
