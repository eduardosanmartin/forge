# forge

Local-first agentic development harness: a daemon that lets a local LLM hold
tool-using conversations (read/write files, run commands, commit to git) inside
a workspace whose every action is gated by an explicit, deny-by-default
permission policy. The product thesis, architecture, and versioned roadmap are
specified in `spec-harness-agentic.md` (v0.8); this repository implements the
v0 MVP defined there.

## Status

v0 is complete and most of the v1 surface is in place: **92 of 97**
requirements covered (the checklist at the top of `spec-harness-agentic.md`
is the source of truth, including what is partial and why). CI runs
gofmt/vet/build/test on Linux, Windows and macOS, plus a race-detector job on
Linux.

Beyond the v0 core (agent + native tools, Ollama/OpenAI-compatible providers,
SQLite sessions, CLI, security floor), forge now ships:

- **Context efficiency:** cache-friendly context assembly, block-stable
  compaction, incremental retrieval, repo map (`code_symbols` tool), memory
  anchors, routing between models.
- **Runs (RF-11):** multi-task runs with branch isolation, one commit per
  task, `done_criteria` checks, checkpoints before merge, recovery after a
  daemon restart, and a workspace lock while an isolated run holds it.
- **Extensibility:** WASM plugins (wazero), skills with mining from
  successful sessions, MCP client and server, subagents.
- **Interfaces:** CLI, terminal TUI, embedded web GUI, remote access with
  token + TLS.
- **Safety extras:** interactive permission prompts, per-turn file
  snapshots with `forge undo`, prompt-injection heuristics on tool output.

Still open: hierarchical LLM compaction (RF-3.3), routing of compaction and
retrieval to the small model (RF-2.4), semantic skill loading (RF-4.2), and
live benchmark runs on the second reference hardware profile (RNF-10.2/10.3).

## Quickstart

Requirements: Go 1.26+, git, and a local Ollama server with a tool-capable
model (`ollama pull qwen2.5-coder:7b`).

```
go build -o forge ./cmd/forge          # build the single binary

# ~/.forge/config.json — minimal example (defaults shown; see Configuration)
mkdir -p ~/.forge
cp configs/forge.json.example ~/.forge/config.json

cd path/to/your-project                # the daemon's workspace = launch directory
forge serve                            # starts daemon, writes ~/.forge/daemon.addr

# in another terminal, from the same project directory:
forge run --json "Create hello.go with a main that prints hi"   # one-shot turn
forge chat                                                       # interactive REPL
forge status                                                      # daemon health
```

`forge run --json` prints a structured result on stdout: `session_id`,
`response`, per-call `tool_calls`, `usage` (tokens), and `duration_ms`. Reuse a
session across runs with `--session <id>` for multi-turn conversations.

### Web GUI (RF-7.2/7.3)

`forge serve` also serves a small session browser on the same address as the
daemon's WebSocket API — open `http://<daemon-addr>/` (printed by `forge
status`, or in the `forge serve` startup log) in a browser. It lists
sessions, shows a session's message/tool-call timeline live, and can compare
two sessions to see how a branch diverged. It is static HTML/CSS/JS embedded
in the binary (`internal/webui`), talks only to the existing JSON-RPC API —
no separate server, no new auth, no change to the daemon's default
loopback-only bind address.

### Remote access (RF-7.4 / RNF-4.11)

`forge serve --addr` binding beyond loopback (127.0.0.1) is refused unless
BOTH an auth token and TLS are configured — there is no insecure remote mode:

```
forge daemon set-password              # prompts on stdin; prefer piping it in
  printf '%s' 'my password' | forge daemon set-password
forge serve --addr 0.0.0.0:8443 --tls-self-signed   # or --tls-cert/--tls-key with a real pair
```

`--tls-self-signed` generates (and reuses across restarts) an ephemeral
certificate under `~/.forge` — genuinely encrypted, but not verifiable
against a public CA, so browsers will warn; fine for a private network (VPN,
SSH tunnel), use a real certificate for public exposure. The GUI shows a
password prompt automatically when a remote/authenticated daemon requires
one; the CLI reads the same password from `FORGE_DAEMON_TOKEN` (and
`FORGE_DAEMON_TLS=1` to dial `wss://` instead of `ws://`) when connecting to
one. `forge daemon set-password --clear` removes the token again.

