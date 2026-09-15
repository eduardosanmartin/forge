# Exit Verification — MVP v3 (formal close)

This document closes v3: the stabilization + TUI M2 + provider-plugin + streaming
amendment milestone. It follows the structure of `docs/EXIT-VERIFICATION-v2.md`
and maps each v3 exit criterion to the artifact/command that proves it, the
known limitations that remain, and the deferred scope for phase 2.

**Outcome:** 7 proposal work units delivered, TUI stabilization (TUI-4/5/6) and
TUI-7 M2 Status Rail (6 items DONE) verified, provider plugins ABI v2 frozen
(`docs/ABI-v2.md`, commit `619f1f0`), spec 0.10 streaming amendment (RF-2.6)
landed, retest 4-9 fixes applied, and turn-latency closed per owner via daemon
fixes (owner-reported, not re-measured here). Branch `main` @ new commit is
tagged `v3`; push includes branch and tag.

Exit criterion (v3 formal close, derived from §6 as amended by 0.10):

> v2 exit criterion holds, plus: TUI M2 status rail is usable in a real
> terminal (6 checklist items), provider plugins load over ABI v2 without
> recompiling the binary, streaming is opt-in with a binding failure semantic
> (RF-2.6), and stabilization/retest fixes for TUI-4/5/6 and retest 4-9 are
> green. Turn-latency is owner-reported resolved.

Plus RF/RNF traceability (§6) per audited verdicts below.

---

## Quick path

1. Verify the 7 WUs and M2 rail: `go test ./internal/tui/... -count=1 -run TestM2 -v` + `go test ./internal/tui/... -count=1 -run TestOverlay -v`.
2. Verify ABI v2: `go test ./internal/plugin/... -run TestManifestKindValidation -v` and `go test ./internal/pluginwasm/... -run TestLLM -v`; read `docs/ABI-v2.md`.
3. Verify streaming amendment: `go test ./internal/llm/... -run TestSSE -v` and `go test ./internal/agent/... -run TestStreaming -v`; inspect `spec-harness-agentic.md` RF-2.6 block (default OFF, `message.delta.event` best-effort, no mid-stream fallback).
4. Full green: `go build ./...` and `go test ./internal/tui/... -count=1`.

---

## Proposal delivery — 7 WUs (commits since `v2` @ `413904c`)

| # | Area | Commit | Evidence |
|---|------|--------|----------|
| 1 | Provider plugins ABI v2 (kind=provider, `llm` permission, streaming PULL, DNS rebinding mitigation, V2 signed approval, artifact caps) | `619f1f0` | `docs/ABI-v2.md`, `internal/pluginwasm/provider_bridge.go`, `internal/pluginwasm/testdata/mock-provider/mock_provider.wasm` |
| 2 | TUI-4 — wrap transcript, lock input during turns, animated spinner, sessions, suggestions, help panel | `75fa229` | `internal/tui/model.go`, `internal/tui/model_tui5_test.go` (predecessor), `internal/tui/components/transcript.go` |
| 3 | TUI-5 — distinct layouts, slower spinner, chat scrolling, session focus, delta burst coalescing | `f9def7e` | `internal/tui/layouts`, `internal/tui/model_tui5_test.go` |
| 4 | TUI-6 — stable spinner (no transcript rebuild on tick), sidebar redesign (Context & tokens / Plugins & skills / Turn stats), sessions dropdown (`ctrl+g`), `/model` panel, `agent.max_iterations` | `a038def` | `internal/tui/model_tui6_test.go`, `internal/config/config.go` (`MaxIterations`) |
| 5 | Spec 0.10 streaming amendment + daemon/LLM streaming (SSE for Anthropic/Gemini, `consumeStream`, `message.delta.event`) | `be950eb` + `f6a2acb` + `c06a5db` | `spec-harness-agentic.md` RF-2.6, `internal/llm/sse.go`, `internal/llm/anthropic.go`, `internal/llm/gemini.go`, `internal/agent/loop.go`, `internal/daemon/delta_test.go` |
| 6 | TUI-7 M2 Status Rail | `bd1b7ce` | `internal/tui/model_tui7_test.go` (see 6-item table below) |
| 7 | Retest / stabilization batch (4-9) + daemon turn-latency fixes | `50d16e4` `71c6192` `3114c46` `8ad1278` | `internal/agent/turn_timeout_test.go`, `internal/client/client_test.go`, `internal/tools/registry_test.go`, `internal/tui/model_tui7_test.go` |

