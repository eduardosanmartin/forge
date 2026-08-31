#!/usr/bin/env bash
# End-to-end smoke of the real forge binary against a live local model.
#
# Builds forge, isolates HOME into a temp directory, starts `forge serve` on
# an ephemeral port inside a fresh temp git workspace, waits for
# ~/.forge/daemon.addr, drives six prompts through `forge run --json` in one
# sustained session (write file, read back, git status, shell exec, rewrite,
# commit), prints a PASS/FAIL table with latency and token usage, verifies
# artifacts on disk and in git history, stops the daemon.
#
# Usage: scripts/run-e2e.sh [model] [base_url]
#   model     default qwen2.5-coder:7b
#   base_url  default http://127.0.0.1:11434/v1
#
# JSON field extraction uses grep/sed (no jq dependency); the script reads
# only the fixed numeric/identifier fields of OneShotResult.

set -u

MODEL="${1:-qwen2.5-coder:7b}"
BASE_URL="${2:-http://127.0.0.1:11434/v1}"
REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

WORK=""
FAILURES=0
SAVED_HOME="${HOME-}"

cleanup() {
    if [ -n "${SERVER_PID:-}" ] && kill -0 "$SERVER_PID" 2>/dev/null; then
        kill -9 "$SERVER_PID" 2>/dev/null || true
    fi
    if [ -n "$SAVED_HOME" ]; then export HOME="$SAVED_HOME"; else unset HOME; fi
}
trap cleanup EXIT

fail() { echo "FAIL: $*" >&2; FAILURES=$((FAILURES + 1)); }

echo "== Building forge..."
WORK="$(mktemp -d "${TMPDIR:-/tmp}/forge-e2e-XXXXXX")"
mkdir -p "$WORK/bin"
go build -o "$WORK/bin/forge" "$REPO_ROOT/cmd/forge" || { fail "go build"; exit 1; }
FORGE="$WORK/bin/forge"

export HOME="$WORK/home"
mkdir -p "$HOME"
WS="$WORK/ws"
mkdir -p "$WS"

echo "== Preparing temp git workspace..."
git -C "$WS" init >/dev/null || { fail "git init (is git installed?)"; exit 1; }
git -C "$WS" config user.email "e2e@forge.local"
git -C "$WS" config user.name "Forge E2E"

mkdir -p "$HOME/.forge"
cat > "$HOME/.forge/config.json" <<EOF
{
  "schema_version": 3,
  "default_provider": "ollama",
  "providers": {
    "ollama": {
      "kind": "openai-compatible",
      "base_url": "$BASE_URL",
      "models": ["$MODEL"]
    }
  },
  "storage": { "path": "$HOME/forge.db" },
  "network": { "allowed_hosts": ["127.0.0.1", "localhost"] },
  "logging": { "level": "info" },
  "permissions": {
    "fs": { "read": ["./**"], "write": ["./**"] },
    "shell": { "allow": ["go"], "require_isolation": true },
    "git": { "allow": ["status", "add", "commit", "log", "diff", "branch",
                        "switch", "stash", "restore", "show", "remote", "fetch"] }
  }
}
EOF

echo "== Starting forge serve (ephemeral port)..."
(cd "$WS" && exec "$FORGE" serve) >"$WORK/serve.out.log" 2>"$WORK/serve.err.log" &
SERVER_PID=$!

ADDR_FILE="$HOME/.forge/daemon.addr"
for _ in $(seq 1 150); do
    [ -s "$ADDR_FILE" ] && break
    if ! kill -0 "$SERVER_PID" 2>/dev/null; then
        fail "forge serve exited early; log: $WORK/serve.err.log"
        exit 1
    fi
    sleep 0.2
done
if [ ! -s "$ADDR_FILE" ]; then
    fail "timeout waiting for $ADDR_FILE"
    exit 1
fi
echo "   daemon at $(cat "$ADDR_FILE")"

