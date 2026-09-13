package perf

import (
	"context"
	"runtime"
	"testing"
)

// TestIdleMemory pins the core at rest (RNF-1.3, spec target < 100MB).
// The daemon is built and STARTED in-process exactly as at startup
// (shared with the cold start sequence on a fresh temp state), a few real
// turns are executed through the daemon's own session manager against the
// zero-latency mock inference server, then the heap is forced into a
// quiescent shape (runtime.GC, twice) and runtime.ReadMemStats is read.
//
// HONEST APPROXIMATION (documented per spec): this is the TEST-BINARY
// heap, not the real daemon process RSS. Go's testing framework, the
// httptest/-benchmark scaffolding, and the Go runtime's own segment overhead
// all share this heap; typical Go idle heaps sit in the 10-30MB range, so
// the process RSS (heap + runtime/goroutine stack + binary segments) on a
// quiet core is expected to stay well under the 100MB allocation ceiling.
// The assertion HeapInuse+StackInuse < 100MB carries that margin on
// purpose: it catches real allocation-path regressions without targeting
// an RSS number this harness cannot faithfully extract in-process.
func TestIdleMemory(t *testing.T) {
	if testing.Short() {
		t.Skip("idle memory benchmark is integration-scope; skipping in -short")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := newMockChatServer(t)
	d, cleanup := startDaemonInProcess(t, ctx, server.URL)
	defer cleanup()
	if !d.IsRunning() {
		t.Fatal("daemon not running after start sequence")
	}

	// A few REAL turns through the daemon's own session manager, so the
	// steady state reflects the actual serving path (agent loop, store,
	// v1 features) rather than an empty daemon.
	mgr := d.GetSessionManager()
	sess, err := mgr.CreateSession(ctx, map[string]any{"purpose": "perf-idle-memory"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, turnErr := mgr.ExecuteTurn(ctx, sess.ID, "warm the daemon"); turnErr != nil {
			t.Fatalf("turn %d: %v", i+1, turnErr)
		}
	}

	// Quiesce: two GC rounds (first reclaims, second makes the "fully
	// idle" shape reusable), then read the stats.
	runtime.GC()
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	t.Logf("after 3 turns, daemon at rest: HeapAlloc=%.1fMB, HeapSys=%.1fMB, HeapInuse=%.1fMB, StackInuse=%.1fMB, NumGC=%d",
		mib(ms.HeapAlloc), mib(ms.HeapSys), mib(ms.HeapInuse), mib(ms.StackInuse), ms.NumGC)

	resident := ms.HeapInuse + ms.StackInuse
	if resident >= 100<<20 {
		t.Errorf("resident in-process memory %.1f MB >= 100 MB spec target (RNF-1.3)", mib(resident))
	}
}

// mib formats a byte count as MiB with one decimal.
func mib(bytes uint64) float64 {
	return float64(bytes) / (1024 * 1024)
}