All 16 commits since `v2` are in `git log --oneline v2..HEAD`; the 7 rows above
aggregate them into proposal work units. Tree is clean at close.

## TUI-7 M2 Status Rail — 6 items DONE

All verified by `go test ./internal/tui/... -run TestM2|TestOverlay|TestTitleBar|TestWatchdog|TestWorkingMarker|TestSidecar|TestHitTest -v` (green on `bd1b7ce`).

| # | Item | Test / evidence | What it proves |
|---|------|-----------------|----------------|
| 1 | Boxed title bar (bordered box like footer, exact `titleHeightRows`, shows `forge` + cwd + daemon version) | `TestTitleBarBoxedLikeFooter`, `TestM2_TitleBarAndSeparatorPresent` | Single title chrome, no clipping, daemon addr visible |
| 2 | Status rail on/off distinct, `ctrl+o` / `ctrl+l` toggle with toast | `TestM2_RailOnOffDistinct`, `TestM2_CtrlOAndCtrlLToggleRail` | Rail is optional, discoverable |
| 3 | Footer height fix (small terminal 20 rows not clipped; dynamic `measureFooterHeight`) | `TestFooterHeight_NoClippingAtSmallHeight` | Layout budgets without overflow |
| 4 | Working marker live elapsed + sidecar persistence | `TestWorkingMarker_SendAndClear`, `TestWorkingMarker_ShowsLiveElapsed`, `TestSidecar_SaveAndLoad`, `TestSidecar_MergeOnReload`, `TestStreamingTurnPersistsUsage` | Elapsed stamps on ticks, survives reload via `.forge/tui-state.json`, no rebuild on same-second tick |
| 5 | Ghost-turn watchdog (halts after ~4 min silence, suppressed while tool runs, touched on `message.delta`) | `TestWatchdog_HaltsGhostTurn`, `TestWatchdog_SuppressedWhileToolRuns`, `TestWatchdog_QuietOnRecentActivity`, `TestTouchDaemon_OnEvent` | Hung turns do not lock TUI forever; bounded by `agent.max_turn_seconds` (default 300s, `50d16e4`) |
| 6 | Overlays float in-frame + interactions (sessions dropdown windowed `12/12`, model panel windowed, rail panels, help; rail/footer hotspots hit-test; `/copy` copies last response; `/session` navigates; mouse capture `ctrl+m`) | `TestOverlay_SessionsDropdownInFrame`, `TestOverlay_ModelPanelInFrame`, `TestOverlay_RailPanelInFrame`, `TestOverlay_HelpInFrame`, `TestHitTestRail_PureFunction`, `TestHitTestFooter_PureFunction`, `TestRailClickOpensPanel`, `TestFooterClickOpensDropdownAndModelPanel`, `TestSlashCopy_CopiesLastResponse`, `TestSlashSession_OpensPanelNavigateSelect`, `TestMouseCaptureToggle`, `TestSuggestionInterceptor_*` | All floating panels stay within `h` rows; keyboard and mouse paths work; clipboard path via `copyText` var |

## Evidence table (v3 criteria)

