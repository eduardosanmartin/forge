// Package agent implements the forge agent loop with stable context prefix
// layout, tool-calling orchestration, and base metrics.
package agent

import (
	"time"
)

// coldStartTime is set at package initialization.
var coldStartTime = time.Now()

// ColdStartMs returns the time elapsed since package initialization in milliseconds.
func ColdStartMs() int64 {
	return time.Since(coldStartTime).Milliseconds()
}
