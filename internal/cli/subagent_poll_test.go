package cli

import (
	"strings"
	"testing"

	"github.com/eduardosanmartin/forge/internal/daemon"
)

func TestSubagentSnapshot_FiltersByParentAndSubagentFlag(t *testing.T) {
	sessions := []daemon.SessionResult{
		{ID: "child-1", MessageCount: 3, Metadata: map[string]any{"subagent": true, "subagent_parent": "parent-1"}},
		{ID: "child-2", MessageCount: 5, Metadata: map[string]any{"subagent": true, "subagent_parent": "parent-1"}},
		{ID: "other-parent-child", MessageCount: 9, Metadata: map[string]any{"subagent": true, "subagent_parent": "parent-2"}},
		{ID: "not-a-subagent", MessageCount: 1, Metadata: map[string]any{"subagent": false}},
		{ID: "no-metadata", MessageCount: 1},
		{ID: "self", MessageCount: 20, Metadata: map[string]any{"run_id": "wordstat-01"}},
	}

	got := subagentSnapshot(sessions, "parent-1")
	want := map[string]int{"child-1": 3, "child-2": 5}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for id, msgs := range want {
		if got[id] != msgs {
			t.Errorf("snapshot[%s] = %d, want %d", id, got[id], msgs)
		}
	}
}

func TestFormatSubagentSnapshot(t *testing.T) {
	tests := []struct {
		name        string
		prev, curr  map[string]int
		wantChanged bool
		wantHas     []string
	}{
		{
			name:        "no subagents at all stays silent",
			prev:        nil,
			curr:        map[string]int{},
			wantChanged: false,
		},
		{
			name:        "first subagent appearing is reported",
			prev:        map[string]int{},
			curr:        map[string]int{"child-1": 1},
			wantChanged: true,
			wantHas:     []string{"1 activo(s)", "child-1", "1 mensajes"},
		},
		{
			name:        "unchanged message counts are silent",
			prev:        map[string]int{"child-1": 4},
			curr:        map[string]int{"child-1": 4},
			wantChanged: false,
		},
		{
			name:        "message count moving is reported",
			prev:        map[string]int{"child-1": 4},
			curr:        map[string]int{"child-1": 7},
			wantChanged: true,
			wantHas:     []string{"7 mensajes"},
		},
		{
			name:        "a second subagent appearing is reported",
			prev:        map[string]int{"child-1": 4},
			curr:        map[string]int{"child-1": 4, "child-2": 1},
			wantChanged: true,
			wantHas:     []string{"2 activo(s)"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			msg, changed := formatSubagentSnapshot(tc.prev, tc.curr)
			if changed != tc.wantChanged {
				t.Fatalf("changed = %v, want %v (msg=%q)", changed, tc.wantChanged, msg)
			}
			for _, want := range tc.wantHas {
				if !strings.Contains(msg, want) {
					t.Errorf("message missing %q\ngot: %s", want, msg)
				}
			}
		})
	}
}