| Criterion | Evidence (test / command / file) | What it proves | No-recompile / isolation note |
|---|---|---|---|
| Provider plugin installs & runs without recompile (ABI v2) | `internal/pluginwasm/provider_test.go` + `internal/e2e/provider_stream_test.go` (committed `mock_provider.wasm` ~37 kB, V2 signed `approved.flag` via `FORGE_KEYS_DIR`) | `kind=provider` + `permissions=["llm"]` loaded by `Manager.LoadAll` with `ApproveExternal=false`; `providerBridge.ChatStream` drains via `forge_llm_next_chunk` PULL; host timeout `PluginLLMCallTimeout=5s` | WASM bytes byte-identical to committed artifact; `go build ./...` is the only build |
| DNS rebinding mitigation | `619f1f0` `internal/pluginwasm` net host check on resolved IP (ABI v2 doc § Known limitations) | `NetAllowlist` enforced at call time on URL host string + mitigation noted | — |
| Streaming amendment RF-2.6 (opt-in, binding failure semantic) | `spec-harness-agentic.md` v0.10 block + `internal/config/config.go` `LLM.Streaming` (default `false`, read at daemon start, no hot-reload) + `internal/agent/streaming_test.go` + `internal/llm/sse_test.go` | `ChatStream` → `message.delta.event` best-effort broadcast (non-blocking, warn on backpressure); mid-stream failure FAILS the turn; only `ErrStreamingNotSupported` degrades to `Chat` | Flag OFF by default preserves v2 behavior |
| Turn-latency resolved per owner (daemon fixes) | `50d16e4` (`agent.max_turn_seconds` 300s, `mergeToolCallDelta`, `summarizeTurn` OK fix) + `71c6192` (reconnect re-resolves `daemon.addr`) + `be950eb` streaming path | Owner reports slow/hung turns bounded; streamed tool-call fragments no longer execute half-calls | **Owner-reported, not re-measured in this doc** |
| Retest 4-9 fixes | `TestTitleBarBoxedLikeFooter`, `TestWorkingMarker_*`, `TestFooterHeight_*`, `TestInitPrimesCursorBlink`, `TestSlashSession_*`, `TestSlashCopy_*`, `TestOverlay_*`, `TestSuggestionInterceptor_*`, `fix(tools): registry lists available tools` (`3114c46`) | Retest checklist 4-9 closed | — |

## RF/RNF traceability (audited verdicts — spot-checked, not invented)

Spot-checks performed before writing: `internal/agent/loop.go` (RF-1.1 tool loop), `internal/llm/anthropic.go` + `gemini.go` (RF-2.2), `internal/llm/registry.go` + `internal/config/config.go` model_roles (RF-2.3), `internal/store` + `internal/perms` (RF-3.x/RF-4/RF-5), `internal/cli` + `internal/tui` (RF-6.x), `internal/tools/shell.go` (RF-10.2). PARTIAL/OPEN rows match the partial scope noted in each implementation file.

### RF

