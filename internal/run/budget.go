package run

import (
	"fmt"
	"time"
)

// BudgetState tracks hard-wall consumption for one run (RNF-8).
// It is created at runner start and updated after each task turn via
// TurnMetrics so the runner can kill the run without doing another LLM call.
type BudgetState struct {
	StartedAt      time.Time
	MaxWallClock   time.Duration // 0 = unlimited
	MaxTokens      int           // 0 = unlimited
	MaxIterations  int           // 0 = unlimited
	TokensUsed     int
	IterationsUsed int
}

// NewBudgetState creates a BudgetState from a manifest.
func NewBudgetState(m *Manifest, startedAt time.Time) BudgetState {
	return BudgetState{
		StartedAt:     startedAt,
		MaxWallClock:  m.WallClockDuration(),
		MaxTokens:     m.Budget.MaxTokens,
		MaxIterations: m.Budget.MaxIterations,
	}
}

// Elapsed returns wall-clock elapsed since StartedAt.
func (b BudgetState) Elapsed(now time.Time) time.Duration {
	return now.Sub(b.StartedAt)
}

// RemainingWallClock returns the remaining wall-clock budget, or 0 for unlimited.
func (b BudgetState) RemainingWallClock(now time.Time) time.Duration {
	if b.MaxWallClock == 0 {
		return 0
	}
	remaining := b.MaxWallClock - b.Elapsed(now)
	if remaining < 0 {
		return 0
	}
	return remaining
}

// Check returns an error when any hard wall has been exceeded (budget exhausted).
// It is called BEFORE starting the next task so the run is killed, not warned.
func (b BudgetState) Check(now time.Time) error {
	if b.MaxWallClock > 0 && b.Elapsed(now) > b.MaxWallClock {
		return fmt.Errorf("budget exceeded: wall-clock %s > max %s — RNF-8 kill", b.Elapsed(now).Truncate(time.Millisecond), b.MaxWallClock)
	}
	if b.MaxTokens > 0 && b.TokensUsed > b.MaxTokens {
		return fmt.Errorf("budget exceeded: tokens %d > max %d — RNF-8 kill", b.TokensUsed, b.MaxTokens)
	}
	if b.MaxIterations > 0 && b.IterationsUsed > b.MaxIterations {
		return fmt.Errorf("budget exceeded: iterations %d > max %d — RNF-8 kill", b.IterationsUsed, b.MaxIterations)
	}
	return nil
}

// CheckThreshold reports which budget fraction has crossed any budget_threshold checkpoint.
// Returns the first matching checkpoint id or empty. This is lightweight: it does not
// kill the run when the trigger is not required; callers decide pause vs continue.
func (b BudgetState) CheckThreshold(now time.Time, checkpoints []Checkpoint) string {
	for _, cp := range checkpoints {
		if cp.Trigger != TriggerBudgetThreshold {
			continue
		}
		frac := b.fractionUsed(now)
		if frac >= cp.Threshold {
			return cp.ID
		}
	}
	return ""
}

func (b BudgetState) fractionUsed(now time.Time) float64 {
	maxFrac := 0.0
	if b.MaxWallClock > 0 {
		frac := float64(b.Elapsed(now)) / float64(b.MaxWallClock)
		if frac > maxFrac {
			maxFrac = frac
		}
	}
	if b.MaxTokens > 0 && b.MaxTokens != 0 {
		frac := float64(b.TokensUsed) / float64(b.MaxTokens)
		if frac > maxFrac {
			maxFrac = frac
		}
	}
	if b.MaxIterations > 0 && b.MaxIterations != 0 {
		frac := float64(b.IterationsUsed) / float64(b.MaxIterations)
		if frac > maxFrac {
			maxFrac = frac
		}
	}
	return maxFrac
}

// AddTurn accumulates tokens/iterations from one completed agent turn.
func (b *BudgetState) AddTurn(tokens, iterations int) {
	b.TokensUsed += tokens
	b.IterationsUsed += iterations
}
