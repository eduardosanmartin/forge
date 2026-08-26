# forge — Referencia de Configuración (MVP v0)

Esquema versión **3** (`schema_version: 3`). La configuración se carga en tres capas con precedencia estricta:

```
defaults (hardcoded)  →  global (~/.forge/config.json)  →  proyecto (.forge/config.json)
```

Los archivos se parsean con **JSON estricto** (`DisallowUnknownFields`); cualquier campo desconocido hace fallar el arranque con mensaje claro indicando archivo y campo.

---

## 1. Estructura Completa (JSON)

```json
{
  "schema_version": 3,
  "default_provider": "ollama",
  "providers": {
    "ollama": {
      "kind": "openai-compatible",
      "base_url": "http://127.0.0.1:11434/v1",
      "models": ["qwen2.5-coder:7b"]
    }
  },
  "storage": {
    "path": "~/.forge/forge.db"
  },
  "network": {
    "allowed_hosts": ["127.0.0.1", "localhost"]
  },
  "logging": {
    "level": "info",
    "file": ""
  },
  "permissions": {
    "fs": {
      "read": ["./**"],
      "write": ["./**"]
    },
    "shell": {
      "allow": ["go", "git"],
      "require_isolation": true
    },
    "git": {
      "allow": [
        "status", "add", "commit", "log", "diff",
        "branch", "switch", "stash", "restore",
        "show", "remote", "fetch"
      ]
    }
  }
}
```

---

## 2. Campos por Sección

### `schema_version` (integer, obligatorio)
Versión del esquema que entiende este build. Valor actual: **3**.
- Ausente → se asume 1 (compatibilidad hacia atrás).
- Valor > actual o < 1 → error al cargar con mensaje: `unsupported schema version X (supported: 1..3)`.
- Migraciones automáticas 1→2→3 se aplican al cargar (ver §4).

### `default_provider` (string)
Clave del proveedor por defecto (debe existir en `providers`). Ej: `"ollama"`.

### `providers` (map[string]Provider)
Mapa de proveedores nombrados. Cada entrada:

```json
{
  "kind": "openai-compatible",
  "base_url": "http://127.0.0.1:11434/v1",
  "models": ["qwen2.5-coder:7b", "llama3.1:8b-instruct"]
}
```

| Campo | Tipo | Requerido | Descripción |
|-------|------|-----------|-------------|
| `kind` | string | Sí | Solo `"openai-compatible"` en v0 |
| `base_url` | string | Sí | URL absoluta HTTP/HTTPS al endpoint `/v1` (ej. `http://127.0.0.1:11434/v1`) |
| `models` | []string | Sí | Al menos un nombre de modelo disponible en ese endpoint |

> **Validación**: `base_url` debe parsearse como URL absoluta http/https; `models` no puede estar vacío.

### `storage` (StorageConfig)

```json
{ "path": "~/.forge/forge.db" }
```

| Campo | Tipo | Descripción |
|-------|------|-------------|
| `path` | string | Ruta al archivo SQLite. Soporta `~/` (se expande a `$HOME`). Directorio padre se crea automáticamente. |

### `network` (NetworkConfig) — RNF-4.9

```json
{ "allowed_hosts": ["127.0.0.1", "localhost"] }
```

| Campo | Tipo | Descripción |
|-------|------|-------------|
| `allowed_hosts` | []string | Lista de hosts:puertos permitidos para **cualquier** egress del adaptador LLM. Coincidencia **exacta** host:port. Entrada **sin puerto** (p.ej. `"127.0.0.1"`) matchea ese hostname en **cualquier puerto**. Lista vacía = **denegar todo egress** (deny-by-default). |

> **Ejemplos**:
> - `["127.0.0.1"]` → permite `127.0.0.1:11434`, `127.0.0.1:8080`, etc.
> - `["127.0.0.1:11434"]` → **solo** ese puerto exacto.
> - `[]` → **deniega todo** (el adaptador falla al construir).

### `logging` (LoggingConfig)

```json
{ "level": "info", "file": "" }
```

| Campo | Tipo | Valores válidos | Descripción |
|-------|------|-----------------|-------------|
| `level` | string | `debug`, `info`, `warn`, `error` | Nivel mínimo de log. Vacío → `info`. |
| `file` | string | ruta absoluta o `""` | Si no vacío, escribe **además** de stderr en ese archivo (append, create). `""` → solo stderr. |

### `permissions` (PermissionsPolicy) — RNF-4.1, RNF-4.7, RNF-4.9

#### `fs` (FSPermissions)
Patrones glob estilo **doublestar** (soporta `**`, `*`, literales). Resueltos contra la **raíz del workspace**.

```json
{ "read": ["./**"], "write": ["./**"] }
```

| Lista | Qué controla |
|-------|--------------|
| `read` | Rutas permitidas para `fs.read` / `fs.list` |
| `write` | Rutas permitidas para `fs.write` |

**Reglas de matching**:
- Patrones **relativos** (sin `/` inicial) → se matchean contra la ruta **relativa al workspace** (normalizada a `/`).
- Patrones **absolutos** (empiezan con `/` en POSIX o `C:/` en Windows) → matchean contra la ruta **absoluta** normalizada.
- Una ruta que **escapa** del workspace (p.ej. `../../etc/passwd`) → **auto-denegada** salvo que un patrón absoluto explícito la autorice (escape hatch documentado).
- Orden: **primera coincidencia gana** (lista ordenada = prioridad).

#### `shell` (ShellPermissions)

