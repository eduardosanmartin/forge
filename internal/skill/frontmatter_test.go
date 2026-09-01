package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSkillFile_Acceptance(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    Skill
	}{
		{
			name: "full shape with all fields",
			content: "---\n" +
				"name: code-review-style\n" +
				"description: \"Provides guidance for code reviews\"\n" +
				"category: review\n" +
				"source: local\n" +
				"activation_keywords: [\"code review\", \"style\", \"PR\"]\n" +
				"scripts: [\"scripts/check-style.sh\"]\n" +
				"---\n" +
				"# Instructions\nBody line\n",
			want: Skill{
				Name:               "code-review-style",
				Description:        "Provides guidance for code reviews",
				Category:           "review",
				Source:             SourceLocal,
				ActivationKeywords: []string{"code review", "style", "PR"},
				Scripts:            []string{"scripts/check-style.sh"},
				Instructions:       "# Instructions\nBody line\n",
			},
		},
		{
			name: "minimal required fields",
			content: "---\n" +
				"name: my-skill\n" +
				"description: \"A minimal skill\"\n" +
				"source: local\n" +
				"---\n" +
				"Body\n",
			want: Skill{
				Name:        "my-skill",
				Description: "A minimal skill",
				Source:      SourceLocal,
				Instructions: "Body\n",
			},
		},
		{
			name: "comments and unquoted strings",
			content: "---\n" +
				"# comment line\n" +
				"name: my-skill # trailing comment\n" +
				"description: A minimal skill without quotes\n" +
				"source: local\n" +
				"---\n" +
				"Hello\n",
			want: Skill{
				Name:        "my-skill",
				Description: "A minimal skill without quotes",
				Source:      SourceLocal,
				Instructions: "Hello\n",
			},
		},
		{
			name: "inline arrays with spaces and single quotes",
			content: "---\n" +
				"name: my-skill\n" +
				"description: \"desc\"\n" +
				"source: local\n" +
				"activation_keywords: ['a', 'b']\n" +
				"scripts: [\"scripts/a.sh\", \"scripts/b.sh\"]\n" +
				"---\n" +
				"Body\n",
			want: Skill{
				Name:               "my-skill",
				Description:        "desc",
				Source:             SourceLocal,
				ActivationKeywords: []string{"a", "b"},
				Scripts:            []string{"scripts/a.sh", "scripts/b.sh"},
				Instructions:       "Body\n",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sk, err := parseSkillFile([]byte(tc.content), "/tmp/my-skill")
			if err != nil {
				t.Fatalf("parse error: %v", err)
			}
			if sk.Name != tc.want.Name {
				t.Errorf("Name = %q want %q", sk.Name, tc.want.Name)
			}
			if sk.Description != tc.want.Description {
				t.Errorf("Description = %q want %q", sk.Description, tc.want.Description)
			}
			if sk.Category != tc.want.Category {
				t.Errorf("Category = %q want %q", sk.Category, tc.want.Category)
			}
			if sk.Source != tc.want.Source {
				t.Errorf("Source = %q want %q", sk.Source, tc.want.Source)
			}
			if !equalStringSlices(sk.ActivationKeywords, tc.want.ActivationKeywords) {
				t.Errorf("ActivationKeywords = %v want %v", sk.ActivationKeywords, tc.want.ActivationKeywords)
			}
			if !equalStringSlices(sk.Scripts, tc.want.Scripts) {
				t.Errorf("Scripts = %v want %v", sk.Scripts, tc.want.Scripts)
			}
			if sk.Instructions != tc.want.Instructions {
				t.Errorf("Instructions = %q want %q", sk.Instructions, tc.want.Instructions)
			}
		})
	}
}

func TestParseSkillFile_Rejections(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantSub string
	}{
		{
			name:    "missing fences",
			content: "name: foo\n",
			wantSub: "missing opening frontmatter fence",
		},
		{
			name:    "BOM",
			content: "\xEF\xBB\xBF---\nname: foo\n---\n",
			wantSub: "unexpected UTF-8 BOM",
		},
		{
			name: "unknown key",
			content: "---\nname: foo\ndescription: \"desc\"\nsource: local\nunknown: bar\n---\n",
			wantSub: "unknown field",
		},
		{
			name: "nested mapping",
			content: "---\nname: foo\ndescription: \"desc\"\nsource: local\nmetadata:\n  author: x\n---\n",
			wantSub: "unknown field",
		},
		{
			name: "block scalar pipe",
			content: "---\nname: foo\ndescription: |\n  block\nsource: local\n---\n",
			wantSub: "block scalars are not supported",
		},
		{
			name: "tab indentation",
			content: "---\nname: foo\ndescription: \"desc\"\nsource: local\n\tcategory: review\n---\n",
			wantSub: "tabs are not allowed",
		},
		{
			name: "bad checksum format on local not relevant to parse but checksum field present with invalid format still parsed",
			content: "---\nname: foo\ndescription: \"desc\"\nsource: local\nchecksum: \"sha256:zzzz\"\n---\n",
			wantSub: "", // parsing succeeds, validation will fail later; so no parse error expected
		},
		{
			name: "checksum on local passes parse but validate will fail",
			content: "---\nname: foo\ndescription: \"desc\"\nsource: local\nchecksum: \"sha256:abc\"\n---\n",
			wantSub: "",
		},
		{
			name: "anchor",
			content: "---\nname: foo\ndescription: \"desc\"\nsource: local\nactivation_keywords: &anchor [\"a\"]\n---\n",
			wantSub: "anchors and aliases are not supported",
		},
		{
			name: "missing closing fence",
			content: "---\nname: foo\ndescription: \"desc\"\nsource: local\n",
			wantSub: "missing closing frontmatter fence",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.wantSub == "" {
				// Expect no parse error (validation may still fail)
				_, err := parseSkillFile([]byte(tc.content), "/tmp/foo")
				if err != nil {
					t.Fatalf("expected parse success but got %v", err)
				}
				return
			}
			_, err := parseSkillFile([]byte(tc.content), "/tmp/foo")
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantSub)
			}
			// Check line number present
			if !strings.Contains(err.Error(), "skill line") {
				t.Errorf("error %q should contain line number", err.Error())
			}
		})
	}
}

func TestParseSkillFile_FromTestdata(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "code-review-style", "SKILL.md"))
	if err != nil {
		t.Fatalf("read testdata: %v", err)
	}
	sk, err := parseSkillFile(data, "testdata/code-review-style")
	if err != nil {
		t.Fatalf("parse testdata: %v", err)
	}
	if sk.Name != "code-review-style" {
		t.Errorf("Name = %q", sk.Name)
	}
	if sk.Description == "" {
		t.Error("description empty")
	}
	if len(sk.ActivationKeywords) == 0 {
		t.Error("activation_keywords empty")
	}
	if len(sk.Scripts) == 0 {
		t.Error("scripts empty")
	}
	if sk.Instructions == "" {
		t.Error("instructions empty")
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
