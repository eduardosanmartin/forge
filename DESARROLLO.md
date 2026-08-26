# forge — Guía de Desarrollo (MVP v0)

Documentación interna para contribuir al código base de forge. Inglés para código/identificadores/comentarios; esta guía en español por ser documento contextual del equipo.

---

## 1. Arquitectura General (resumen)

```
forge (binario único)
├── cmd/forge/main.go                 # entry point, dispatch aislamiento
├── internal/
│   ├── agent/          # loop agente + ensamblado contexto + métricas
│   ├── client/         # cliente WebSocket JSON-RPC (chat/run/attach)
│   ├── cli/            # comandos cobra (serve/chat/run/attach/halt/…)
│   ├── config/         # config versionado, migraciones, validación
│   ├── daemon/         # servidor WebSocket JSON-RPC 2.0 + session mgr
│   ├── e2e/            # tests live/offline contra criterio de salida
│   ├── isolation/      # Landlock+seccomp (Linux) / no-op (otros)
│   ├── llm/            # Provider interface + Ollama adapter + registry
│   ├── logging/        # slog JSON + redacción secrets
│   ├── pathmatch/      # glob doublestar (fs perms + config validation)
│   ├── perms/          # deny-by-default engine + git floor
│   ├── store/          # SQLite sessions/messages (WAL, migraciones)
│   ├── tools/          # registry + fs/shell/git tools (MCP-shape)
│   ├── version/        # versión/build info (ldflags)
│   └── pathmatch/      # glob matcher compartido
├── configs/            # ejemplo .forge/config.json + README
├── scripts/            # run-e2e.ps1 / run-e2e.sh
├── GUÍA.md             # guía usuario (español)
├── CONFIGURACIÓN.md    # referencia config (español)
├── DESARROLLO.md       # este archivo
└── spec-harness-agentic.md v0.8  # spec fuente (READ-ONLY)
```

---

## 2. Convenciones de Código

- **Idioma**: código, identificadores, comentarios, strings de error → **English**.
- **Documentos contextuales** (GUÍA, CONFIGURACIÓN, DESARROLLO, README) → **Español (rioplatense)**.
- **Errores**: `fmt.Errorf("context: %w", err)` siempre con `%w`.
- **Panic**: nunca en código de librería; solo en `main` tras log fatal.
- **Tests**: `testing` stdlib (sin testify); table-driven; `-race` cuando haya gcc.
- **Formato**: `gofmt` obligatorio (`gofmt -l .` debe imprimir nada).
- **Vet**: `go vet ./...` sin warnings.
- **Imports**: stdlib primero, luego deps externos (alfabético dentro de cada grupo).

---

## 3. Flujo de Trabajo (Workflow)

### 3.1 Ramas y Commits

- **main** = rama protegida (push directo solo via commits aprobados).
- **Work units** = unidades atómicas de trabajo (WU1…WU10). Cada WU = 1 commit convencional.
- **Convención**: `feat:`, `fix:`, `chore:`, `test:` (Conventional Commits, inglés).
- **Nunca** `Co-Authored-By` ni atribución IA.
- **Checkpoint**: cada commit requiere aprobación explícita del operador (llega notificación móvil).

### 3.2 Tests

```bash
# Suite completa (default, sin Ollama live)
go test -count=1 ./...

# Live E2E (requiere Ollama corriendo + qwen2.5-coder:7b)
FORGE_E2E_LIVE=1 go test -count=1 -timeout 2700s ./internal/e2e/ -run "Live"

# Solo un test específico
go test -count=1 -v ./internal/e2e/ -run "TestLive_SustainedConversationWithTools"
```

- **Timeouts generosos**: turnos CPU contra 7B pueden tardar 60–330s; `-timeout 2700s` para sustained.
- Tests live corren **solo** con `FORGE_E2E_LIVE=1`; suite default (CI) corre offline con mocks.

