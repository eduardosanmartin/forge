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
wholesale per provider. Missing files are skipped silently.

## Fields

- `schema_version`: config schema revision (currently `1`). Other versions
  are rejected; forward migrations will land here.
- `default_provider`: provider selected when a task does not pin one.
- `providers`: map of name to `{kind, base_url, models}`. Only
  `"openai-compatible"` is supported today.
- `storage.path`: local database location; a leading `~/` expands to your
  home directory.
- `network.allowed_hosts`: hosts outbound requests may target. An empty list
  means deny-all egress.
- `logging.level`: one of `debug`, `info`, `warn`, `error`.
  `logging.file`: optional extra log destination (stderr always receives logs).

## Strictness

Unknown JSON fields are rejected, so typos fail fast instead of being
silently ignored.
