// Package perf hosts the RNF-1.1–1.3 performance benchmarks for forge's
// daemon/core: cold start time (RNF-1.1, target < 200ms), per-turn harness
// overhead (RNF-1.2, target < 50ms excluding LLM inference), and idle core
// memory (RNF-1.3, target < 100MB).
//
// The numbers are REPORTED through t.Logf for trend visibility, and each
// assertion carries a documented safety margin above the spec target so the
// package stays green under ordinary CI load while still catching real
// regressions. Benchmarks construct the real daemon/core in-process on
// fresh t.TempDir() working sets and, for the turn-overhead path, exercise
// the real LLM registry/provider code against a zero-latency local
// httptest server pretending to be an inference server.
//
// All benchmarks are integration-scope and skip under -short.
package perf