### 3.3 Cross-Compile Linux (verificación WU9+)

```bash
$env:GOOS="linux"; go build ./...; go vet ./...; go test -c ./internal/isolation/...; $env:GOOS=$null
```

- Los tests `//go:build !windows` no corren en Windows; `go vet` con `GOOS=linux` verifica que compilan.

---

## 4. Estructura de Paquetes Clave (para navegar)

| Paquete | Responsabilidad | Archivos clave |
|---------|-----------------|----------------|
| `internal/daemon` | Servidor WebSocket JSON-RPC 2.0, session manager, emergency halt | `transport.go`, `session_mgr.go`, `handler.go`, `emergency.go` |
| `internal/agent` | Loop agente, ensamblado contexto (system→tools→memoria→historia), métricas | `loop.go`, `context.go`, `metrics.go` |
| `internal/tools` | Registry + fs/shell/git tools (MCP-shape) + fencing + redacción | `registry.go`, `fs.go`, `shell.go`, `git.go`, `fencing.go`, `workdir.go` |
| `internal/isolation` | Landlock+seccomp (Linux) / no-op (otros) + wrapper re-exec | `linux.go`, `other.go`, `isolation.go`, `wrap.go` |
| `internal/llm` | Provider interface + Ollama adapter + registry hot-swap | `provider.go`, `ollama.go`, `registry.go` |
| `internal/store` | SQLite sessions/messages (WAL, migraciones embed) | `store.go`, `migrate.go`, `migrations/001.sql` |
| `internal/perms` | Deny-by-default engine + git floor | `perms.go`, `gitfloor.go`, `audit.go` |
| `internal/client` | WebSocket JSON-RPC client + REPL + one-shot | `client.go`, `repl.go`, `oneshot.go` |
| `internal/e2e` | Tests live/offline + harness compartido | `e2e_live_test.go`, `e2e_offline_test.go`, `harness_test.go` |

---

## 4. Patrones Importantes (para no romper)

### 4.1 MCP-shape Tool Interface
```go
type Tool interface {
    Name() string
    Description() string
    JSONSchema() map[string]any
    Execute(ctx context.Context, req perms.Request) (Result, error)
}
```
- `Execute` recibe `perms.Request` (ya validado por registry contra schema + perms engine).
- **Nunca** hagas I/O directo en tools sin pasar por `tools.Registry.Execute` (hace perms check + fencing + redaction).

### 4.2 Fencing RNF-4.5 (obligatorio)
Todo resultado de tool que vuelve al modelo **debe** envolverse:
```
<<TOOL_RESULT:tool.name>>
<CONTENT>
...contenido...
</CONTENT>
</TOOL_RESULT:tool.name>
```
- `tools.registry.Execute` lo aplica **automáticamente** + `logging.Redact()` antes de devolver.

### 4.3 Permisos (Deny-by-Default)
- `perms.Engine.Check(req)` → `Decision{Allowed, Rule}`.
- **Orden**: malformed → git floor → allowlist → default-deny.
- **Nunca** asumas permitido; siempre chequea.

### 4.4 Redacción (RNF-4.4)
```go
import "github.com/eduardosanmartin/forge/internal/logging"
redacted := logging.Redact(rawString)
```
- Aplicar a **todo** string que vaya a logs o al modelo (tool output, args, URLs, etc.).

### 4.4 Workdir Validation (WU10 fix)
```go
import "github.com/eduardosanmartin/forge/internal/tools"
resolved, err := tools.ValidateWorkdir(workdir) // devuelve abs o error accionable
```
- Usar en `git.go` y `shell.go` **antes** de ejecutar.
- Devuelve error accionable: `"workdir /workspace does not exist... RETRY OMITTING workdir..."`.

### 4.5 System Prompt (agente)
```go
const systemPrompt = `... When a tool argument has a default: OMIT it entirely instead of inventing values...`
```
- Está en `internal/agent/context.go` (`systemPrompt` const).
- Cambios aquí afectan a **todos** los modelos; testeá con live E2E.

