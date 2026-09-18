package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/eduardosanmartin/forge/internal/run"
)

// TestPrintManifestProgress locks the wording of each progress-event line
// (internal/run.Runner.OnProgress -> printManifestProgress), the mechanism
// that replaces total terminal silence during a manifest run's task loop.
func TestPrintManifestProgress(t *testing.T) {
	tests := []struct {
		name    string
		ev      run.ProgressEvent
		wantHas []string
	}{
		{
			name: "task_start names the task and its position",
			ev:   run.ProgressEvent{Phase: run.ProgressTaskStart, TaskID: "t2-cli", TaskIndex: 2, TotalTasks: 3},
			wantHas: []string{"[2/3]", "t2-cli", "iniciando"},
		},
		{
			name: "task_retry shows the attempt count and the failure that triggered it",
			ev: run.ProgressEvent{
				Phase: run.ProgressTaskRetry, TaskID: "t2-cli", TaskIndex: 2, TotalTasks: 3,
				Attempt: 1, MaxRetries: 2, Err: errors.New("go build failed"),
			},
			wantHas: []string{"[2/3]", "t2-cli", "intento 2/3", "go build failed"},
		},
		{
			name: "task_done carries the cumulative budget, not per-task",
			ev: run.ProgressEvent{
				Phase: run.ProgressTaskDone, TaskID: "t2-cli", TaskIndex: 2, TotalTasks: 3,
				TokensUsed: 40094, IterationsUsed: 224,
			},
			wantHas: []string{"[2/3]", "t2-cli", "listo", "40094", "224"},
		},
		{
			name: "task_failed names the total attempts made and the final error",
			ev: run.ProgressEvent{
				Phase: run.ProgressTaskFailed, TaskID: "t2-cli", TaskIndex: 2, TotalTasks: 3,
				MaxRetries: 2, Err: errors.New("duplicate key"),
			},
			wantHas: []string{"[2/3]", "t2-cli", "falló tras 3 intentos", "duplicate key"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			printManifestProgress(&buf, tc.ev)
			out := buf.String()
			for _, want := range tc.wantHas {
				if !strings.Contains(out, want) {
					t.Errorf("output missing %q\ngot: %s", want, out)
				}
			}
		})
	}
}

// TestPrintManifestGuidance locks the wording printManifestGuidance prints
// for each terminal run status. Before this fix, every non-completed
// outcome — including a HITL checkpoint pause after every task succeeded —
// surfaced only as cobra's generic "Error: %s", making a normal, expected
// pause awaiting human approval look indistinguishable from a genuine
// failure.
func TestPrintManifestGuidance(t *testing.T) {
	tests := []struct {
		name      string
		rep       *run.Report
		wantHas   []string
		wantOmits []string
	}{
		{
			name: "completed prints nothing",
			rep:  &run.Report{Status: run.StatusCompleted},
		},
		{
			name: "genuine HITL pause reads as success, not error",
			rep: &run.Report{
				Status:            run.StatusPaused,
				CompletedTasks:    []string{"t1", "t2", "t3"},
				TotalTasks:        3,
				PausedCheckpoints: []string{"cp-before-merge"},
			},
			wantHas: []string{
				"Generación exitosa", "3/3 tareas completadas", "cp-before-merge",
				"--resume --yes",
			},
			wantOmits: []string{"Error:"},
		},
		{
			name: "retries-exhausted pause is flagged distinctly from a clean checkpoint, with the --yes gotcha spelled out",
			rep: &run.Report{
				Status:            run.StatusPaused,
				CompletedTasks:    []string{"t1"},
				TotalTasks:        3,
				PausedCheckpoints: []string{"implicit-retries-exhausted"},
			},
			wantHas: []string{
				"no se completó", "--resume\n",
				"CON --yes en este checkpoint la marca como fallida",
			},
			wantOmits: []string{"Generación exitosa", "Error:"},
		},
		{
			name: "killed is a hard wall, not resumable",
			rep:  &run.Report{Status: run.StatusKilled},
			wantHas: []string{
				"presupuesto agotado", "no es reanudable",
			},
		},
		{
			name:    "failed points at the detail above",
			rep:     &run.Report{Status: run.StatusFailed},
			wantHas: []string{"terminó en error"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			printManifestGuidance(&buf, tc.rep, "run.json")
			out := buf.String()
			for _, want := range tc.wantHas {
				if !strings.Contains(out, want) {
					t.Errorf("output missing %q\ngot: %s", want, out)
				}
			}
			for _, omit := range tc.wantOmits {
				if strings.Contains(out, omit) {
					t.Errorf("output should not contain %q\ngot: %s", omit, out)
				}
			}
		})
	}
}
