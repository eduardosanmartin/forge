// Package agent implements the forge agent loop with stable context prefix
// layout, tool-calling orchestration, and base metrics.
package agent

import (
	"testing"
	"time"
)

func TestColdStartMs_NonZero(t *testing.T) {
	// Cold start time should be non-zero after package initialization
	ms := ColdStartMs()
	if ms <= 0 {
		t.Errorf("ColdStartMs() = %d, want > 0", ms)
	}
}

func TestColdStartMs_IncreasesOverTime(t *testing.T) {
	ms1 := ColdStartMs()
	time.Sleep(10 * time.Millisecond)
	ms2 := ColdStartMs()
	if ms2 <= ms1 {
		t.Errorf("ColdStartMs() did not increase: %d -> %d", ms1, ms2)
	}
}

func TestTurnMetrics_DurationMs(t *testing.T) {
	start := time.Now()
	end := start.Add(100 * time.Millisecond)
	m := TurnMetrics{
		StartTime: start,
		EndTime:   end,
	}
	duration := m.DurationMs()
	if duration < 90 || duration > 110 {
		t.Errorf("DurationMs() = %d, want ~100", duration)
	}
}
