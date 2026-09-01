package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/eduardosanmartin/forge/internal/skill"
	"github.com/eduardosanmartin/forge/internal/store"
	"github.com/eduardosanmartin/forge/internal/tools"
)

func TestContextAssembler_Build_SkillsInjection(t *testing.T) {
	ctx := context.Background()

	// Create a skill manager with one local skill.
	root := t.TempDir()
	skillDir := filepath.Join(root, "code-review-style")
	desc := "Provides guidance for code reviews and pull request style checks"
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := "---\nname: code-review-style\ndescription: \"" + desc + "\"\nsource: local\nactivation_keywords: [\"code review\", \"style\", \"PR\"]\n---\nSKILL BODY: follow style guide\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	mgr := skill.NewManager(skill.Options{MinScore: 0.4, TopK: 1})
	defer mgr.Close()
	if _, err := mgr.Scan(root); err != nil {
		t.Fatalf("scan: %v", err)
	}

	combined := desc + " code review style PR"
	paraphrase := "Please review this code for style issues and PR feedback"
	unrelated := "weather forecast gardening cooking"

	tests := []struct {
		name       string
		metadata   map[string]any
		skillsDep  *skill.Manager
		userMsg    string
		wantInject bool
	}{
		{
			name:       "enabled flag and matching query injects",
			metadata:   map[string]any{"v1_skills": true},
			skillsDep:  mgr,
			userMsg:    combined,
			wantInject: true,
		},
		{
			name:       "enabled flag and realistic paraphrase injects",
			metadata:   map[string]any{"v1_skills": true},
			skillsDep:  mgr,
			userMsg:    paraphrase,
			wantInject: true,
		},
		{
			name:       "enabled flag but unrelated query no injection",
			metadata:   map[string]any{"v1_skills": true},
			skillsDep:  mgr,
			userMsg:    unrelated,
			wantInject: false,
		},
		{
			name:       "flag off no injection even with matching query",
			metadata:   map[string]any{"v1_skills": false},
			skillsDep:  mgr,
			userMsg:    combined,
			wantInject: false,
		},
		{
			name:       "flag on but dep nil no injection",
			metadata:   map[string]any{"v1_skills": true},
			skillsDep:  nil,
			userMsg:    combined,
			wantInject: false,
		},
		{
			name:       "flag missing (default off) no injection",
			metadata:   map[string]any{},
			skillsDep:  mgr,
			userMsg:    combined,
			wantInject: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assembler := NewContextAssembler(tools.New(nil, "", nil), &contextMockStore{
				session: &store.Session{ID: "session-1", Metadata: tc.metadata},
			}, 10)
			assembler.SetV1Deps(V1Deps{Skills: tc.skillsDep})
			messages, err := assembler.Build(ctx, "session-1", tc.userMsg)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			_, ok := findSystemMessageByPrefix(messages, "SKILL INSTRUCTIONS (v1)")
			if ok != tc.wantInject {
				t.Errorf("injection present = %v, want %v", ok, tc.wantInject)
			}
			if tc.wantInject {
				msg, _ := findSystemMessageByPrefix(messages, "SKILL INSTRUCTIONS (v1)")
				if !contains(msg.Content, "code-review-style") {
					t.Errorf("injected content should contain skill name, got %q", msg.Content)
				}
				if !contains(msg.Content, "SKILL BODY") {
					t.Errorf("injected content should contain skill body, got %q", msg.Content)
				}
			}
		})
	}
}

func TestContextAssembler_Build_SkillsNilChangesNothing(t *testing.T) {
	ctx := context.Background()
	assembler := NewContextAssembler(tools.New(nil, "", nil), &contextMockStore{
		session: &store.Session{ID: "session-1", Metadata: map[string]any{"v1_skills": true}},
	}, 10)
	// V1Deps with nil Skills should not inject and not error
	assembler.SetV1Deps(V1Deps{})
	messages, err := assembler.Build(ctx, "session-1", "any message")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if _, ok := findSystemMessageByPrefix(messages, "SKILL INSTRUCTIONS (v1)"); ok {
		t.Error("nil Skills should not inject")
	}
}