```json
{ "allow": ["go", "git"], "require_isolation": true }
```

| Campo | Tipo | Descripción |
|-------|------|-------------|
| `allow` | []string | **Basenames** de ejecutables permitidos (case-insensitive en todas las plataformas). `["go", "git"]` permite `go`, `GO.EXE`, `Git.exe`, `./go`, `/usr/bin/go`, etc. Vacío = **denegar toda shell**. |
| `require_isolation` | bool | **Solo Linux (Perfil B)**. `true` = exige Landlock+seccomp; si el kernel no los soporta, la shell se niega a ejecutar (denegación segura). En macOS/Windows se **ignora** (ver spec §6, aclaración 2). Default: `true`. |

> **Patrones permitidos**: entrada exacta del basename. No hay globbing intencional (evita `rm*` match `rm -rf`). Entradas vacías o solo espacios → error al validar.

#### `git` (GitPermissions)

```json
{ "allow": ["status", "add", "commit", "log", "diff", "branch", "switch", "stash", "restore", "show", "remote", "fetch"] }
```

| Campo | Tipo | Descripción |
|-------|------|-------------|
| `allow` | []string | Subcomandos git permitidos (case-sensitive, convención lowercase). `"COMMIT"` ≠ `"commit"`. |

**Piso de seguridad no configurable (RNF-8.2 espíritu)**: los siguientes **siempre se deniegan** aunque estén en `allow`:
- `push --force`, `push -f` (incluye clusters `-ff`)
- `reset --hard`
- `clean` (cualquier variante, incluso `-n` dry-run)
- `branch -D`, `branch --delete` (pero `branch -d` permitido)

---

## 3. Precedencia y Merge

Al cargar:

1. Se parte de `Defaults()` hardcodeados.
2. Si existe `~/.forge/config.json` → merge **field-group-wise** (secciones completas: `providers` reemplaza mapa entero; `fs.read` reemplaza lista entera; escalares reemplazan).
3. Si existe `.forge/config.json` (o `--config <path>`) → mismo merge encima del paso anterior.
4. `Validate()` final: unknown fields = error; validaciones de §2.

**Ejemplo**: global tiene `shell.allow: ["go"]`; proyecto agrega `shell.allow: ["npm"]` → resultado final `["npm"]` (reemplazo total de lista). Para sumar, redeclará la lista completa en el proyecto.

---

## 4. Migraciones de Esquema

| De → A | Qué hace |
|--------|----------|
| 1 → 2 | Inyecta bloque `permissions` con defaults (deny-by-default + git allowlist curada). |
| 2 → 3 | Añade `permissions.shell.require_isolation: true` (default seguro). |

- `Migrate(data []byte, from int) ([]byte, error)` se ejecuta **automáticamente** en `Load()` si `schema_version` del archivo < `CurrentSchemaVersion` (3).
- Archivo **no se reescribe** hasta que llamás `Save()` explícitamente.
- `Migrate` para versiones desconocidas o salto >1 devuelve error descriptivo.

---

## 5. Validaciones Críticas (resumen)

| Validación | Qué falla |
|------------|-----------|
| Unknown fields | Cualquier campo no definido en el schema |
| `schema_version` ausente o 0/>3 | Error con rango soportado |
| `default_provider` vacío o no existe en `providers` | Error |
| Provider `kind` ≠ `"openai-compatible"` | Error (v0 solo soporta este kind) |
| `base_url` no parseable como URL absoluta http/https | Error |
| `models` vacío | Error |
| `storage.path` vacío tras expandir `~` | Error |
| `logging.level` ∉ {debug,info,warn,error} | Error |
| `fs.read`/`write` patrón inválido (vacío, `..`, backslash, `//`) | Error |
| `shell.allow` / `git.allow` entrada vacía | Error |
| `network.allowed_hosts` entrada vacía | Error |

---

## 5. Ejemplo Mínimo Válido (proyecto)

```json
{
  "schema_version": 3,
  "default_provider": "ollama",
  "providers": {
    "ollama": {
      "kind": "openai-compatible",
      "base_url": "http://127.0.0.1:11434/v1",
      "models": ["qwen2.5-coder:7b"]
    }
  },
  "storage": { "path": "~/.forge/forge.db" },
  "network": { "allowed_hosts": ["127.0.0.1"] },
  "logging": { "level": "info" },
  "permissions": {
    "fs": { "read": ["./**"], "write": ["./**"] },
    "shell": { "allow": ["go"], "require_isolation": true },
    "git": { "allow": ["status","add","commit","log","diff"] }
  }
}
```

---

## 6. Variables de Entorno (solo runtime, no config)

| Variable | Efecto |
|----------|--------|
| `FORGE_E2E_LIVE=1` | Habilita tests live E2E contra Ollama real (omitidos por defecto) |
| `FORGE_ISOLATION_CHILD=1` | Usado internamente por el wrapper de aislamiento (no tocar) |

---

## 7. Archivos de Ejemplo

| Archivo | Contenido |
|---------|-----------|
| `configs/forge.json.example` | Config de proyecto completa con todos los campos y comentarios en `README.md` adjunto |
| `configs/README.md` | Explicación de precedencia, campos, y cómo versionar en git (RNF-7.2) |

---

> **Recordatorio**: esta config rige **todo** lo que el agente puede hacer. La postura es **deny-by-default**: si no está explícitamente en las listas `allow`, la acción se deniega y el modelo recibe `DENIED: <rule>`. Revisá y ajustá las listas antes de usar `forge` en proyectos sensibles.