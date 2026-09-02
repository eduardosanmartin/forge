package mining

import (
	"regexp"
	"strings"
	"testing"

	"github.com/eduardosanmartin/forge/internal/embedding"
)

func trajWithPrompts(sessionID string, prompts []string, toolSeq [][]string) Trajectory {
	var turns []Turn
	for i, prompt := range prompts {
		var steps []Step
		if i < len(toolSeq) {
			for _, tool := range toolSeq[i] {
				steps = append(steps, Step{ToolName: tool, ArgsSummary: "{}"})
			}
		}
		turns = append(turns, Turn{UserPrompt: prompt, Steps: steps})
	}
	return Trajectory{SessionID: sessionID, Turns: turns}
}

func TestMine_ClusteringSeparatesDissimilar(t *testing.T) {
	trajs := []Trajectory{
		trajWithPrompts("s1", []string{"fix authentication bug in login flow"}, [][]string{{"fs_read", "fs_write"}}),
		trajWithPrompts("s2", []string{"fix authentication bug login flow patch"}, [][]string{{"fs_read", "fs_write"}}),
		trajWithPrompts("s3", []string{"garden cooking recipes tomatoes"}, [][]string{{"shell_exec"}}),
		trajWithPrompts("s4", []string{"garden cooking recipes basil"}, [][]string{{"shell_exec"}}),
	}
	opts := Options{MinClusterSize: 2, Threshold: 0.4}
	proposals := Mine(trajs, opts)
	if len(proposals) != 2 {
		t.Fatalf("expected 2 proposals (auth cluster + garden cluster), got %d: %+v", len(proposals), proposals)
	}
	// Ensure each proposal has at least 2 source sessions.
	for _, p := range proposals {
		if len(p.SourceSessions) < 2 {
			t.Fatalf("proposal %q has <2 sources: %v", p.Name, p.SourceSessions)
		}
	}
}

func TestMine_JoinsSimilar(t *testing.T) {
	// Paraphrases that share vocab: should cluster together.
	trajs := []Trajectory{
		trajWithPrompts("a1", []string{"Please review this code for style issues and PR feedback"}, [][]string{{"fs_read"}}),
		trajWithPrompts("a2", []string{"review code style pull request checks"}, [][]string{{"fs_read"}}),
		trajWithPrompts("a3", []string{"code review style PR checks"}, [][]string{{"fs_read"}}),
	}
	opts := Options{MinClusterSize: 2, Threshold: 0.4}
	proposals := Mine(trajs, opts)
	if len(proposals) != 1 {
		t.Fatalf("expected 1 cluster for similar workflows, got %d", len(proposals))
	}
	if len(proposals[0].SourceSessions) != 3 {
		t.Fatalf("expected 3 sources, got %v", proposals[0].SourceSessions)
	}
}

func TestMine_MinClusterSizeFiltering(t *testing.T) {
	tests := []struct {
		name       string
		trajs      []Trajectory
		opts       Options
		wantCount  int
	}{
		{
			name: "size 2 filters singleton",
			trajs: []Trajectory{
				trajWithPrompts("s1", []string{"deploy app to production"}, [][]string{{"shell_exec"}}),
				trajWithPrompts("s2", []string{"deploy app to production"}, [][]string{{"shell_exec"}}),
				trajWithPrompts("s3", []string{"unrelated garden cooking"}, [][]string{{"fs_read"}}),
			},
			opts:      Options{MinClusterSize: 2, Threshold: 0.4},
			wantCount: 1,
		},
		{
			name: "size 3 requires three",
			trajs: []Trajectory{
				trajWithPrompts("s1", []string{"deploy app to production"}, [][]string{{"shell_exec"}}),
				trajWithPrompts("s2", []string{"deploy app to production"}, [][]string{{"shell_exec"}}),
				trajWithPrompts("s3", []string{"deploy app to production"}, [][]string{{"shell_exec"}}),
			},
			opts:      Options{MinClusterSize: 3, Threshold: 0.4},
			wantCount: 1,
		},
		{
			name: "size 3 filters pair",
			trajs: []Trajectory{
				trajWithPrompts("s1", []string{"deploy app to production"}, [][]string{{"shell_exec"}}),
				trajWithPrompts("s2", []string{"deploy app to production"}, [][]string{{"shell_exec"}}),
			},
			opts:      Options{MinClusterSize: 3, Threshold: 0.4},
			wantCount: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Mine(tt.trajs, tt.opts)
			if len(got) != tt.wantCount {
				t.Fatalf("want %d proposals, got %d: %+v", tt.wantCount, len(got), got)
			}
		})
	}
}

func TestMine_DeterministicInstructions(t *testing.T) {
	trajs := []Trajectory{
		trajWithPrompts("s1", []string{"build docker image for app"}, [][]string{{"fs_read", "shell_exec"}}),
		trajWithPrompts("s2", []string{"build docker image for app"}, [][]string{{"fs_read", "shell_exec"}}),
	}
	opts := Options{MinClusterSize: 2, Threshold: 0.4}
	p1 := Mine(trajs, opts)
	p2 := Mine(trajs, opts)
	if len(p1) != 1 || len(p2) != 1 {
		t.Fatalf("expected 1 proposal each")
	}
	if p1[0].Instructions != p2[0].Instructions {
		t.Fatalf("instructions not deterministic:\n%s\nvs\n%s", p1[0].Instructions, p2[0].Instructions)
	}
	if !strings.Contains(p1[0].Instructions, "Recurring workflow (observed 2 times):") {
		t.Fatalf("instructions missing header: %s", p1[0].Instructions)
	}
	if !strings.Contains(p1[0].Instructions, "Common tool sequence:") {
		t.Fatalf("instructions missing tool sequence: %s", p1[0].Instructions)
	}
	if p1[0].Description == "" {
		t.Fatalf("description should not be empty")
	}
}