| RF | Verdict | Evidence (spot-check) | Note |
|----|---------|-----------------------|------|
| RF-1.1 agent with tools over workspace | **DONE** | `internal/agent/loop.go` + `internal/tools` native tools gated by perms | Verified: perms deny-by-default honored |
| RF-1.3 orchestrator → specialized subagents | **PARTIAL** | No dedicated subagent orchestrator package; TUI rail shows turn stats but not subagent topology | — |
| RF-1.4 background jobs after client disconnect | **PARTIAL** | `71c6192` reconnect follows `daemon.addr` restart (file re-resolve), daemon survives; no full background-job queue | Reconnect ok, no full background jobs |
| RF-1.2 multiple concurrent agents in one session | **OPEN** | No session-level concurrent agent scheduler; RNF-1.5 single-model queue assumption holds | Deferred |
| RF-2.1 OpenAI-compatible (Ollama etc.) | **DONE** | `internal/llm/openai_compatible.go`, `internal/llm/provider.go` | — |
| RF-2.2 Anthropic/Gemini adapters | **DONE** | `internal/llm/anthropic.go`, `internal/llm/gemini.go`, `f6a2acb` + `sse.go` | — |
| RF-2.3 switch provider/model without restart | **DONE** | `internal/config` `model_roles` + `internal/llm/registry.go` hot model selection (`--session` retains) | — |
| RF-2.6 streaming (spec 0.10, opt-in) | **PARTIAL** | `LLM.Streaming` OFF default, `ChatStream` + `message.delta.event`, failure fails turn, `ErrStreamingNotSupported` fallback only | Streaming present, not default, not yet benchmarked for TTFT |
| RF-3.1 persistent memory | **DONE** | `internal/store` SQLite + `internal/tools/anchoring.go` | — |
| RF-3.2 selective retrieval (not full history) | **DONE** | `internal/tools/retrieval.go`, `internal/agent/context.go` | — |
| RF-4 skills (create/load/lazy-load, approval) | **DONE** | `internal/skill` lazy-load (`Relevant` scoring), `forge skill new/list/validate/enable` | — |
| RF-5 plugins (tool + provider, sandbox, manifest, enable/disable) | **DONE** | `internal/pluginwasm` wazero + `forge plugin new/list/validate/enable`, `docs/ABI-v2.md` | — |
| RF-6.1 CLI core (`serve`, `run`, `chat`, `tui`, plugin/skill mgmt, provider mgmt) | **DONE** | `internal/cli/*.go` | — |
| RF-6.2 interactive TUI + non-interactive scriptable | **DONE** | `internal/tui` + `internal/cli/run.go` (`--json`) | — |
| RF-6.3 JSON output for non-interactive | **PARTIAL** | `forge run --json` exists; per-RF matrix JSON coverage not fully audited | Partial per audit |
| RF-6.3 (+) | — | — | — |
| RF-9 session branching / merging / parallel compare | **PARTIAL** | Sessions exist (`internal/daemon/session_mgr.go`); branching not formalized | Partial |
| RF-10.1 git integration | **PARTIAL** | `internal/tools/git.go` allowlist + safety floor (RNF-8.2) | Read/diff/commit present, worktree/branch-per-task not full |
| RF-10.2 shell execution with full output | **DONE** | `internal/tools/shell.go` (Landlock/seccomp on Linux, perms on others) | — |
| RF-11 autonomous one-shot + SPEC + HITL (run manifest, decomposition, self-correction, audit, autonomy levels) | **PARTIAL** | Loop + metrics present, but no `run manifest` first-class artifact or autonomy-ceiling enforcement | Partial |
| RF-7 GUI web (API + browser client, diffs, auth, TLS) | **OPEN** | TUI only; no web GUI package | Deferred — TUI is the interactive surface |
| RF-8 SDD (spec artifact, task decomposition, spec-vs-implementation validation, versioned history) | **OPEN** | No SDD runtime package; specs are markdown only | Deferred |

### RNF

| RNF | Verdict | Note |
|-----|---------|------|
| RNF-1 performance (cold start <200 ms, harness <50 ms, <100 MB idle, long-session stability) | **Pending formal verification** | Implemented (daemon startup, bounded turn), but not measured on reference bench in this doc |
| RNF-2 context/token efficiency (report tokens, stable order, ≥40% saving, KV-cache prefix, 4-8k ceiling) | Mostly implemented (stable `system→tools→memory→history`, `RNF-2.2`, metrics in `internal/agent/metrics.go`); RNF-2.3 target pending bench | — |
| RNF-3 modularity (core independent, plugin-only extension, integration contract tests) | **DONE** per RNF-3.3 contract tests (`internal/e2e/contract_test.go`, `internal/plugin/abi_test.go`) | — |
| RNF-4 security (deny-by-default, least privilege, local-first, secret redaction, untrusted-as-data, external checksum/firma, OS isolation, halt, allowlist, regulated log chain, TLS) | Mostly implemented; RNF-4 nuances per platform hold | — |
| RNF-5 portability (Linux/macOS/Windows, 100% local) | **Pending formal verification** | Linux primary; macOS `sandbox-exec` out per spec, Windows perms-only — pending matrix |
| RNF-6 observability (JSON logs, replay, cost metrics) | Implemented (`logging`, `scripts/replay`, `metrics`) | — |
| RNF-7 adaptability (mid-task direction change, versionable config) | **Pending formal verification** | `configs/README.md` precedence, but no mid-task UX bench |
| RNF-8 safe autonomy (isolated worktree, non-configurable floor, positive completion, atomic commit) | Partial (git safety floor, halt, atomic per-message commits) | Run-manifest subset deferred with RF-11 |
| RNF-9 sensitivity classification (`general`/`regulado`/`datos-sensibles` ceiling) | **Pending formal verification** | No `sensitivity` field enforcement yet |
| RNF-10 empirical bench (tokens/sec, TTFT, prefill, wall time on Perfil A/B) | **Pending formal verification** | Benchmark harness not run in this doc; targets unverified |