---

## 5. Testing Patterns

### 5.1 Mock Server (LLM)
```go
mock := llm.NewMockServer()
mock.SetDefaultResponse(&llm.MockResponse{StatusCode: 200, Body: rawJSON})
// or
mock.SetHandler("/v1/chat/completions", customHandler)
```

### 5.2 Harness E2E Stack
```go
s := newStack(t, baseURL, []string{model})
sess := s.createSession()
res, dur := s.executeTurn(sess, "prompt")
```
- `newStack` crea temp workspace + git init + chdir + store + llmReg + toolsReg + daemon transport + client.
- `executeTurn` devuelve `ExecuteTurnResult` + duración.

### 5.3 Assertions de Turno
```go
stat := summarizeTurnResult(i+1, res, dur)
assertTurnSane(t, stat, spec.tools...)  // chequea tokens >0, finalLen>0 si sin tools
if spec.anyTool && len(stat.tools) == 0 { t.Errorf(...) }
if spec.check != nil { spec.check(t) }
```

---

## 5. Debugging Común

| Síntoma | Causa típica | Fix |
|---------|--------------|-----|
| `close of closed channel` en transport | double-close `cc.done` | `sync.Once` en `closeDone()` (ya fix) |
| `unknown tool git` en test | Registry vacío | Usar `tools.NewDefaultRegistry` |
| `workdir /workspace` no existe | Modelo alucina path | `validateWorkdir()` + descripción schema + system prompt |
| `usage` / `tool_calls` en cero | Falta json tags en structs respuesta | Ver `provider.go` tags snake_case |
| Allowlist deniega `127.0.0.1:11434` | Config sin puerto vs exact match | Entrada sin puerto matchea hostname en cualquier puerto |

---

## 6. Dependencias (actuales)

| Dep | Versión | Uso | Nota |
|-----|---------|-----|------|
| `github.com/spf13/cobra` | v1.10.2 | CLI | Transitivo: pflag, mousetrap |
| `github.com/coder/websocket` | v1.8.15 | WebSocket transport | Pure Go |
| `modernc.org/sqlite` | v1.57.0 | SQLite driver | Pure Go, no CGO |
| `golang.org/x/sys` | v0.47.0 | Landlock syscalls | Pure Go |
| `github.com/elastic/go-seccomp-bpf` | v1.6.0 | BPF filter assembly | Pure Go (archived pero releases 2025) |
| `golang.org/x/net` | v0.41.0 | Indirect (de go-seccomp-bpf) | — |

**Regla**: CERO deps nuevas sin aprobación del operador (checkpoint WU9 fue la única excepción).

---

## 7. Cómo Añadir una Nueva Tool

1. Crear `internal/tools/nueva.go` implementando `Tool` interface.
2. Añadir constructor `newNuevaTool()` en `registry.go` → `defaultRegistryTools()`.
3. Registrar en `NewDefaultRegistry` (ya lo hace el loop).
4. Añadir permisos en `config.PermissionsPolicy` (nuevo campo o extender existente).
4. Tests: `*_test.go` con mock perms + asserts de fencing/redaction.
5. Actualizar `CONFIGURACIÓN.md` y `GUÍA.md` si es user-facing.

---

## 8. Versionado y Release

- **Versión**: `internal/version` (`Version`, `Commit`, `Date` via ldflags).
- **Build**: `go build -ldflags "-X github.com/eduardosanmartin/forge/internal/version.Version=v0.1.0 -X ...Commit=$(git rev-parse --short HEAD) -X ...Date=$(date -u +%Y-%m-%d)" -o forge ./cmd/forge`
- **Tag**: `git tag v0.1.0` tras commit aprobado.

---

## 9. Referencias Rápidas (comandos útiles)