## Configuration

Loaded in precedence order (later overrides earlier): built-in defaults →
`~/.forge/config.json` → `./.forge/config.json` (or `--config <path>`).
Documents carry `schema_version`; older versions migrate forward automatically.
Unknown fields are rejected.

The security-relevant section is deny-by-default: nothing runs unless a rule
allows it.

```json
{
  "permissions": {
    "fs":    { "read": ["./**"], "write": ["./src/**"] },
    "shell": { "allow": ["go"], "require_isolation": true },
    "git":   { "allow": ["status", "add", "commit", "log", "diff"] }
  }
}
```

- Relative fs globs match workspace-relative paths; absolute patterns are the
  documented escape hatch for explicitly authorized out-of-workspace locations.
  Escaping paths (e.g. `../`) auto-deny unless an absolute pattern allows them.
- A non-configurable git safety floor blocks destructive subcommands before any
  allowlist is consulted — also when git is invoked through `shell_exec`.
- `shell.allow` entries match a program by base name (`"go"`), optionally
  followed by an argument pattern (`"go test *"`, `"npm run lint"`; `*` = any
  text). A path-qualified program inside the workspace (`./src/go`) runs only
  if an entry names that exact path (`"./scripts/check.sh"`), so an allowed
  name can't be shadowed by a file the agent wrote. `workdir` for shell/git
  must stay inside the workspace.
- `ask` lists (`shell.ask`, `git.ask`, `fs.ask_write`) hold a matching
  operation until a connected client approves it: the TUI shows a modal
  (`y` once, `s` for the session, `n`/`esc` deny) and the web GUI a dialog.
  No client connected, no answer within 5 minutes, or a cancelled turn =
  deny. Floors (git floor, shell floor) are never askable.
- `network.allowed_hosts` gates every provider endpoint by host (or exact
  host:port); an empty list denies all egress.
- `limits.plugin_wasm_max_bytes` caps the plugin `.wasm` entrypoint at install
  time (default `2097152` = 2 MiB). `limits.skill_file_max_bytes` caps each file
  inside a skill directory at install time (default `1048576` = 1 MiB).
  Exceeding a cap rejects the install with an error naming the limit, actual
  size, and configured max (e.g. `plugin wasm too large: 3145728 bytes > limit 2097152 (limits.plugin_wasm_max_bytes)`).
  Zero or negative values in `config.json` fall back to defaults. The message
  store cap is deferred (not enforced). Enforcement is install-time only — the
  managers do not re-check sizes on load; oversized artifacts are rejected at
  the policy boundary before bytes are written.

### MCP servers

External MCP servers extend the agent's tools without writing a plugin:

```json
{
  "mcp": { "servers": {
    "docs":   { "command": "npx", "args": ["-y", "some-docs-mcp-server"] },
    "github": { "command": "github-mcp-server", "args": ["stdio"],
                "env": { "GITHUB_TOKEN": "${env:GITHUB_TOKEN}" } },
    "remote": { "url": "https://mcp.example.com/mcp" }
  }},
  "permissions": { "mcp": { "allow": ["docs/*", "github/get_*"], "ask": ["github/create_pull_request"] } }
}
```

- `forge mcp list` connects to each server and shows its tools; `forge mcp
  approve <server>` approves its current tool list (names, descriptions and
  schemas reach the model, so they are reviewed like an external plugin). A
  changed tool list disables the server until approved again.
- Only tools allowed or asked for in `permissions.mcp` ("server/tool", globs;
  `"*"` = all) are shown to the model; calls go through the same permission
  engine, fencing, redaction and prompt-injection flagging as native tools.
  HTTP servers must be in `network.allowed_hosts`.
- `forge mcp serve` exposes forge to other agents over stdio (`forge_task`,
  `forge_run_manifest`, `forge_run_status`, `forge_sessions`); checkpoint
  approval is deliberately not exposed.

## Security posture

- **RNF-4.1** Deny-by-default permission engine for fs/shell/git; decisions are
  audited and denials surface to the model as data.
