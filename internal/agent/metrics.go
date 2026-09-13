// Package agent implements the forge agent loop with stable context prefix
// layout, tool-calling orchestration, and base metrics.
package agent

import (
	"sync/atomic"
	"time"
)

// coldStartTime is set at package initialization.
var coldStartTime = time.Now()

// ColdStartMs returns the time elapsed since package initialization in milliseconds.
func ColdStartMs() int64 {
	return time.Since(coldStartTime).Milliseconds()
}

// TTFT holds last observed time-to-first-token for streaming turns.
// It is set atomically when the first delta token arrives; 0 means not yet observed.
var lastTTFTMs atomic.Int64

// RecordTTFT stores the TTFT for the current streaming turn.
func RecordTTFT(ms int64) { lastTTFTMs.Store(ms) }

// LastTTFTMs returns the last recorded TTFT in milliseconds (0 if none).
func LastTTFTMs() int64 { return lastTTFTMs.Load() }
