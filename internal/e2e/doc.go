// Package e2e hosts forge's end-to-end verification suites.
//
// Two suites share the helpers in this package:
//
//   - The offline suite (e2e_offline_test.go) runs in the DEFAULT test suite.
//     It drives the complete in-process stack (transport, handler, session
//     manager, agent, permission engine, native tools, SQLite store) against
//     a scripted OpenAI-compatible mock server, so it needs no model.
//
//   - The live suite (e2e_live_test.go) is guarded by FORGE_E2E_LIVE=1 and
//     demonstrates the spec §6 v0 exit criterion against a real local model
//     served by Ollama.
package e2e