- **RNF-4.3 / RNF-4.4** Local-first: state stays in local SQLite under
  `~/.forge`; secrets are redacted from logs and tool output.
- **RNF-4.5** Tool results are untrusted data, wrapped in fencing markers so
  content can never steer the harness as instructions.
- **RNF-4.7** On Linux, shell commands run through an OS-isolation wrapper
  (Landlock + seccomp via forge re-exec), exercised by the Linux CI job;
  `require_isolation` refuses shell execution when unavailable. Windows/macOS
  are permissions-only (documented spec §6 nuance).
- **RNF-4.8** Emergency halt from any client cancels in-flight turns
  immediately; halted sessions persist state and reject turns until resumed.
- **RNF-4.9** Network egress allowlist is on by default in every mode.

## Development

```
go build ./...            # compile everything
go vet ./...
gofmt -l .                # must print nothing
go test -count=1 ./...    # default suite: no live model required
go test -race ./...       # needs cgo (a C compiler); CI runs it on Linux
```

End-to-end verification lives in `internal/e2e`:

- The offline suite runs in the default `go test ./...` against a scripted
  OpenAI-compatible mock server (full in-process stack, deterministic).
- The live suite demonstrates the spec §6 exit criterion against a real model:

```
$env:FORGE_E2E_LIVE = "1"; go test -v ./internal/e2e        # PowerShell
FORGE_E2E_LIVE=1 go test -v ./internal/e2e                  # POSIX shell
```

Optional env: `FORGE_E2E_BASE_URL` (default `http://127.0.0.1:11434/v1`),
`FORGE_E2E_MODEL` (default `qwen2.5-coder:7b`). Tests skip when no live server
answers `/api/version`.

Operator script driving the real binary end-to-end (builds, serves, six tool
turns, PASS/FAIL table): `scripts/run-e2e.ps1` / `scripts/run-e2e.sh`.

Layout:

| Path | Role |
| --- | --- |
| `cmd/forge` | entrypoint + isolation-wrapper dispatch |
| `internal/cli` | cobra commands: serve, chat, run, attach, halt, resume, sessions, status |
| `internal/client` | reconnecting JSON-RPC-over-WebSocket client, REPL, one-shot mode |
| `internal/daemon` | transport, RPC handler, session manager, emergency halt |
| `internal/webui` | embedded static session GUI, served by the daemon transport (RF-7.2/7.3) |
| `internal/agent` | turn loop, context assembler, metrics |
| `internal/tools` | native tools (fs, shell, git), schema validation, fencing |
| `internal/perms` | deny-by-default engine + git safety floor + audit log |
| `internal/pathmatch` | shared glob semantics for config and permissions |
| `internal/config` | versioned, migrating, mergeable configuration |
| `internal/llm` | OpenAI-compatible provider adapter + hot-swap registry |
| `internal/store` | SQLite sessions/messages with migrations |
| `internal/isolation` | Linux Landlock/seccomp wrapper capability |
| `internal/run` | multi-task run engine (RF-11): branches, verification, recovery |
| `internal/retrieval`, `internal/embedding`, `internal/compaction` | context retrieval, embeddings, history compaction |
| `internal/repomap` | workspace symbol index behind `code_symbols` |
| `internal/mcpbridge` | MCP client: external servers' tools as forge tools (`forge mcp serve` exposes forge itself) |
| `internal/plugin`, `internal/pluginwasm`, `internal/skill` | plugins (WASM) and skills |
| `internal/snapshot` | per-turn file snapshots for `forge undo` |
| `internal/tui` | terminal UI |
| `internal/bench`, `internal/benchlive`, `internal/perf` | offline/live benchmarks and performance tests |
| `internal/e2e` | offline + live end-to-end suites |

## Roadmap

Version milestones, exit criteria per version, and deferred capabilities are
tracked in `spec-harness-agentic.md` §6. Development is driven with external
tooling (OpenCode + frontier models via OpenAI-compatible endpoints); the
guiding principle is context/token efficiency (RNF-2.x). Bootstrapping —
building each MVP with the previous one — is a deferred, desirable requirement
(spec §0, changelog 0.9); v0 remains the seed.
