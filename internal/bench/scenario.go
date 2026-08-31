package bench

// This file defines the deterministic scripted conversation the benchmark
// replays in both arms. Everything is generated from fixed templates and
// modular arithmetic — there is no randomness, so two runs over the same
// NumTurns produce byte-identical transcripts and token counts.
//
// Content profile (matching a realistic developer session):
//   - turns 1-6 state facts and decisions (project layout, constraints, a
//     naming decision, a bug decision, conventions) that later turns
//     explicitly reference;
//   - turns 7+ cycle through work templates that reference those facts;
//   - roughly every third turn from turn 9 on simulates tool work by
//     persisting a plausible multi-line tool result (directory listing, test
//     run, git status) — the bulk of real sessions' history;
//   - user and assistant messages vary between ~60 and ~150 words.

// scenarioTurn is one scripted turn of the bench conversation.
type scenarioTurn struct {
	user      string
	assistant string
	// toolName and toolOut are empty when the turn has no tool result.
	toolName string
	toolOut  string
}

// factTurns are the opening turns that state the session's facts. Later work
// turns reference them, and the v1 arm seeds the anchor store with condensed
// versions of the same decisions (see anchorSeeds).
var factTurns = []scenarioTurn{
	{
		user:      "Let me set up the context for this session before we start. The project is forge, a coding agent written in Go, and the module path is github.com/eduardosanmartin/forge. The repository root is the directory the daemon was launched from, and every path we reference today is relative to that root. Sessions and messages are persisted in SQLite under .forge/forge.db, while the retrieval index and the embedding store live in memory for the life of the daemon process. Keep this layout in mind, because every task we take on today builds on it.",
		assistant: "Noted. We are working on forge at the repository root, module github.com/eduardosanmartin/forge, with durable state only in the SQLite store under .forge/forge.db and everything else in memory for the life of the daemon process. I will keep every file reference relative to that root and treat the store as the single source of truth. If anything we build seems to need durable state outside that database, we will stop and decide where it belongs before writing code.",
	},
	{
		user:      "Before we touch code, let's fix the constraints for this session. We stay on the supported Go toolchain and reach for the standard library first, and we do not add new dependencies without an explicit decision recorded in this session. The permission model stays deny-by-default: every tool call has to pass the policy engine before it executes, and there is no blanket allow anywhere in the configuration. If a change seems to require a new dependency, surface it and we decide together instead of pulling it silently.",
		assistant: "Understood. Standard library first, no new dependencies without a recorded decision, and the deny-by-default permission model untouched: every tool invocation is checked against the policy engine, and failures come back as DENIED responses with the blocking rule. If I think a third-party library is justified, I will present the tradeoff and wait for your explicit call before adding anything to go.mod.",
	},
	{
		user:      "One naming decision I want locked in today so we stop debating it in reviews. The slice of history the assembler keeps verbatim is called the recent window. The summary-plus-recent layout that compaction produces is the compact view. Decisions and facts persisted through the anchoring tools are anchors, never memories and never notes. Use exactly these terms in identifiers, comments, and commit messages, so searching the repository for any of them stays unambiguous.",
		assistant: "Locked in. Recent window for the verbatim history slice, compact view for the summary-plus-recent layout, anchors for persisted decisions. I will use those exact terms in code, comments, and commit messages from here on, and I will flag any place in the codebase that still uses the old loose vocabulary so we can retire it during this session. Grep over the repository should always resolve to exactly one concept.",
	},
	{
		user:      "Let's record the decision from the message-ordering bug we hit last week. Sequence numbers on messages are one-based and assigned by the store at append time, so callers never compute them and nothing in the codebase may assume zero-based seq values. The retrieval indexer and any pagination code must read seq from the row instead of deriving it from a slice position. If you find arithmetic that adds one to a seq or treats the first message as zero, that is a bug, so flag it immediately.",
		assistant: "Recorded. One-based seq values, assigned by the store at append time, never computed by callers and never derived from slice positions. I will treat any code that offsets a seq by one as a defect and raise it instead of patching around it. The retrieval indexer and the pagination paths are the first places I will re-check under this rule.",
	},
	{
		user:      "Error handling convention for today's work, so the diff passes review on the first pass. Wrap every error you return with percent W so the cause chain survives, and prefix it with the operation that failed. Best-effort paths like indexing or logging stay best-effort: they log a warning and continue, and they never fail a user-visible turn. Construction paths are the opposite and fail fast, because a daemon running without a declared dependency is worse than a daemon that refuses to start.",
		assistant: "Clear. Every returned error gets wrapped with percent W and prefixed with the failing operation, so the cause chain stays readable end to end. Best-effort paths log and continue without failing the turn, and construction paths fail fast instead of degrading silently. I will apply that split consistently and call it out in review whenever a new error path blurs the line.",
	},
	{
		user:      "Last piece of setup, the testing convention. Tests are table-driven with descriptive case names, long expected output lives in golden files, and every verification run uses count equals one so cached results cannot hide a regression. A fix without a failing test written first does not count as fixed. When you report a green suite, include the package count so I can tell the whole repository was exercised, not just the package you happened to touch.",
		assistant: "Got it. Table-driven tests with descriptive case names, golden files for long output, and every run with count equals one so the cache cannot lie to us. Nothing counts as fixed without a failing test that preceded the fix, and every green-suite report will carry the package count so we know the whole repository was built and verified, not a slice of it.",
	},
}

