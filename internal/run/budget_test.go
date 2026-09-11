package run

import (
	"testing"
	"time"
)

func TestBudgetCheckWallClockKill(t *testing.T) {
	m := Manifest{RunID: "r", Mode: "supervised", Goal: "g", Budget: Budget{MaxWallClock: "1h", MaxTokens: 100, MaxIterations: 10}}
	start := time.Now().Add(-2 * time.Hour)
	b := NewBudgetState(&m, start)
	if err := b.Check(time.Now()); err == nil {
		t.Fatal("expected wall-clock exceeded")
	}
}

func TestBudgetCheckTokensKill(t *testing.T) {
	m := Manifest{RunID: "r", Mode: "supervised", Goal: "g", Budget: Budget{MaxTokens: 10}}
	b := NewBudgetState(&m, time.Now())
	b.TokensUsed = 11
	if err := b.Check(time.Now()); err == nil {
		t.Fatal("expected token exceeded")
	}
}

func TestBudgetCheckIterationsKill(t *testing.T) {
	m := Manifest{RunID: "r", Mode: "supervised", Goal: "g", Budget: Budget{MaxIterations: 5}}
	b := NewBudgetState(&m, time.Now())
	b.IterationsUsed = 6
	if err := b.Check(time.Now()); err == nil {
		t.Fatal("expected iteration exceeded")
	}
}

func TestBudgetUnlimitedNoKill(t *testing.T) {
	m := Manifest{RunID: "r", Mode: "supervised", Goal: "g"}
	b := NewBudgetState(&m, time.Now())
	b.TokensUsed = 1_000_000
	b.IterationsUsed = 1_000_000
	if err := b.Check(time.Now()); err != nil {
		t.Fatalf("unlimited should not kill: %v", err)
	}
}

func TestBudgetThresholdFraction(t *testing.T) {
	m := Manifest{RunID: "r", Mode: "supervised", Goal: "g", Budget: Budget{MaxTokens: 100}}
	b := NewBudgetState(&m, time.Now())
	b.TokensUsed = 60
	cps := []Checkpoint{{ID: "half", Trigger: TriggerBudgetThreshold, Threshold: 0.5}}
	if got := b.CheckThreshold(time.Now(), cps); got != "half" {
		t.Fatalf("threshold hit got %q want half", got)
	}
	cps2 := []Checkpoint{{ID: "high", Trigger: TriggerBudgetThreshold, Threshold: 0.9}}
	if got := b.CheckThreshold(time.Now(), cps2); got != "" {
		t.Fatalf("should not hit 0.9, got %q", got)
	}
}

func TestBudgetAddTurn(t *testing.T) {
	m := Manifest{RunID: "r", Mode: "supervised", Goal: "g"}
	b := NewBudgetState(&m, time.Now())
	b.AddTurn(5, 2)
	if b.TokensUsed != 5 || b.IterationsUsed != 2 {
		t.Fatalf("add turn failed: %+v", b)
	}
}
