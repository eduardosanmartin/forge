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

- `schema_version`: config schema revision (currently `2`). Older versions
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

  - `permissions.git.allow`: allowed git subcommands (lowercase). Destructive
    operations — force-push, `reset --hard`, `clean`, forced branch deletion —
    stay blocked by forge's non-configurable safety floor (RNF-8.2) no matter
    what this list contains.

## Strictness

Unknown JSON fields are rejected, so typos fail fast instead of being
silently ignored. Invalid permission glob patterns fail validation with the
offending entry named.