SESSION_ID=""

# json_num FIELD FILE -> prints the numeric value or 0.
json_num() {
    sed -n 's/.*"'"$1"'"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p' "$2" | head -n 1
}

run_turn() {
    turn_id="$1"; desc="$2"; prompt="$3"
    args=(run --json)
    if [ -n "$SESSION_ID" ]; then args+=(--session "$SESSION_ID"); fi
    args+=("$prompt")

    start=$(date +%s%3N)
    if ! "$FORGE" "${args[@]}" >"$WORK/turn.json" 2>>"$WORK/serve.err.log"; then
        rc=1
    else
        rc=0
    fi
    end=$(date +%s%3N)
    ms=$((end - start))

    resp_len=$(sed -n 's/.*"response"[[:space:]]*:[[:space:]]*"\(.*\)".*/x/p' "$WORK/turn.json" | head -n 1 | wc -c)
    sid=$(sed -n 's/.*"session_id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$WORK/turn.json" | head -n 1)
    tokens=$(json_num total_tokens "$WORK/turn.json"); tokens=${tokens:-0}

    ok=PASS
    if [ "$rc" -ne 0 ] || [ "$resp_len" -le 1 ]; then
        ok=FAIL
        FAILURES=$((FAILURES + 1))
        echo "   turn $turn_id raw output:" >&2
        head -c 400 "$WORK/turn.json" >&2
    fi
    if [ -z "$SESSION_ID" ] && [ -n "$sid" ]; then SESSION_ID="$sid"; fi

    printf '| %-4s | %-24s | %-4s | %8s ms | %7s tok |\n' "$turn_id" "$desc" "$ok" "$ms" "$tokens"
}

echo ""
echo "== E2E turns (model: $MODEL)"
printf '| %-4s | %-24s | %-4s | %10s | %9s |\n' Turn Step Result Latency Tokens
run_turn 1 "fs_write creates file" 'Use the fs_write tool to create a file named cli-notes.md whose entire content is exactly this single line: CLI_E2E_MARKER=zulu-7 . Then reply DONE.'
run_turn 2 "fs_read reads it back" 'Use the fs_read tool to read cli-notes.md, then reply with the exact value of CLI_E2E_MARKER.'
run_turn 3 "git status"            'Use the git tool with subcommand status to show the repository status, then summarize it in one sentence.'
run_turn 4 "shell_exec go version" 'Use the shell_exec tool to run the command go with argument version, then reply with the exact output.'
run_turn 5 "fs_write rewrites"     'Use the fs_write tool to rewrite cli-notes.md so its entire content becomes exactly two lines: CLI_E2E_MARKER=zulu-7 and updated-by-cli-run . Then reply DONE.'
run_turn 6 "git add + commit"      'Commit the change using the git tool twice: first subcommand add with argument cli-notes.md, then subcommand commit with commit message cli-e2e-commit . Reply with the confirmation.'

ARTIFACT_OK=1
if ! grep -q "zulu-7" "$WS/cli-notes.md" 2>/dev/null \
   || ! grep -q "updated-by-cli-run" "$WS/cli-notes.md" 2>/dev/null; then
    fail "cli-notes.md missing or wrong content in workspace"
    ARTIFACT_OK=0
fi
subject="$(git -C "$WS" log -1 --format=%s 2>/dev/null)"
if [ "$subject" != "cli-e2e-commit" ]; then
    fail "expected HEAD subject cli-e2e-commit, got: $subject"
    ARTIFACT_OK=0
fi
echo ""
echo "Artifact checks: $([ "$ARTIFACT_OK" = 1 ] && echo PASS || echo FAIL)"
[ -n "$SESSION_ID" ] && echo "Session persisted across all six turns ($SESSION_ID)."

if [ "$FAILURES" -gt 0 ]; then
    echo "RESULT: FAIL ($FAILURES failure(s))"
    exit 1
fi
echo "RESULT: PASS"