```bash
# Build + test suite completo
go build ./... && go vet ./... && gofmt -l . && go test -count=1 ./...

# Solo paquete específico
go test -count=1 -v ./internal/agent/...

# Cross-compile Linux (verificación WU9+)
$env:GOOS="linux"; go build ./...; go vet ./...; $env:GOOS=$null

# Live E2E individual
FORGE_E2E_LIVE=1 go test -count=1 -timeout 1500s -v ./internal/e2e/ -run "TestLive_PermissionDenial"

# Script E2E completo (Windows)
.\scripts\run-e2e.ps1 -Model qwen2.5-coder:7b
```

---

## 10. Checklist Pre-Commit (antes de pedir aprobación)

- [ ] `go build ./...` OK
- [ ] `go vet ./...` OK
- [ ] `gofmt -l .` → vacío
- [ ] `go test -count=1 ./...` todo verde
- [ ] `$env:GOOS="linux"; go build ./...; go vet ./...; $env:GOOS=$null` OK
- [ ] Tests live pasan (si tocás runtime/core): `FORGE_E2E_LIVE=1 go test -count=1 -timeout 2700s ./internal/e2e/ -run "Live"`
- [ ] Docs actualizadas (`GUÍA.md`, `CONFIGURACIÓN.md`, `README.md` si aplica)
- [ ] Commit message convencional (`feat:`, `fix:`, etc.) en inglés
- [ ] Sin `Co-Authored-By`, sin secretos en diff

---

## 11. Contacto / Escalación

- **Spec**: `spec-harness-agentic.md` v0.8 (fuente de verdad).
- **Operador**: checkpoints vía notificación móvil (1 commit = 1 aprobación).
- **Architecture decisions**: registradas en `sdd/forge-v0/*` (engram) + `DECISIONS.md` si crece.

---

---

## 12. Punto de Rollback MVP v0 (Base para v1)

**Commit de referencia**: `b8ef0de` (HEAD, `origin/main`)

Este commit representa el **MVP v0 completo y verificado**:

- Todos los 15 commits están en `main` y pusheados a `origin/main`.
- Criterio de salida §6 probado **live** contra `qwen2.5-coder:7b`.
- Stack completo: config v3, perms engine, MCP-shape tools, Ollama adapter, SQLite store, daemon JSON-RPC/WebSocket, agent loop + métricas, REPL + one-shot clients, Landlock+seccomp isolation (validación runtime pendiente en Linux Perfil B).
- Docs completas: README, GUÍA.md, CONFIGURACIÓN.md, DESARROLLO.md, ERRORES.md, BITÁCORA.md.

### Tag explícito (recomendado)

```bash
git tag v0.0-mvp-complete
git push origin v0.0-mvp-complete
```

### Cómo volver a este punto (Rollback)

Si al desarrollar v1 algo falla y querés volver a este estado exacto:

```bash
# Opción 1: por commit hash (siempre funciona)
git checkout b8ef0de

# Opción 2: por tag explícito (si hiciste el push del tag)
git checkout v0.0-mvp-complete
```

Desde ahí podés:

```bash
# Crear rama de experimentación
git checkout -b v1-experimento

# Hacer ajustes, probar, commitear
git commit -m "feat: ajustar estrategia v1..."

# Si querés descartar y volver otra vez:
git checkout b8ef0de
```

> **Nota**: El commit `b8ef0de` está **pusheado a `origin/main`**. Si hiciste `git tag v0.0-mvp-complete && git push origin v0.0-mvp-complete`, el tag también está en el remoto y cualquiera puede clonar y hacer `git checkout v0.0-mvp-complete` directamente.

> **Importante**: Este es el punto base para v1. Según el principio de bootstrapping (§0 spec), **v1 se construye 100% con forge v0 + modelo local** (sin OpenCode, sin OpenChamber, sin herramientas externas). Si algo no funciona en v1, volvé a `b8ef0de` y ajustá la estrategia antes de seguir.