// workUserTemplates cycle from turn 7 on. Each references facts stated in the
// opening turns, the way a real session keeps calling back to earlier
// decisions.
var workUserTemplates = []string{
	"Now the first task. Extend the session manager so the per-turn re-indexing logs a warning with the session id and the underlying error whenever the transcript fetch fails, exactly as the error convention from earlier requires. The behavior stays best-effort: a failed index rebuild must never fail the turn that just completed. Add a table-driven test that feeds a failing store and asserts the turn still succeeds while the warning is recorded.",
	"Take a look at the recent window handling in the context assembler. I want the window size documented right at the constructor, not discovered by reading call sites. Per our naming decision, the comments should say recent window, not history buffer or context slice. Keep the change small: a doc comment on the constructor plus a test that builds a session longer than the window and asserts only the newest messages survive.",
	"The compact view needs attention before we demo it. When the transcript crosses the threshold, the assembler swaps the plain window for the compacted summary plus the newest turns verbatim. Verify that the tool call fields on the kept messages survive that swap intact, because a lost tool call id breaks the follow-up turn. Write the failing test first, per the testing convention, then make it pass.",
	"I hit a strange ordering issue while reading messages back from the store. The list comes back newest first and the assembler reverses it, but the pagination offset still feels off by one somewhere. Remember the decision about one-based sequence numbers from the start of the session, and check every place that converts between a seq and a slice index. Report what you find before changing anything.",
	"Let's improve the retrieval path. After each turn the daemon rebuilds the index from the full transcript, which is correct but wasteful for long sessions. Sketch, do not implement yet, how an incremental index could work while keeping the stateless rebuild as the fallback. Surface the tradeoff between memory and consistency, and remember that any new storage engine here would collide with our no-new-dependencies constraint.",
	"Review the anchors flow end to end. A decision stored through the anchoring tool must appear in the anchored facts block of the very next turn, with the session flag gating the injection. Confirm the anchor store shares the session database handle instead of opening a second connection, and that a store error skips the injection instead of failing the turn, the way the error convention demands.",
	"Time to clean up the bench harness leftovers. There are one-off scripts sitting in the repository root from the earlier debugging sessions. List everything that is there, classify each file as disposable or load-bearing, and show me the list before deleting anything. Nothing in that cleanup may touch the specs or the migration code, and the working tree must keep building after every individual removal.",
	"For the demo script I need the daemon start sequence documented. Start from an empty data directory, note which tables get created and when, which dependencies are constructed before the websocket listener comes up, and where each v1 feature flag is read from session metadata. Keep it factual, one sentence per step, so we can turn it into runbook entries later without rewriting anything.",
}