No RNF verdict is invented — `pending formal verification` marks implemented-but-unmeasured items explicitly.

## What was / was not built (v3 delta)

- Built once (in this verification flow): `forge` binary via `go build ./...` (single build). `go vet` / `gofmt` / `go test ./internal/tui/...` green is the gate before tag.
- Built and committed as artifacts (not rebuilt in verification):
  - `internal/pluginwasm/testdata/urlcheck/urlcheck.wasm` (41 342 bytes, WU6, used by `exit_test.go` v2) and `internal/pluginwasm/testdata/mock-provider/mock_provider.wasm` (~37 kB, ABI v2, 4 streaming chunks `Hello from mock provider!`).
  - `docs/ABI-v2.md` frozen contract (commit `619f1f0`) — any export/import/signature change requires bumping `ABIVersionV2`.
- NOT built: `cargo build` for wasm (only for regeneration when `cargo` present, loud-skip otherwise); no `*.wasm` recompiled in `go test` or in `scripts/verify-v2-exit.ps1` (which still enforces ONE `go build`).
- NOT built: any skill markdown compilation (skills are plain files).

## RNF-3.3 citation

> RNF-3.3 Cobertura de tests de integración sobre el contrato de la API interna (no solo unitarios).

Covered by `internal/e2e/contract_test.go` (pins `pluginwasm.Manager` / `skill.Manager` surface, `daemon.Method*` uniqueness) + `internal/plugin/abi_test.go` (`TestWASMExportNames`, `TestLLMErrorCodes`, `TestManifestKindValidation`) + `internal/pluginwasm/provider_test.go` (missing exports, approval, cancel) + `internal/e2e/provider_stream_test.go` (committed wasm + isolated `FORGE_KEYS_DIR`, streaming parity) + `internal/daemon/handler_plugin_skill_test.go` (RPC enable/disable).

## Known limitations (carry from v2, plus v3)

| Limitation | Detail | Mitigation / note |
|---|---|---|
| Streaming sentinel | LLM streaming parser relies on sentinel for tool-call boundaries; malformed streaming can truncate | Not exercised via full LLM in `exit_test.go` (uses `registry.Execute`); real SSE path covered by `sse_test.go` |
| DNS rebinding | `NetAllowlist` checks URL host string at call time; DNS rebinding bypass possible | Partially mitigated in `619f1f0`; documented in `docs/ABI-v2.md` |
| 2 MiB cap | `net_fetch` truncates at 2 MiB; plugin wasm capped at 2 MiB, skill file at 1 MiB (`limits.*`, WU7) | Install-time rejection with limit/actual in error |
| Wizard cargo not in CI | `wizard_regen_test.go` loud-skips without `cargo`; CI passes via committed wasm | Same as v2 |
| No live LLM in CI | `scripts/verify-v2-exit.ps1` and `exit_test.go` skip `forge run` when `FORGE_LLM`/`zen.key` absent | Go e2e `exit_test.go` covers via httptest + `ContextAssembler` |
| Bump allocator (provider) | No `forge_free`; sufficient for 3-5 chunk dogfood, not large streams | Deferred |
| Single-model per provider plugin | Manifest name == model name; multi-model advertisement deferred | `docs/ABI-v2.md` |
| RNF benches not run | RNF-1/5/7/9/10 pending reference-hardware measurement (Perfil A/B) | Tracked as pending formal verification |
| Turn-latency claim | `owner-reported, not re-measured` (daemon fixes `50d16e4`/`71c6192`) | Re-measure on Perfil A with 16k+ context before claiming RNF-1.2 |

## Deferred items (for phase 2)

