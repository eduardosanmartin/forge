# Exit Verification — MVP v2 (WU7)

This document maps each v2 exit criterion from `spec-harness-agentic.md` to the
evidence that proves it, the exact artifact/command that produces it, and the
known limitations that remain after v2.

Exit criterion (verbatim, §6 / v2 definition):

> "instalaste un plugin de terceros y una skill sin recompilar el binario, y
> ambos corren aislados con permisos mínimos declarados. Además: el wizard CLI
> permite crear plugins y skills válidos desde cero sin editar archivos a mano."

Plus RNF-3.3:

> "RNF-3.3 Cobertura de tests de integración sobre el contrato de la API interna (no solo unitarios)."

---

## Evidence table

| Criterion | Evidence (test name / script step) | What it proves | No-recompile claim |
|---|---|---|---|
| Third-party plugin installed without recompile | `TestExit_Verification/plugin/install_external` (internal/e2e/exit_test.go) + `scripts/verify-v2-exit.ps1: forge plugin install --yes` | Real CLI path `cli.RunPluginInstallForTest` / `forge plugin install` copies the pre-built `urlcheck.wasm` (41 342 bytes, sha256:73d805cdbf101d5ed71afa4610db46e01986c906e70537d0c11a3b1f7de3f899) into a fresh `forge-plugins/urlcheck`, writes hash-bound `approved.flag`, and `pluginwasm.Manager.LoadAll` loads it with `ApproveExternal=false` (approval from record, not global flag). No `cargo build` is invoked. | `go build ./...` is the ONLY build; `urlcheck.wasm` is byte-identical before and after (`string(installedWasm)==string(committedWasm)` assertion). The committed wasm was built once in WU6; the test fails if the bytes were mutated. |
| Plugin runs isolated with minimum permission | `TestExit_Verification/plugin/load_enable_execute_without_recompile` | `pluginwasm.NewManager` with `Perms` engine + `NetAllowlist: ["127.0.0.1"]` + `permissions=["net"]` in manifest; `registry.Execute("urlcheck_status", {url: httptest URL})` hits the httptest server exactly once and returns `{"url","status":200,"bytes":...}`. `TestUrlcheck_PermissionDeniedWithoutNet` (existing WU6 test) proves non-net permission denies `net_fetch`. | Same as above — execution is via the already-built wasm, no rebuild. |
| Third-party skill installed without recompile | `TestExit_Verification/skill/install_external` + script `forge skill install --yes` | `cli.RunSkillInstallForTest` copies the external `SKILL.md` (source=external, checksum over `skill.StripChecksumLine` semantics) into `.forge/skills/deploy-notes`, writes hash-bound `approved.flag`. `skill.NewManager.Scan` with `ApproveExternal=false` loads it. No compilation at all — skills are markdown. | Skills are plain files; `go build` does not compile them. Flag binds `sha256` of `SKILL.md`-minus-checksum-line. |
| Skill runs isolated with lazy-load | `TestExit_Verification/skill/scan_enable_relevant` + `skill/context_assembler_injects` | `skill.Manager.Relevant("preparing the deploy notes for release")` returns `deploy-notes`; `agent.ContextAssembler.Build` with `V1Deps{Skills: mgr}` and session `v1_skills=true` injects `SKILL INSTRUCTIONS (v1) [deploy-notes]` only for the relevant query, not for unrelated queries. | No recompile; injection is purely in-memory retrieval. |
| Runtime enable/disable without recompile | `TestExit_Verification/plugin/disable_enable_cycle` + `skill/disable_enable_cycle` | `Manager.Disable` unregisters the tool / removes from `Relevant`; `Manager.Enable` re-registers without calling `LoadAll` or rebuilding. `registry.Get` proves the state. Daemon RPC `plugin.enable/disable` and `skill.enable/disable` expose the same flow (covered in `internal/daemon/handler_plugin_skill_test.go` and via `scripts/verify-v2-exit.ps1: forge plugin enable`). | No filesystem copy or build — only in-memory registry mutation. |
| Wizard creates valid plugin/skill without hand-edit | `TestExit_Verification/wizard/generates_valid_artifacts` + existing `TestWizard_RegenAndCargoBuild` (internal/cli/wizard_regen_test.go) + script `forge plugin new` / `forge skill new` + `forge plugin validate` / `forge skill validate` | Scripted prompters drive `cli.RunPluginWizardForTest` and `cli.RunSkillWizardForTest` to create `manifest.toml` / `SKILL.md` scaffolds; `plugin.ParseManifest` / `skill.NewManager.Scan` validates them. `wizard_regen_test.go` additionally proves `wizard → cargo build → load` when cargo is present (loud skip otherwise); this test references that coverage and does NOT duplicate the cargo invocation. Script validates with `forge plugin validate <path>` and `forge skill validate <path>`. | Wizard output is validated structurally, not compiled in this test. The regen test is the only place that compiles Rust. |
| RF-4.4 isolation (proposals not auto-active) | `TestExit_Verification/isolation/proposals_not_scanned` + `isolation/external_requires_approval` + existing `TestProposalsNotScannedBySkillsManager` (WU5) + script proposals check | A valid `SKILL.md` under `.forge/skill-proposals` is NOT discovered by `skill.Manager.Scan(skillsRoot)`; an external plugin/skill without `approved.flag` and with `ApproveExternal=false` fails `LoadAll` / `Enable` with `ErrApprovalRequired`. Script creates `skill-proposals/should-not-appear` and asserts `forge skill list` does not show it. | — |
| RNF-3.3 integration over internal API contract | `internal/e2e/contract_test.go` | Compile-time `var _` assertions pinning `pluginwasm.Manager` / `skill.Manager` surface (`Info/Reload/Enable/Disable/Close`), uniqueness of `daemon.Method*` constants and `daemon.ErrCode*` codes, and `tools.PermRequestSource` interface existence. `go vet` / `go test ./...` fails on contract drift. Cited verbatim in file header. | Contract is code, not artifact build. |
| No recompilation of plugins | Repo-wide invariant (this doc + exit_test.go assertion + script log) | `scripts/verify-v2-exit.ps1` builds exactly once (`go build ./cmd/forge`); plugins are `*.wasm` + `manifest.toml` + `SKILL.md` copied, never `cargo build`. The test asserts installed wasm bytes equal committed wasm bytes. | See per-row evidence. |