// workAssistantTemplates pair with workUserTemplates by index.
var workAssistantTemplates = []string{
	"Done. The re-indexing path now logs a warning carrying the session id and the wrapped cause whenever the transcript fetch or the index build fails, and the turn still completes normally. The table-driven test covers a failing fetch and a failing index separately, both asserting the turn result is untouched. The full suite passes with count equals one, including the daemon package.",
	"Updated. The constructor now documents that maxHistoryTurns sets the recent window, defaults to ten when unset, and that the window counts persisted messages rather than turns. The new test builds a session well past the window and asserts the older messages drop out while the newest ones survive. Terminology in the touched comments now matches the naming decision we locked in earlier.",
	"The failing test came first, as agreed: it appends a tool call pair, crosses the threshold, and asserts the kept messages still carry their tool call ids. The swap to the compact view indeed dropped them, so I restored the fields from the original transcript for the verbatim slice. The test is green now and the whole suite passes with it.",
	"I found it. One pagination helper was subtracting the offset from a limit computed over newest-first rows, which is correct only for that ordering, and a caller passed it oldest-first data. Per the one-based seq decision, I also flagged a comment that described the first message as zero. No behavior change yet; the report is in the summary for your call.",
	"Here is the sketch. The index would keep per-message chunks with their seq, and each turn would index only messages newer than the last indexed seq, while a flag toggle or a gap falls back to the full rebuild. Memory stays bounded by the transcript either way. The tradeoff is a stale index window if an append fails midway; the fallback rebuild covers exactly that case.",
	"Confirmed end to end. A stored anchor shows up in the anchored facts block on the next Build, the session flag gates the injection, and the anchor store queries the same database handle the session store owns. A forced store error skips the injection and logs, leaving the turn intact, which matches the error convention we fixed earlier. No second connection is ever opened.",
	"Here is the inventory. Four debug scripts and two one-off benchmark generators are disposable; the e2e and bootstrap scripts under scripts are load-bearing. I have not deleted anything yet. Nothing on the disposable list touches the specs or the migrations, and I verified the tree still builds with each candidate removed in isolation, per your constraint. Say the word and I will proceed.",
	"The start sequence is documented. An empty data directory gets the store opened and migrated first, the anchors table is created alongside it, then the embedding store, retriever, compactor, and anchor store are constructed, then the tools registry, and only after that does the websocket listener come up. Feature flags are read from session metadata at turn time, not at startup.",
}

// toolAssistantTemplates are the assistant replies on tool-work turns,
// indexed by tool kind (see toolKindNames).
var toolAssistantTemplates = []string{
	"The listing matches the layout we fixed at the start of the session: everything we care about lives under internal, the entrypoint stays under cmd, and nothing unexpected has appeared at the root. I will keep the new code inside the existing package boundaries so the structure stays stable for the demo. If a new file is genuinely needed, it goes next to its siblings rather than into a new package.",
	"The suite is green across every package, including the ones we touched earlier, so the convention of running with count equals one is doing its job. I will keep that exact command as the verification step for the rest of the session and re-run it before we record any decision as done. If a package starts failing after the next change, that output is the first place we look.",
	"The working tree only shows the files from our current work unit plus the new bench script, nothing stale from previous sessions, which keeps the diff reviewable. I will keep staging changes per work unit, and once you confirm the next step I will prepare the commit message following our conventions. Any file that turns out unrelated gets reported instead of silently included.",
}