- RF-1.2 multi-agent concurrency (orchestrator + subagents with shared session) — needs scheduler over single-model queue.
- RF-7 web GUI (API is single contract; GUI is a future client of it).
- RF-8 SDD runtime (spec as first-class artifact, task decomposition, implementation-vs-spec divergence signal, versioned history).
- RF-10.3 GitHub issues/PRs integration (desirable, not v1 per spec).
- RF-1.3/RF-1.4 full background jobs queue (beyond reconnect).
- RF-6.3 full JSON-output audit and RF-9 branching/merging/parallel-compare.
- RF-11 run-manifest autonomous execution (HITL checkpoints, isolation worktree, autonomy ceiling).
- RNF-1/5/7/9/10 formal benches on both hardware profiles.

## Naming & versioning

Inspected at close: `README.md` still reads `Status: MVP v0` (spec `v0.8` reference), `go.mod` is `github.com/eduardosanmartin/forge` go `1.26.7` (no semver in module path), `configs/forge.json` schema is `4` (example still shows `3` — pre-existing gap), `internal/version.Version` default is `0.0.0-dev` (overridden at link time via `-ldflags -X`), and `git tag` list contains exactly one tag:

```
v2  (annotated, 2026-09-01, "MVP v2: plugin/skill extensibility ...")
```

Convention is bare `vN` (not `vN.M.N`), used for `v2`.

**Decision for this close — OWNER DECISION PENDING on long-term scheme, most consistent tag applied:**

| Option | Tag | Rationale | Tradeoff |
|--------|-----|-----------|----------|
| **A — chosen for this tag** | `v3` | Continuity with `v2` (bare `vN`) | Not semver; `go install @v3` works but patch tracking needs extra tags |
| B | `v3.0.0` | Semver, `go list -m -versions` friendly | Breaks bare-`vN` precedent; would want `v2` retro-tag |
| C | `v0.10.0` | Aligns with `spec-harness-agentic.md` v0.10 | Spec version and repo tag diverge today; conflates document vs. product version |

This doc records the decision as **OWNER DECISION PENDING** for the future
scheme; the tag created now is **Option A: `v3`** (annotated, on the new
commit). `internal/version.Version` default is updated to `v3` so a plain
`go build` reports `forge version v3 (...)` without needing `-ldflags`.

## How to run

```powershell
# Build + TUI gate (required before tag)
go build ./...
go test ./internal/tui/... -count=1

# Focused v3 checks
go test ./internal/tui/... -run TestM2 -count=1 -v
go test ./internal/tui/... -run TestOverlay -count=1 -v
go test ./internal/llm/... -run TestSSE -count=1 -v
go test ./internal/agent/... -run TestStreaming -count=1 -v

# Operational check (isolated clone + build + daemon) — same as v2
powershell -ExecutionPolicy Bypass -File scripts/verify-v2-exit.ps1
powershell -ExecutionPolicy Bypass -File scripts/verify-v2-exit.ps1 -Repo C:\ESV\IA\harness-code

# In-process verification (no daemon, no LLM, CI-friendly)
go test ./... -count=1
go test ./internal/e2e -run TestExit_Verification -count=1 -v
go test ./internal/e2e -run TestContract -count=1 -v
```

## References

- V2 close: `docs/EXIT-VERIFICATION-v2.md` (structure template)
- ABI v2: `docs/ABI-v2.md` (commit `619f1f0`)
- Spec 0.10: `spec-harness-agentic.md` § RF-2.6 (streaming opt-in, `message.delta.event`, failure semantics)
- TUI M2: `internal/tui/model_tui7_test.go` + `internal/tui/model.go` (`bd1b7ce`)
- Streaming: `internal/llm/sse.go`, `internal/llm/anthropic.go`, `internal/llm/gemini.go`, `internal/agent/loop.go`
- Config: `configs/forge.json` (schema 4), `configs/forge.json.example`, `configs/README.md`
- Version: `internal/version/version.go` (`v3`)
- Dogfood WASM: `internal/pluginwasm/testdata/urlcheck` + `internal/pluginwasm/testdata/mock-provider`
- Third-party fixtures: `internal/e2e/testdata/thirdparty/urlcheck-ext/` + `deploy-notes/`
