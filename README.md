# forge

Local-first agentic development harness: a daemon that lets a local LLM hold
tool-using conversations (read/write files, run commands, commit to git) inside
a workspace whose every action is gated by an explicit, deny-by-default
permission policy. The product thesis, architecture, and versioned roadmap are
specified in `spec-harness-agentic.md` (v0.8); this repository implements the
v0 MVP defined there.

## Status: MVP v0

Scope (spec §6): agent + native tools over a workspace, one OpenAI-compatible
provider adapter (Ollama), persistent SQLite sessions, full CLI, basic git +
shell tooling, and the security floor (deny-by-default permissions, network
allowlist, emergency halt). v1+ capabilities — retrieval, compaction,
subagents, OS isolation beyond Linux — are out of scope.

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
  allowlist is consulted.
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

## Security posture

- **RNF-4.1** Deny-by-default permission engine for fs/shell/git; decisions are
  audited and denials surface to the model as data.
- **RNF-4.3 / RNF-4.4** Local-first: state stays in local SQLite under
  `~/.forge`; secrets are redacted from logs and tool output.
- **RNF-4.5** Tool results are untrusted data, wrapped in fencing markers so
  content can never steer the harness as instructions.
- **RNF-4.7** On Linux, shell commands run through an OS-isolation wrapper
  (Landlock + seccomp via forge re-exec); `require_isolation` refuses shell
  execution when unavailable. Windows/macOS v0 are permissions-only (documented
  spec §6 nuance).
- **RNF-4.8** Emergency halt from any client cancels in-flight turns
  immediately; halted sessions persist state and reject turns until resumed.
- **RNF-4.9** Network egress allowlist is on by default in every mode.

## Development

```
go build ./...            # compile everything
go vet ./...
gofmt -l .                # must print nothing
go test -count=1 ./...    # default suite: no live model required
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
| `internal/e2e` | offline + live end-to-end suites |

## Roadmap

Version milestones, exit criteria per version, and deferred capabilities are
tracked in `spec-harness-agentic.md` §6. Development is driven with external
tooling (OpenCode + frontier models via OpenAI-compatible endpoints); the
guiding principle is context/token efficiency (RNF-2.x). Bootstrapping —
building each MVP with the previous one — is a deferred, desirable requirement
(spec §0, changelog 0.9); v0 remains the seed.