// detailPool sentences extend work user messages on alternating turns to vary
// message length deterministically.
var detailPool = []string{
	"Also double-check that the change behaves the same when the session is brand new as when it already has a long transcript behind it.",
	"Keep the diff reviewable: no drive-by refactors and no formatting-only churn in files you are not otherwise touching.",
	"If you need a third opinion on the design, write the question down and we will settle it before the code moves further.",
	"Make sure any log line you add carries enough context to be useful to someone reading the daemon log a week from now.",
	"The same rule applies here as everywhere else: the store is the source of truth, never the in-memory state.",
	"Whatever you decide, record the reasoning in a comment so the next session does not relitigate it from scratch.",
	"If the change turns out bigger than one focused work unit, split it and show me the seam before going further.",
	"And as always, run the full suite with count equals one before you report anything as done.",
}

// extraDetailPool sentences extend work user messages on a sparser cadence.
var extraDetailPool = []string{
	"One more thing: write down any assumption you make about the store schema so we can verify it against the migrations.",
	"If the output gets long, prefer a golden file over an inline expectation so the diff stays readable.",
	"Treat this as a chance to delete dead code you bump into, as long as the suite stays green.",
}

// toolKindNames are the plausible tool names persisted with scripted tool
// results. They mirror the base registry names.
var toolKindNames = []string{"fs_list", "shell_exec", "git"}

// anchorSeeds are the condensed decision anchors seeded into the v1 arm's
// anchor store, the way the anchoring tools would have persisted them early
// in the session. They are injected as anchored facts on every v1-arm turn.
var anchorSeeds = []string{
	"Project layout: forge lives at the repository root, module github.com/eduardosanmartin/forge; durable state is the SQLite store under .forge/forge.db; the retrieval index is in-memory per daemon process.",
	"Constraints: supported Go toolchain, standard library first, no new dependencies without a recorded decision, permissions deny-by-default on every tool call.",
	"Naming: the verbatim history slice is the recent window, the summary-plus-recent layout is the compact view, persisted decisions are anchors.",
	"Bug decision: message seq values are one-based and assigned by the store at append time; never derive a seq from a slice position.",
}

// buildScenario generates the deterministic conversation for turns turns.
func buildScenario(turns int) []scenarioTurn {
	out := make([]scenarioTurn, 0, turns)
	for t := 1; t <= turns; t++ {
		out = append(out, scenarioTurnFor(t))
	}
	return out
}

// scenarioTurnFor returns the scripted turn for the 1-based turn number.
func scenarioTurnFor(turnNo int) scenarioTurn {
	if turnNo <= len(factTurns) {
		return factTurns[turnNo-1]
	}

	idx := (turnNo - len(factTurns) - 1) % len(workUserTemplates)
	turn := scenarioTurn{
		user:      workUserTemplates[idx],
		assistant: workAssistantTemplates[idx],
	}
	if turnNo%2 == 0 {
		turn.user += " " + detailPool[(turnNo/2)%len(detailPool)]
	}
	if turnNo%7 == 0 {
		turn.user += " " + extraDetailPool[(turnNo/7)%len(extraDetailPool)]
	}

	// Tool work roughly every third turn from turn 9 on, rotating through
	// the three tool kinds. Every ninth turn adds a second block to the
	// output (a daemon log excerpt), the way real tool results pile up.
	if turnNo >= 9 && turnNo%3 == 0 {
		kind := (turnNo / 3) % len(toolKindNames)
		turn.toolName = toolKindNames[kind]
		turn.toolOut = toolOutput(kind, turnNo)
		turn.assistant = toolAssistantTemplates[kind]
	}
	return turn
}

// toolOutput renders a plausible multi-line tool result for the given tool
// kind and turn.
func toolOutput(kind, turnNo int) string {
	var out string
	switch kind {
	case 0:
		out = fsListOutput
	case 1:
		out = testRunOutput
	default:
		out = gitStatusOutput
	}
	if turnNo%9 == 0 {
		out += "\n" + daemonLogExcerpt
	}
	return out
}

