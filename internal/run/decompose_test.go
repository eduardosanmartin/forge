package run

import (
	"strings"
	"testing"
)

func TestBuildDecompositionPromptIncludesGoalAndSpec(t *testing.T) {
	p := BuildDecompositionPrompt("implement feature X", "SPEC: requirement details")
	if !strings.Contains(p, "implement feature X") {
		t.Errorf("prompt missing goal: %s", p)
	}
	if !strings.Contains(p, "SPEC: requirement details") {
		t.Errorf("prompt missing spec: %s", p)
	}
	if !strings.Contains(p, "JSON array") {
		t.Errorf("prompt missing JSON-only instruction: %s", p)
	}
}

func TestBuildDecompositionPromptOmitsEmptySpec(t *testing.T) {
	p := BuildDecompositionPrompt("goal only", "")
	if strings.Contains(p, "SPEC:") {
		t.Errorf("prompt should omit SPEC section when spec is empty: %s", p)
	}
}

func TestParseDecomposedTasks_CleanJSON(t *testing.T) {
	raw := `[{"id":"t1","goal":"do a thing","done_criteria":"cmd: go build ./...","file_budget":"internal/foo","model_hint":"cheap"}]`
	tasks, err := ParseDecomposedTasks(raw)
	if err != nil {
		t.Fatalf("ParseDecomposedTasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("len(tasks) = %d, want 1", len(tasks))
	}
	got := tasks[0]
	want := Task{ID: "t1", Goal: "do a thing", DoneCriteria: "cmd: go build ./...", FileBudget: "internal/foo", ModelHint: "cheap"}
	if got != want {
		t.Errorf("task = %+v, want %+v", got, want)
	}
}

func TestParseDecomposedTasks_CodeFenceWrapped(t *testing.T) {
	raw := "Here's the plan:\n```json\n[{\"id\":\"t1\",\"goal\":\"do a thing\"}]\n```"
	tasks, err := ParseDecomposedTasks(raw)
	if err != nil {
		t.Fatalf("ParseDecomposedTasks: %v", err)
	}
	if len(tasks) != 1 || tasks[0].ID != "t1" {
		t.Fatalf("tasks = %+v", tasks)
	}
}

func TestParseDecomposedTasks_EmbeddedInProse(t *testing.T) {
	raw := `Sure, here is the breakdown: [{"id":"t1","goal":"do a thing"},{"id":"t2","goal":"do another"}] Let me know if you need changes.`
	tasks, err := ParseDecomposedTasks(raw)
	if err != nil {
		t.Fatalf("ParseDecomposedTasks: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("len(tasks) = %d, want 2", len(tasks))
	}
}

func TestParseDecomposedTasks_MultipleTasks(t *testing.T) {
	raw := `[{"id":"t1","goal":"first"},{"id":"t2","goal":"second"},{"id":"t3","goal":"third"}]`
	tasks, err := ParseDecomposedTasks(raw)
	if err != nil {
		t.Fatalf("ParseDecomposedTasks: %v", err)
	}
	if len(tasks) != 3 {
		t.Fatalf("len(tasks) = %d, want 3", len(tasks))
	}
}

// TestParseDecomposedTasks_TrailingProseWithStrayBracket reproduces a real
// failure observed against a live decomposition call: the model answered
// with a valid JSON array followed by trailing commentary that itself
// contained a ']' (e.g. an "arr[0]" aside), which a naive "last ']' in the
// whole text" extraction would have folded into the "JSON", breaking
// json.Unmarshal with "invalid character after top-level value".
func TestParseDecomposedTasks_TrailingProseWithStrayBracket(t *testing.T) {
	raw := "[{\"id\":\"t1\",\"goal\":\"do a thing\"}]\n\nNote: verify with something like `arr[0]` afterwards."
	tasks, err := ParseDecomposedTasks(raw)
	if err != nil {
		t.Fatalf("ParseDecomposedTasks: %v", err)
	}
	if len(tasks) != 1 || tasks[0].ID != "t1" {
		t.Fatalf("tasks = %+v", tasks)
	}
}

// TestParseDecomposedTasks_NestedObjectsWithBracketsInStrings covers a task
// list whose string fields themselves contain brackets, to confirm the
// bracket-depth scan isn't confused by them.
func TestParseDecomposedTasks_NestedObjectsWithBracketsInStrings(t *testing.T) {
	raw := `[{"id":"t1","goal":"index into arr[0] and check result","done_criteria":"cmd: go test ./..."}]`
	tasks, err := ParseDecomposedTasks(raw)
	if err != nil {
		t.Fatalf("ParseDecomposedTasks: %v", err)
	}
	if len(tasks) != 1 || !strings.Contains(tasks[0].Goal, "arr[0]") {
		t.Fatalf("tasks = %+v", tasks)
	}
}

func TestParseDecomposedTasks_NoJSONErrors(t *testing.T) {
	_, err := ParseDecomposedTasks("I refuse to answer in JSON.")
	if err == nil {
		t.Fatal("expected an error for non-JSON response")
	}
}

func TestParseDecomposedTasks_EmptyArrayErrors(t *testing.T) {
	_, err := ParseDecomposedTasks("[]")
	if err == nil {
		t.Fatal("expected an error for an empty task list")
	}
}