## What was / was not built

- Built once: `forge` binary via `go build ./cmd/forge` (or `go build ./...` / `go test` harness). This is the ONLY compilation that happens in the verification flow.
- NOT built: `urlcheck.wasm` (pre-built in WU6, 41 342 bytes, committed at `internal/pluginwasm/testdata/urlcheck/urlcheck.wasm` and mirrored at `internal/e2e/testdata/thirdparty/urlcheck-ext/urlcheck.wasm`), any skill markdown, any other plugin. The wizard-generated `wiz-verify` plugin scaffold is validated but NOT cargo-built here; the cargo build is exercised only in `wizard_regen_test.go` when `cargo` is present.
- The PowerShell script `scripts/verify-v2-exit.ps1` enforces the "ONE build" rule by invoking `go build` exactly once and never calling `cargo`.

## RNF-3.3 citation

> RNF-3.3 Cobertura de tests de integración sobre el contrato de la API interna (no solo unitarios).

Covered by `internal/e2e/contract_test.go` (doc comment cites verbatim) + `internal/e2e/exit_test.go` (full-stack journey over real CLI + daemon seams) + `internal/daemon/handler_plugin_skill_test.go` (RPC paths in-process).

## Known limitations (from WU6 risks, still applicable)

| Limitation | Detail | Mitigation / note |
|---|---|---|
| Streaming sentinel | LLM streaming parser relies on a sentinel for tool-call boundaries; malformed streaming can truncate. | Not exercised in exit test (uses direct `registry.Execute`, not full LLM turn). |
| DNS rebinding | `NetAllowlist` checks the URL host string at call time, not resolved IP; DNS rebinding could bypass host check. | Documented; no fix in v2. |
| 2 MiB cap | `net_fetch` truncates bodies at 2 MiB (`2*1024*1024`); oversize responses report truncated `bytes`. | Proven in `TestUrlcheck_NetFetch_OversizedTruncates`. |
| Wizard cargo not in CI | `wizard_regen_test.go` loud-skips when `cargo` is absent; CI without Rust still passes via committed wasm. | Exit test references regen coverage and does not require cargo. |
| No live LLM in CI | Operational script skips `forge run` when `FORGE_LLM` / `.forge/zen.key` absent; Go e2e `exit_test.go` covers execution without an LLM (httptest + ContextAssembler). | Script emits `SKIPPED` (non-fatal) for that row. |

## How to run

```powershell
# Full operational check (isolated clone + build + daemon)
powershell -ExecutionPolicy Bypass -File scripts/verify-v2-exit.ps1
powershell -ExecutionPolicy Bypass -File scripts/verify-v2-exit.ps1 -Repo C:\ESV\IA\harness-code

# In-process verification (no daemon, no LLM, CI-friendly)
go test ./... -count=1
go test ./internal/e2e -run TestExit_Verification -count=1 -v
go test ./internal/e2e -run TestContract -count=1 -v
```

## References

- Regen coverage: `internal/cli/wizard_regen_test.go` (`TestWizard_RegenAndCargoBuild`)
- Isolation invariant: WU5 `TestProposalsNotScannedBySkillsManager`
- Dogfood WASM source: `internal/pluginwasm/testdata/urlcheck/src/lib.rs` + `Cargo.toml`
- Third-party fixtures: `internal/e2e/testdata/thirdparty/urlcheck-ext/` + `internal/e2e/testdata/thirdparty/deploy-notes/`