const fsListOutput = `$ fs_list .
NAME                             MODE  SIZE    MODIFIED
internal/agent                   0755  -       2026-08-30
internal/agent/context.go        0644  11253   2026-08-30
internal/agent/loop.go           0644  9865    2026-08-30
internal/agent/metrics.go        0644  2140    2026-08-29
internal/bench                   0755  -       2026-08-30
internal/bench/bench.go          0644  6204    2026-08-30
internal/daemon                  0755  -       2026-08-30
internal/daemon/session_mgr.go   0644  13040   2026-08-30
internal/retrieval               0755  -       2026-08-28
internal/retrieval/retriever.go  0644  3382    2026-08-28
internal/store                   0755  -       2026-08-29
internal/store/store.go          0644  14874   2026-08-29
internal/tools                   0755  -       2026-08-30
internal/tools/registry.go       0644  6433    2026-08-30
internal/tools/shell.go          0644  7121    2026-08-27
cmd/forge                        0755  -       2026-08-25
go.mod                           0644  812     2026-08-20
go.sum                           0644  2847    2026-08-20
18 entries, 1.42 MB total`

const testRunOutput = `$ go test -count=1 ./...
ok      github.com/eduardosanmartin/forge/internal/agent       0.512s
ok      github.com/eduardosanmartin/forge/internal/anchor      0.104s
ok      github.com/eduardosanmartin/forge/internal/bench       0.233s
ok      github.com/eduardosanmartin/forge/internal/compaction  0.096s
ok      github.com/eduardosanmartin/forge/internal/config      0.187s
ok      github.com/eduardosanmartin/forge/internal/daemon      0.641s
ok      github.com/eduardosanmartin/forge/internal/embedding   0.088s
ok      github.com/eduardosanmartin/forge/internal/perms       0.121s
ok      github.com/eduardosanmartin/forge/internal/retrieval   0.152s
ok      github.com/eduardosanmartin/forge/internal/routing     0.074s
ok      github.com/eduardosanmartin/forge/internal/store       0.401s
ok      github.com/eduardosanmartin/forge/internal/tools       0.733s
ok      github.com/eduardosanmartin/forge/internal/websocket   0.214s
13 packages, all green, 0 failures, 0 skips`

const gitStatusOutput = `$ git status
On branch main
Your branch is ahead of 'origin/main' by 3 commits.
  (use "git push" to publish your local commits)

Changes not staged for commit:
  modified:   internal/agent/context.go
  modified:   internal/agent/loop.go
  modified:   internal/daemon/session_mgr.go
  modified:   internal/bench/bench.go
  modified:   internal/tools/registry.go

Untracked files:
  scripts/run-bench.ps1

no changes added to commit (use "git add" and/or "git commit -a")`

const daemonLogExcerpt = `--- daemon log excerpt (last 12 lines) ---
2026-08-30T21:14:02 INFO  session created session_id=7f3a91c2
2026-08-30T21:14:05 INFO  turn started turn=1 session_id=7f3a91c2
2026-08-30T21:14:06 INFO  tool executed name=fs_list dur=12ms
2026-08-30T21:14:07 INFO  turn finished turn=1 prompt_approx=1180
2026-08-30T21:14:11 INFO  turn started turn=2 session_id=7f3a91c2
2026-08-30T21:14:12 INFO  v1 retrieval injection chunks=3 score_avg=0.04
2026-08-30T21:14:13 INFO  turn finished turn=2 prompt_approx=1340
2026-08-30T21:14:20 INFO  turn started turn=3 session_id=7f3a91c2
2026-08-30T21:14:21 WARN  v1 retrieval index empty, skipping injection
2026-08-30T21:14:22 INFO  turn finished turn=3 prompt_approx=1510
2026-08-30T21:14:30 INFO  v1 compaction applied messages=42 summaries=2
2026-08-30T21:14:31 INFO  store checkpoint wal_bytes=184320`