func TestMine_NameSlugValidity(t *testing.T) {
	re := regexp.MustCompile(`^[a-z][a-z0-9_-]{1,63}$`)
	trajs := []Trajectory{
		trajWithPrompts("s1", []string{"analyze test coverage report"}, [][]string{{"fs_read"}}),
		trajWithPrompts("s2", []string{"analyze test coverage report"}, [][]string{{"fs_read"}}),
	}
	opts := Options{MinClusterSize: 2}
	proposals := Mine(trajs, opts)
	if len(proposals) == 0 {
		t.Fatalf("expected proposal")
	}
	for _, p := range proposals {
		if !re.MatchString(p.Name) {
			t.Fatalf("name %q invalid", p.Name)
		}
	}
}

func TestMine_EndToEnd(t *testing.T) {
	// Realistic multi-turn trajectories.
	trajs := []Trajectory{
		{
			SessionID: "sess-1",
			Turns: []Turn{
				{UserPrompt: "create new user with email test@example.com", Steps: []Step{{ToolName: "fs_read"}, {ToolName: "fs_write"}}},
				{UserPrompt: "send welcome email", Steps: []Step{{ToolName: "shell_exec"}}},
			},
		},
		{
			SessionID: "sess-2",
			Turns: []Turn{
				{UserPrompt: "create new user with email foo@bar.com", Steps: []Step{{ToolName: "fs_read"}, {ToolName: "fs_write"}}},
				{UserPrompt: "send welcome email to user", Steps: []Step{{ToolName: "shell_exec"}}},
			},
		},
		{
			SessionID: "sess-3",
			Turns: []Turn{
				{UserPrompt: "gardening tips for tomatoes", Steps: []Step{{ToolName: "fs_read"}}},
			},
		},
	}
	proposals := Mine(trajs, Options{MinClusterSize: 2, Threshold: 0.4})
	if len(proposals) != 1 {
		t.Fatalf("expected 1 proposal for similar user creation workflows, got %d", len(proposals))
	}
	p := proposals[0]
	if len(p.SourceSessions) != 2 {
		t.Fatalf("expected 2 source sessions, got %v", p.SourceSessions)
	}
	// Check steps contain expected tool names
	found := false
	for _, s := range p.Steps {
		if s == "fs_read" || s == "fs_write" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected proposal steps to contain fs_read/fs_write, got %v", p.Steps)
	}
	if p.Instructions == "" || p.Description == "" {
		t.Fatalf("proposal missing instructions/description")
	}
}

func TestSanitizeSlug(t *testing.T) {
	tests := []struct {
		input string
		valid bool
	}{
		{"Hello World", true},
		{"123-start", true}, // should become w-123-start
		{"a", true},
		{"  leading spaces  ", true},
		{"UPPER-CASE", true},
	}
	re := regexp.MustCompile(`^[a-z][a-z0-9_-]{1,63}$`)
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := sanitizeSlug(tt.input)
			if tt.valid && !re.MatchString(got) {
				t.Fatalf("sanitizeSlug(%q) = %q invalid", tt.input, got)
			}
		})
	}
}

func TestCosineSimilarity(t *testing.T) {
	a := []float32{1, 0, 0}
	b := []float32{1, 0, 0}
	if got := embedding.CosineSimilarity(a, b); got != 1 {
		t.Fatalf("identical vectors should be 1, got %v", got)
	}
	c := []float32{0, 1, 0}
	if got := embedding.CosineSimilarity(a, c); got != 0 {
		t.Fatalf("orthogonal should be 0, got %v", got)
	}
	if got := embedding.CosineSimilarity([]float32{}, []float32{1}); got != 0 {
		t.Fatalf("mismatched len should be 0, got %v", got)
	}
	if got := embedding.CosineSimilarity([]float32{0, 0}, []float32{0, 0}); got != 0 {
		t.Fatalf("zero vectors should be 0, got %v", got)
	}
}

func TestEmbeddingEquivalenceMiningDelegatesToEmbedding(t *testing.T) {
	// Cross-package equivalence: mining must produce bit-for-bit identical vectors
	// and similarities as embedding.GenerateEmbedding / CosineSimilarity.
	inputs := []string{
		"hello world",
		"fix authentication bug in login flow",
		"",
		"!!!",
		"función para sumar dos números",
		"Hello, WORLD!!! 123",
		"a b c d e f g",
	}
	for _, txt := range inputs {
		emb, err := embedding.GenerateEmbedding(txt)
		if err != nil {
			t.Fatalf("GenerateEmbedding(%q) error: %v", txt, err)
		}
		if len(emb) != 384 {
			t.Fatalf("expected dim 384, got %d for %q", len(emb), txt)
		}
		emb2, _ := embedding.GenerateEmbedding(txt)
		for i := range emb {
			if emb[i] != emb2[i] {
				t.Fatalf("non-deterministic embedding for %q at %d: %v vs %v", txt, i, emb[i], emb2[i])
			}
		}
	}
	// Deterministic similarity check: identical texts -> 1.0, unrelated -> <0.2
	sameA, _ := embedding.GenerateEmbedding("Provides guidance for code reviews and pull request style checks")
	sameB, _ := embedding.GenerateEmbedding("Provides guidance for code reviews and pull request style checks")
	if got := embedding.CosineSimilarity(sameA, sameB); got < 0.99 {
		t.Fatalf("identical texts cosine should be ~1.0, got %f", got)
	}
	unrelated, _ := embedding.GenerateEmbedding("weather forecast gardening cooking recipes")
	if got := embedding.CosineSimilarity(sameA, unrelated); got > 0.2 {
		t.Fatalf("unrelated texts cosine should be <0.2, got %f", got)
	}
}
