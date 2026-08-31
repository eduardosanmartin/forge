# forge configuration example

`forge.json.example` mirrors the built-in defaults. Copy it next to your code
to make a project-scoped, versionable configuration (spec RNF-7.2):

    mkdir .forge
    cp configs/forge.json.example .forge/config.json

## Precedence

Later sources override earlier ones, field-group-wise:

1. Built-in defaults (`internal/config.Defaults`).
2. Global file: `~/.forge/config.json`.
3. Project file: `.forge/config.json`, or `--config <path>` when given.

Whole sections replace when present; named provider entries replace
wholesale per provider; within `permissions`, each subsection (`fs`,
`shell`, `git`) replaces wholesale when present. Missing files are skipped
silently. Documents on older schema versions are migrated forward at load.

## Fields

- `schema_version`: config schema revision (currently `3`). Older versions
  are migrated forward automatically; newer versions are rejected.
- `default_provider`: provider selected when a task does not pin one.
- `providers`: map of name to `{kind, base_url, models}`. Only
  `"openai-compatible"` is supported today.
- `storage.path`: local database location; a leading `~/` expands to your
  home directory.
- `network.allowed_hosts`: hosts outbound requests may target. An empty list
  means deny-all egress.
- `logging.level`: one of `debug`, `info`, `warn`, `error`.
  `logging.file`: optional extra log destination (stderr always receives logs).
- `permissions`: deny-by-default tool permission policy (RNF-4.1). Anything
  not listed is refused:
  - `permissions.fs.read` / `permissions.fs.write`: glob patterns matched
    against workspace-relative paths (`./**`, `src/**`, `*.go`). Absolute,
    forward-slashed patterns (`/data/**`, `C:/data/**`) are the explicit
    escape hatch for authorized locations outside the workspace.
  - `permissions.shell.allow`: executable base names the agent may run.
    The default list is EMPTY — deny-by-default (RNF-4.1). To let the agent
    run builds and tests, add entries like `"go"`, `"npm"`, `"make"`:

        "shell": { "allow": ["go", "npm"] }

  - `permissions.shell.require_isolation`: when `true` (the default since
    schema v3, RNF-4.7), forge refuses `shell_exec` on **Linux** if OS-level
    isolation is unavailable instead of silently running with the permission
    model alone. Shell commands always run through an isolation wrapper on
    Linux (Landlock filesystem bounds + a default-deny seccomp filter, no
    networking) regardless of this flag — it only controls whether missing
    isolation is fatal. On macOS and Windows the flag is ignored: those
    platforms are permissions-only in v0 per spec §6, and forge logs that
    at startup.
  - `permissions.git.allow`: allowed git subcommands (lowercase). Destructive
    operations — force-push, `reset --hard`, `clean`, forced branch deletion —
    stay blocked by forge's non-configurable safety floor (RNF-8.2) no matter
    what this list contains.

## Migration

Documents on schema v1 or v2 are migrated forward automatically at load:
v1 gains the default `permissions` section, and v2 gains
`permissions.shell.require_isolation: true` (an explicitly written `false`
is preserved). Files on disk are never rewritten unless you save them.

## Strictness

Unknown JSON fields are rejected, so typos fail fast instead of being
silently ignored. Invalid permission glob patterns fail validation with the
offending entry named.
