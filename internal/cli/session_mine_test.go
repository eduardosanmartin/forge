package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eduardosanmartin/forge/internal/daemon"
	"github.com/eduardosanmartin/forge/internal/mining"
	"github.com/eduardosanmartin/forge/internal/skill"
)

func TestSessionCommandConstruction(t *testing.T) {
	cmd := newSessionCommand()
	if cmd.Use != "session" {
		t.Fatalf("Use = %q want session", cmd.Use)
	}
	subs := map[string]bool{}
	for _, c := range cmd.Commands() {
		subs[c.Name()] = true
	}
	for _, want := range []string{"success", "replay"} {
		if !subs[want] {
			t.Fatalf("missing subcommand %q", want)
		}
	}
}

func TestSkillMineCommandFlags(t *testing.T) {
	cmd := newSkillMineCommand()
	if cmd.Flags().Lookup("yes") == nil {
		t.Fatalf("mine should have --yes")
	}
	if cmd.Flags().Lookup("force") == nil {
		t.Fatalf("mine should have --force")
	}
	if !strings.Contains(cmd.Long, "NEVER auto-installed") {
		t.Fatalf("mine help should mention never auto-installed")
	}
}

func TestMine_ScriptedPrompter_ProducesValidSkill(t *testing.T) {
	// Exercise the REAL proposal-writing path (writeProposalIfAccepted) without a daemon.
	tmp := t.TempDir()
	proposalsRoot := filepath.Join(tmp, ".forge", "skill-proposals")

	trajs := []mining.Trajectory{
		{SessionID: "s1", Turns: []mining.Turn{{UserPrompt: "create user alice", Steps: []mining.Step{{ToolName: "fs_read"}, {ToolName: "fs_write"}}}}},
		{SessionID: "s2", Turns: []mining.Turn{{UserPrompt: "create user bob", Steps: []mining.Step{{ToolName: "fs_read"}, {ToolName: "fs_write"}}}}},
	}
	proposals := mining.Mine(trajs, mining.Options{MinClusterSize: 2, Threshold: 0.4})
	if len(proposals) != 1 {
		t.Fatalf("expected 1 proposal, got %d", len(proposals))
	}
	p := proposals[0]

	// Interactive acceptance: ScriptedPrompter says y, then defaults for
	// name/description/category/keywords.
	prompter := NewScriptedPrompter([]string{"y", "", "", "", ""})
	var out bytes.Buffer
	if !writeProposalIfAccepted(p, proposalsRoot, false, false, prompter, &out) {
		t.Fatalf("expected proposal to be accepted and written")
	}

	skillFile := filepath.Join(proposalsRoot, p.Name, "SKILL.md")
	if _, err := os.Stat(skillFile); err != nil {
		t.Fatalf("proposal file missing: %v", err)
	}

	// The generated file must parse and validate via the skill package.
	mgr := skill.NewManager(skill.Options{ApproveExternal: true})
	defer mgr.Close()
	if _, err := mgr.Scan(proposalsRoot); err != nil {
		t.Fatalf("generated proposal failed validation: %v", err)
	}
	found := false
	for _, n := range mgr.Loaded() {
		if n == p.Name {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("proposal %q not loaded after scan of proposals root", p.Name)
	}

	// Isolation invariant (RF-4.4): scanning the SKILLS root must NOT see the
	// proposal — proposals activate only after human install + enable.
	skillsRoot := filepath.Join(tmp, ".forge", "skills")
	if err := os.MkdirAll(skillsRoot, 0o755); err != nil {
		t.Fatalf("mkdir skills root: %v", err)
	}
	mgr2 := skill.NewManager(skill.Options{ApproveExternal: true})
	defer mgr2.Close()
	if _, err := mgr2.Scan(skillsRoot); err != nil {
		t.Fatalf("scan skills root: %v", err)
	}
	for _, n := range mgr2.Loaded() {
		if n == p.Name {
			t.Fatalf("proposals should not be visible via skills scan, found %q", n)
		}
	}
}

func TestWriteProposalIfAccepted_Declined_NoFile(t *testing.T) {
	tmp := t.TempDir()
	proposalsRoot := filepath.Join(tmp, ".forge", "skill-proposals")
	p := mining.Proposal{Name: "my-proposal", Description: "d", Instructions: "body"}
	prompter := NewScriptedPrompter([]string{"n"})
	var out bytes.Buffer
	if writeProposalIfAccepted(p, proposalsRoot, false, false, prompter, &out) {
		t.Fatalf("declined proposal should not be written")
	}
	if _, err := os.Stat(filepath.Join(proposalsRoot, p.Name)); !os.IsNotExist(err) {
		t.Fatalf("declined proposal dir should not exist")
	}
}

func TestWriteProposalIfAccepted_ExistingDir_NoForce_Skips(t *testing.T) {
	tmp := t.TempDir()
	proposalsRoot := filepath.Join(tmp, ".forge", "skill-proposals")
	p := mining.Proposal{Name: "dup-proposal", Description: "d", Instructions: "b"}
	if err := os.MkdirAll(filepath.Join(proposalsRoot, p.Name), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	prompter := NewScriptedPrompter([]string{"y"})
	var out bytes.Buffer
	if writeProposalIfAccepted(p, proposalsRoot, false, false, prompter, &out) {
		t.Fatalf("existing dir without --force should skip, not write")
	}
}

func TestBuildMiningTrajectory_Grouping(t *testing.T) {
	// ToolCallResult.Function is an anonymous struct; build calls via a helper.
	mkCall := func(id, name, args string) daemon.ToolCallResult {
		return daemon.ToolCallResult{ID: id, Type: "function", Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: name, Arguments: args}}
	}
	msgs := []daemon.MessageResult{
		{Seq: 0, Role: "user", Content: "create user alice"},
		{Seq: 1, Role: "assistant", Content: "doing it", ToolCalls: []daemon.ToolCallResult{
			mkCall("c1", "fs_write", `{"path":"a.txt"}`),
		}},
		{Seq: 2, Role: "tool", ToolCallID: "c1", Name: "fs_write", Content: "wrote 10 bytes"},
		{Seq: 3, Role: "user", Content: "list users"},
		{Seq: 4, Role: "assistant", Content: "", ToolCalls: []daemon.ToolCallResult{
			mkCall("c2", "fs_read", `{"path":"users.txt"}`),
		}},
		{Seq: 5, Role: "tool", ToolCallID: "c2", Name: "fs_read", Content: "alice,bob"},
	}
	traj := buildMiningTrajectory("s1", msgs)
	if traj.SessionID != "s1" {
		t.Fatalf("SessionID = %q want s1", traj.SessionID)
	}
	if len(traj.Turns) != 2 {
		t.Fatalf("expected 2 turns, got %d", len(traj.Turns))
	}
	t0 := traj.Turns[0]
	if t0.UserPrompt != "create user alice" {
		t.Fatalf("turn 0 prompt = %q", t0.UserPrompt)
	}
	if len(t0.Steps) != 1 || t0.Steps[0].ToolName != "fs_write" {
		t.Fatalf("turn 0 steps = %+v", t0.Steps)
	}
	if t0.Steps[0].ResultSummary != "wrote 10 bytes" {
		t.Fatalf("turn 0 result summary = %q", t0.Steps[0].ResultSummary)
	}
	if traj.Turns[1].UserPrompt != "list users" || traj.Turns[1].Steps[0].ToolName != "fs_read" {
		t.Fatalf("turn 1 = %+v", traj.Turns[1])
	}

	// Out-of-order input (newest first) must be normalized before grouping.
	reversed := make([]daemon.MessageResult, len(msgs))
	for i, m := range msgs {
		reversed[len(msgs)-1-i] = m
	}
	traj2 := buildMiningTrajectory("s1", reversed)
	if len(traj2.Turns) != 2 || traj2.Turns[0].UserPrompt != "create user alice" {
		t.Fatalf("reversed input not normalized: %+v", traj2.Turns)
	}
}

func TestProposalsNotScannedBySkillsManager(t *testing.T) {
	tmp := t.TempDir()
	skillsRoot := filepath.Join(tmp, ".forge", "skills")
	proposalsRoot := filepath.Join(tmp, ".forge", "skill-proposals")
	// Create a valid skill in proposals only.
	dir := filepath.Join(proposalsRoot, "my-proposal")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: my-proposal\ndescription: \"desc\"\nsource: local\n---\nBody\n"), 0o644)

	mgr := skill.NewManager(skill.Options{ApproveExternal: true})
	defer mgr.Close()
	// Scan skills root should find nothing.
	if _, err := mgr.Scan(skillsRoot); err != nil {
		t.Fatalf("scan skills: %v", err)
	}
	if len(mgr.Loaded()) != 0 {
		t.Fatalf("expected 0 skills in skillsRoot, got %v", mgr.Loaded())
	}
	// Scan proposals root should find one.
	if _, err := mgr.Scan(proposalsRoot); err != nil {
		t.Fatalf("scan proposals: %v", err)
	}
	if len(mgr.Loaded()) != 1 || mgr.Loaded()[0] != "my-proposal" {
		t.Fatalf("expected my-proposal in proposals scan, got %v", mgr.Loaded())
	}
}
