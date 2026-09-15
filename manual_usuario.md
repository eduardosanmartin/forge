# Manual de Usuario — Forge

Forge es un harness de desarrollo agéntico **local-first**: un daemon persistente que corre un agente LLM con acceso real a herramientas (filesystem, shell, git, GitHub) sobre tu workspace, bajo un modelo de permisos deny-by-default. Se controla desde CLI, un REPL, una TUI o una GUI web embebida — todos hablan el mismo protocolo JSON-RPC 2.0 sobre WebSocket contra el mismo daemon.

Este manual documenta el estado real del código en la rama `feat/spec-validate`, no un roadmap aspiracional. Donde algo está parcialmente implementado o tiene una limitación conocida, se aclara explícitamente.

---

## 1. Instalación

Requisitos: Go 1.26+, Git en el PATH. Opcionalmente `gh` (CLI de GitHub) si vas a usar la tool `github`, y Ollama si vas a usar modelos locales.

```bash
git clone <repo>
cd harness-code
go build -o forge.exe ./cmd/forge   # Windows
go build -o forge ./cmd/forge       # Linux/macOS
```

El binario resultante es autocontenido: la GUI web y las migraciones de base de datos están embebidas (`//go:embed`), no hay assets externos que copiar.

---

## 2. Configuración

Forge lee configuración en capas: **proyecto (`.forge/config.json`) > global (`~/.forge/config.json`) > defaults**. Los overrides son por sección — una sección presente en el archivo de proyecto reemplaza esa sección entera (no hace merge campo a campo dentro de ella, salvo excepciones documentadas como `providers`, que se reemplaza por nombre).

### 2.1 Estructura completa

```json
{
  "schema_version": 3,
  "default_provider": "ollama",
  "fallback_chain": ["openrouter/some-model"],
  "providers": {
    "ollama": {
      "kind": "openai-compatible",
      "base_url": "http://127.0.0.1:11434/v1",
      "models": ["qwen2.5-coder:7b"],
      "model_roles": { "generation": "qwen2.5-coder:7b", "cheap": "qwen2.5-coder:1.5b" },
      "api_key": "",
      "price_per_million_input_tokens": 0,
      "price_per_million_output_tokens": 0,
      "request_timeout_seconds": 0
    }
  },
  "storage": { "path": "~/.forge/forge.db" },
  "network": { "allowed_hosts": ["127.0.0.1", "localhost"] },
  "logging": { "level": "info", "file": "" },
  "permissions": {
    "fs": { "read": ["./**"], "write": ["./**"] },
    "shell": { "allow": ["go", "git"], "require_isolation": true },
    "git": { "allow": ["status", "add", "commit", "log", "diff"] },
    "github": { "allow": ["issue-list", "pr-list"] },
    "custom": { "deny": [], "allow": [] }
  },
  "tui": { "layout": "session", "palette": "ember", "sidebar": true },
  "llm": { "streaming": { "mode": "off" }, "cores": 0 },
  "limits": { "plugin_wasm_max_bytes": 2097152, "skill_file_max_bytes": 1048576 },
  "agent": { "max_iterations": 10, "max_turn_seconds": 300, "max_parallel_children": 2 },
  "project": { "sensitivity": "general", "spec_path": "" },
  "daemon": { "addr": "", "auth_token_hash": "", "tls_cert_file": "", "tls_key_file": "" }
}
```

### 2.2 Qué hace cada sección

- **`default_provider` / `providers`**: cada provider tiene un `kind` (`openai-compatible`, `anthropic`, `gemini`), una `base_url` y una lista de `models`. El primer modelo de la lista es el default de ese provider. `model_roles` mapea roles (`cheap`, `generation`, `reasoning`) a nombres de modelo concretos para el router de costos (ver §11). `api_key` viaja en texto plano en el archivo — **no comitees `.forge/config.json` con keys reales a un repo compartido** sin revisar `.gitignore` primero.
- **`network.allowed_hosts`**: allowlist de egress (RNF-4.9). Vacía = deniega todo. Una entrada sin puerto (`"openrouter.ai"`) matchea cualquier puerto de ese host; con puerto (`"127.0.0.1:11434"`) exige match exacto. Cualquier `base_url` de provider fuera de esta lista falla al construir el registry.
- **`permissions`**: ver §5, es el corazón del modelo de seguridad.
- **`daemon.addr`**: dirección fija para `forge serve` cuando no pasás `--addr` explícito (ver §3.2). Si está vacía, cada arranque elige un puerto efímero distinto.
- **`project.sensitivity`**: `general` | `regulado` | `datos-sensibles`. Es un techo (ceiling) que limita qué tan autónomo puede ser un run con manifiesto (RF-11) y activa el audit log tamper-evident (§13) cuando es `regulado` o `datos-sensibles`.
- **`agent.max_turn_seconds`**: default 300s (5 min). Si tu modelo es local y lento, subilo.
- **`providers.<name>.request_timeout_seconds`**: timeout HTTP para una llamada de chat completion a ese proveedor puntual. `0`/ausente cae al default de 900s (15 min) — el mismo valor fijo que antes se aplicaba a todos los proveedores por igual. Configuralo bajo (120-180) en un modelo local que sabés que responde rápido para que un turno colgado corte pronto en vez de bloquear la GUI en silencio 15 minutos; dejalo alto (o en el default) para un modelo remoto grande legítimamente lento.
- **`fallback_chain`**: opt-in, lista ordenada de `"provider/model"` a la que un turno recurre si el default falla por una razón transitoria (rate limit, outage, timeout de red). Ver §11.3 para el detalle completo.

---

## 3. Levantar el daemon

### 3.1 Arranque básico

```bash
forge serve
```

Al arrancar, la terminal muestra un banner con el logo ASCII de Forge y los créditos de autoría; el mismo banner se vuelve a imprimir al salir (Ctrl+C o por error). Después del banner, los logs estructurados (JSON, uno por línea) van a stderr.

### 3.2 Puerto fijo vs. efímero

Por defecto (`--addr 127.0.0.1:0`), el daemon elige un puerto libre al azar en cada arranque — cómodo para no chocar con nada, incómodo si querés un bookmark estable para la GUI. Para fijarlo:

```bash
forge serve --addr 127.0.0.1:8765
```

o, mejor, dejalo en la config del proyecto para no tener que acordarte del flag:

```json
{ "daemon": { "addr": "127.0.0.1:8765" } }
```

`--addr` explícito en la línea de comandos siempre gana sobre `daemon.addr` de la config.

### 3.3 Acceso remoto (opcional)

Por defecto el daemon solo escucha en loopback (`127.0.0.1`) sin autenticación — el comportamiento de siempre. Bindear a una dirección no-loopback (para acceder desde otra máquina) está bloqueado por un "safety floor" a menos que configures **ambas** cosas:

```bash
# 1. Setear una password (se guarda como SHA-256, nunca en texto plano)
printf '%s' 'mi-password' | forge daemon set-password

# 2. Levantar con TLS (certificado real, o autofirmado para redes privadas)
forge serve --addr 0.0.0.0:8765 --tls-self-signed
# o con un cert real:
forge serve --addr 0.0.0.0:8765 --tls-cert cert.pem --tls-key key.pem
```

Sin ambas cosas, `forge serve --addr 0.0.0.0:...` se niega a arrancar. Para volver a un daemon sin password: `forge daemon set-password --clear`.

El CLI (`forge run`, `forge chat`, etc.) manda la password guardada como header `Authorization: Bearer` vía la variable de entorno `FORGE_DAEMON_TOKEN`; la GUI web hace login por `POST /auth/login` (no puede mandar headers custom en el WebSocket del navegador) y recibe una cookie de sesión HttpOnly.

---

## 4. Formas de interactuar con el daemon

Todas hablan el mismo JSON-RPC — elegí la que te convenga según el contexto.

### 4.1 REPL interactivo

```bash
forge chat
```

Comandos dentro del REPL: `/model <nombre>` (hot-swap del modelo default del daemon — ver limitación en §11), `/sessions`, `/attach <id>`, `/new`, `/halt [id]`, `/resume <id>`, `/exit`.

### 4.2 Ejecución no interactiva (scripts / CI)

```bash
forge run "Creá un archivo hello.go que imprima Hola"
forge run --json "Creá un test para hello.go"   # stdout = solo JSON
```

Salida JSON (`OneShotResult`):
```json
{
  "session_id": "uuid", "model": "...",
  "response": "...", "tool_calls": [...],
  "usage": {"prompt_tokens":120,"completion_tokens":45,"total_tokens":165},
  "duration_ms": 4583
}
```

Códigos de salida: `0` éxito, `1` fallo de ejecución, `2` error de uso/flags.

Flags v1 opt-in: `--retrieval`, `--compaction`, `--anchoring`, `--routing`, `--skills` (ver §10-12).

### 4.3 GUI web

Servida por el propio daemon en `http://<addr>/` — no es un servidor separado. Ver §14 para el detalle completo de la interfaz.

### 4.4 TUI

```bash
forge tui
```

Interfaz de terminal a pantalla completa (layout configurable vía `tui.layout`/`tui.palette`/`tui.sidebar`).

### 4.5 Attach a una sesión existente

```bash
forge attach <session-id>
```

Reconecta un REPL a una sesión que ya existe (creada por otro cliente, por un run anterior, etc.), sin perder historial.

---

## 5. Modelo de permisos (deny-by-default)

**Todo está denegado salvo lo que declares explícitamente.** No hay modo "permitir todo" — es una decisión de diseño, no una omisión. Tipos de permiso (`perms.Kind`):

| Kind | Config | Qué controla |
|---|---|---|
| `KindFsRead` / `KindFsWrite` | `permissions.fs.read` / `.write` | Globs relativos al workspace (`./**`) o absolutos POSIX-rooted |
| `KindShell` | `permissions.shell.allow` | Lista de basenames de ejecutables permitidos (`"go"`, `"git"`) |
| `KindGit` | `permissions.git.allow` | Subcomandos git permitidos (`status`, `commit`, `worktree`, `branch`, ...) |
| `KindGitHub` | `permissions.github.allow` | Subcomandos de la tool `github` permitidos (`issue-list`, `pr-view`, ...) — deny-by-default separado del resto porque toca red |
| `KindCustom` | `permissions.custom.deny`/`.allow` | Piso de seguridad para tools de plugins WASM |

`permissions.shell.require_isolation: true` exige aislamiento a nivel OS (Landlock+seccomp) antes de ejecutar shell — solo implementado en Linux; en macOS/Windows el flag se ignora y rige solo el modelo de permisos declarativo.

---

## 6. Herramientas disponibles (tools)

El agente ve estas tools en cada turno (nombre — qué hace — permiso que necesita):

| Tool | Qué hace | Permiso |
|---|---|---|
| `fs_read` | Lee archivo (offset/limit, binario → base64) | `fs.read` |
| `fs_write` | Escribe archivo (atómico, create_dirs opcional) | `fs.write` |
| `fs_list` | Lista directorio (recursivo, glob) | `fs.read` |
| `shell_exec` | Ejecuta comando (timeout 120s, máx 300s) | `shell.allow` |
| `git` | Subcomando git | `git.allow` |
| `git_worktree_add/list/remove` | Worktrees git (deny-by-default, sin rutas fuera del repo, sin `--force`) | `git.allow` incluye `worktree` |
| `git_branch_task` | Crea `forge/task-<id>` desde HEAD, exige working tree limpio | `git.allow` incluye `branch` |
| `github` | Lee issues/PRs vía `gh` CLI (**solo lectura**: `issue-list`, `issue-view`, `pr-list`, `pr-view`) | `github.allow` |
| `retrieval_search` | Búsqueda semántica en historial (v1, requiere `--retrieval`) | — |
| `compaction_summarize` | Resumen jerárquico de historial (v1) | — |
| `anchoring_store/list/get/delete` | Hechos anclados persistentes (v1/v0, ver §12) | — |
| `spawn_subagent` | Turno hijo acotado en sesión ramificada (ver §9) | — |

Cada resultado de tool vuelve envuelto en bloques fenced (`<<TOOL_RESULT:name>>...`) con redacción automática de secretos (claves AWS, PEM, tokens `sk-...`/`ghp_...`) — el modelo no puede confundir salida de herramienta con instrucciones.

---

## 7. Gestión de sesiones

```bash
forge sessions                              # lista rápida
forge session list                          # lista con info de branching
forge session replay <id>                   # transcript agrupado por turnos + tokens
forge session branch <source> [--at seq]    # ramifica (copia completa o hasta seq)
forge session merge <source> --into <dest>  # append-tail, sin resolución de conflictos 3-way
forge session compare <a> <b>               # divergencia desde branch_at_seq (alias: diff)
forge session switch <id>                   # valida existencia
forge session success <id>                  # marca verificado por humano (RF-4.4)
forge session cost <id>                     # tokens + costo estimado (ver §15)
```

---

## 8. Parada de emergencia y resume

```bash
forge halt              # detiene TODO inmediatamente, desde cualquier cliente
forge halt <session-id> # detiene solo esa sesión
forge resume <id>       # reanuda una sesión detenida
```

El estado `halted` persiste en SQLite — sobrevive a reinicio del daemon.

---

## 9. Subagentes y fanout multi-modelo

El modelo puede invocar la tool `spawn_subagent` para crear un turno hijo en una **sesión ramificada** con su propio presupuesto (`max_iterations`, `token_budget`, `file_budget`) y, opcionalmente, un `provider`/`model` distinto al de la sesión padre. El hijo hereda el mismo piso de permisos que el padre — nunca más ancho. Profundidad máxima: 3 niveles.

Cuando **todas** las tool calls de una misma iteración son `spawn_subagent` (2 o más), se despachan en paralelo con un pool acotado (`agent.max_parallel_children`, default 2, rango 2-4); cualquier mezcla con otra tool cae a ejecución secuencial.

```bash
forge subagents list                        # topología: padre/hijos, profundidad, tarea
```

`forge fanout` corre la misma tarea en un hijo por modelo, útil para comparar respuestas:

```bash
forge fanout "implementá X" --models "ollama/qwen2.5-coder:7b,openrouter/nemotron-3.5-lightning:free"
forge session compare <parent> <child>      # divergencia lado a lado
```

---

## 10. Runs autónomos con manifiesto (RF-11)

```bash
forge run --manifest run.json [--yes] [--state-dir .]
forge run --manifest run.json --decompose   # sin "tasks": pide al modelo que las genere (ver 10.1)
forge run --manifest run.json --resume      # reanuda un run interrumpido
forge run --verify-audit                    # verifica el hash chain del audit log
```

Manifiesto mínimo (JSON):

```json
{
  "run_id": "mi-run-01",
  "mode": "checkpoint",
  "goal": "Implementar el endpoint X",
  "budget": { "max_wall_clock": "30m", "max_tokens": 50000, "max_iterations": 20 },
  "git": { "isolation": "worktree", "commit_per_task": true },
  "hitl": { "checkpoints": [{ "id": "cp1", "trigger": "before_merge", "required": true }] },
  "tasks": [
    { "id": "t1", "goal": "Crear el handler", "done_criteria": "cmd: go build ./...", "file_budget": "internal/api/handler.go", "model_hint": "cheap" }
  ]
}
```

Modos de autonomía (`mode`): `dry_run` (nunca escribe), `supervised`, `checkpoint`, `autonomous`. El techo `project.sensitivity` de la config puede limitar qué modo es válido. `git.isolation` (`worktree`|`branch`|`none`) aísla los cambios; `none` solo es válido en `supervised`/`dry_run`.

Cada `task` tiene:
- **`done_criteria`**: texto libre por defecto (nunca se verifica). Con el prefijo `"cmd: "` pasa a ser una **verificación mecánica**: el runner ejecuta el resto del string como un comando (sin shell — exactamente un programa + argumentos separados por espacio, sin `&&`/`|`/`;`/redirects) y solo marca la tarea como completa si sale con status 0. Un `DoneCriteria` fallido cuenta como fallo de la tarea (dispara los reintentos de `budget.max_retries_per_task` igual que un error del agente) — así una tarea nunca se marca "lista" solo porque el modelo lo dijo.
- **`file_budget`**: declarativo — documenta qué archivos toca la tarea; hoy nada lo aplica automáticamente.
- **`model_hint`**: `cheap`/`generation`/`reasoning` — se resuelve contra `providers.<name>.model_roles` (§2.2) y **fija el modelo de esa tarea puntual**, distinto al modelo default de la sesión. Sin `model_roles` configurado para el proveedor, o sin `model_hint` en la tarea, corre con el modelo default de siempre — no rompe nada existente.

### 10.1 Descomposición automática (`--decompose`)

Si el manifiesto no trae `tasks` (o viene vacío), `--decompose` le pide al modelo default del daemon que proponga la lista de tareas a partir de `goal` (+ `spec` si está presente), usando una sesión efímera separada de la sesión real de ejecución. La propuesta:
- Se valida con las mismas reglas que un manifiesto escrito a mano (IDs únicos, `goal` no vacío).
- Se persiste en `--state-dir/.forge/runs/<run_id>/tasks.decomposed.json` como rastro de auditoría.
- Queda sujeta al checkpoint HITL `after_spec_decomposition` si el manifiesto lo declara — recién ahí ese gate tiene contenido real que aprobar, en vez de dispararse contra el fallback de un solo task.
- En modo `dry_run`, decompone y muestra el plan (cuántas tareas, con qué estructura) sin ejecutar nada — útil para previsualizar antes de correr en serio.

El turno de descomposición corre **sin acceso a tools** (a propósito: en una prueba real contra este mismo repo, el modelo agotó las iteraciones explorando el filesystem en vez de responder, pese a que el prompt le pedía no hacerlo — forzar `tools: []` en ese turno lo hace imposible en vez de solo pedirlo).

`--decompose` requiere `--manifest` y un manifiesto sin `tasks` ya escritas; es incompatible con `--resume` (un run reanudado reusa la lista de tareas original, decompuesta o no) y con `--verify-audit`.

`--resume` reanuda desde `state.json` en `--state-dir`, salta tareas ya completadas y reutiliza la sesión original — requiere el mismo archivo de manifiesto (mismo `run_id`).

---

## 11. Modelos, cambio de proveedor y routing por costo (v1)

`session.switch_model` (o `/model <nombre>` en el REPL) cambia el modelo **default del daemon**, no solo el de la sesión actual — es una limitación conocida, no un bug: forge no trackea "proveedor por sesión", solo un string `model` (y ahora `provider`) en la metadata.

### 11.1 Cambiar de proveedor

`/model` acepta dos formas:
- **Nombre pelado** (`/model kimi-k3`): busca primero en el proveedor default actual (comportamiento de siempre); si no está ahí, busca en el catálogo cacheado de **todos** los demás proveedores (`ListAll`) — si aparece en exactamente uno, cambia provider+modelo juntos automáticamente; si aparece en más de uno, error explícito nombrando cada proveedor (usá la forma explícita para desambiguar); si no aparece en ninguno, el error de siempre.
- **`provider/model` explícito** (`/model go/kimi-k3`, misma sintaxis que ya usa `forge fanout --models`): cambia ambos de una, sin ambigüedad posible, ganando siempre sobre la búsqueda automática.

Para ver **todos los modelos que un proveedor realmente tiene** (no solo los declarados en `providers.<name>.models`) y elegir uno:
- **REPL**: `/provider <name>` refresca en vivo el catálogo real del proveedor (pega contra su propio endpoint `/models`) y lista los modelos numerados; contestá con un número o el nombre para cambiar. `/provider` sin argumento lista los proveedores configurados.
- **CLI no interactiva**: `forge daemon set-provider <name>` sin `--model` imprime el catálogo real y no cambia nada (para descubrir); con `--model` cambia el default del daemon directo, sin necesidad de sesión — afecta a toda sesión nueva a partir de ahí.

```bash
forge daemon set-provider go                          # lista el catálogo real (puede tener más modelos que los declarados en config)
forge daemon set-provider go --model kimi-k3           # cambia el default, aunque kimi-k3 no esté en providers.go.models
```

### 11.2 Routing por rol (`model_roles`)

Con `--routing` (flag v1), el paso de generación principal de un turno normal puede resolver su modelo vía `providers.<name>.model_roles.generation` en vez del default fijo. Por separado, un `Task.model_hint` en un manifiesto (§10) resuelve `providers.<name>.model_roles.<hint>` y fija el modelo de esa tarea puntual — ambos caminos comparten la misma config `model_roles`. La infraestructura de routing (`internal/routing`) define además steps `classify`/`retrieve`/`summarize`/`validate`/`reason` para automatizar la elección de rol — hoy esos steps no hacen ninguna llamada a modelo (retrieval/compactación son determinísticos), así que no hay nada que enrutar ahí todavía; el rol de cada tarea sigue siendo una decisión explícita (a mano o del descomponedor de `--decompose`), no automática.

### 11.3 Failover automático de modelo (`fallback_chain`)

Por defecto, si el `default_provider`/modelo falla (rate limit, el proveedor está caído, timeout de red), el turno entero falla — el error llega tal cual al usuario. `fallback_chain` (opt-in, ausente por defecto) le da a forge una lista de respaldo a la que recurrir en ese caso, sin intervención manual:

```json
{
  "default_provider": "go",
  "fallback_chain": ["go/minimax-m3", "ollama/qwen2.5-coder:7b"]
}
```

**Qué cuenta como "falla transitoria" (reintentable):** 429 (rate limit), 502/503/504 (outage del lado del proveedor), timeout de red o conexión rechazada. Todo lo demás — 400/401/403/404/500, un request malformado, una API key inválida — se considera permanente: cambiar de modelo no lo arregla, así que el turno falla inmediato sin gastar intentos en el resto de la cadena. Esta clasificación es automática (`internal/llm/retryable.go`), no configurable.

**Orden de intentos:** primero el default actual, después cada entrada de `fallback_chain` en el orden declarado. Se prueba una por una; en cuanto una responde, ese es el resultado del turno. Cubre tanto turnos normales como streaming (`llm.streaming: true`) — un fallo *antes* de que llegue el primer token se trata igual que un fallo no-streaming (seguro reintentar); un fallo a mitad de stream (con texto parcial ya mostrado) nunca reintenta, para no mezclar dos respuestas a medias.

**Sticky + cooldown (comportamiento automático, sin flags):**
- Si un modelo de respaldo responde con éxito, se **promueve a default** — los turnos siguientes van directo ahí, sin volver a pagar el timeout/rechazo del que falló primero. Se ve en el log del daemon como `"fallback promoted to sticky default"`.
- Un modelo que acaba de fallar de forma reintentable entra en **cooldown** (no se lo vuelve a ofrecer por un rato) — usa el `Retry-After` que mande el proveedor si vino, si no un default fijo de 30s. Si en algún momento *todos* los candidatos están en cooldown a la vez, forge ignora los cooldowns para ese intento en vez de fallar sin probar nada (un `Retry-After` mal calculado nunca debe dejar el turno sin ninguna opción).

**Qué mirar en el log** (`daemon.log` o stdout de `forge serve`, nivel `WARN`):
```
fell back to next model in fallback_chain             from_provider=go from_model=minimax-m3 to_provider=ollama to_model=qwen2.5-coder:7b
fallback promoted to sticky default                   provider=ollama model=qwen2.5-coder:7b
```

**Cuándo NO aplica:** un modelo pineado explícitamente — `spawn_subagent` con `provider`/`model`, un `Task.model_hint` de manifiesto, un modelo elegido por `--routing` — nunca usa `fallback_chain`: es una elección deliberada de esa llamada puntual, y sustituirla en silencio por otra cosa escondería el error en vez de respetar la intención. Solo el turno "plano" (sin override ni routing activo) usa failover.

---

## 12. Features v1 (opt-in por flag)

| Flag | Qué hace |
|---|---|
| `--retrieval` | Inyecta los 3 chunks de historial más similares semánticamente al mensaje actual (índice en memoria, se reconstruye por sesión de daemon) |
| `--compaction` | Sobre 40 mensajes persistidos, reemplaza el historial completo por un resumen determinístico + los turnos más recientes verbatim. No destructivo: SQLite guarda el transcript completo, solo cambia lo que el modelo *ve* |
| `--anchoring` | Habilita hechos anclados persistentes (`anchoring_store/list/get/delete`, o `forge memory add/list/get/edit/delete` desde afuera) que sobreviven a la compactación |
| `--skills` | Inyección lazy de instrucciones de skills (`.forge/skills/`) cuya descripción matchea semánticamente el mensaje actual |

`forge memory` opera directo sobre el store, sin pasar por el gate de permisos del modelo (son comandos iniciados por el dueño de la sesión, no por el LLM).

---

## 13. Auditoría tamper-evident (RNF-4.10)

Activa **solo** cuando `project.sensitivity` es `regulado` o `datos-sensibles`. Cada entrada del audit log incluye el hash de la anterior (hash chain), append-only. `forge run --verify-audit` recomputa la cadena y reporta si está intacta — cualquier edición retroactiva del archivo la rompe de forma detectable.

---

## 14. La GUI web

Servida en `http://<addr>/`, misma API JSON-RPC que el resto.

- **Topbar**: toggle del rail de sesiones (⌘/Ctrl+B), título de la sesión activa, indicador de conexión, botón de halt global, toggle del panel de recursos (⌘/Ctrl+R).
- **Rail izquierdo**: buscador, botón "nueva sesión", lista de sesiones con prefijo `[DD-MM-YYYY:HH:mm]` y contador de mensajes. Colapsable, estado persistido en `localStorage`.
- **Hilo central**: mensajes del turno con burbuja de usuario, bloque del asistente (con toggle **"razonamiento"** colapsable cuando el modelo devuelve un bloque `<think>...</think>`), tool calls, y meta-línea con tokens + modelo + tiempo transcurrido (`"2h45m35s"`) de cada respuesta. Al enviar, tu mensaje aparece al instante y se muestra un indicador "thinking…" mientras se espera al modelo (el turno es sincrónico del lado del protocolo — ver `sugerenciasDeClaude.md`).
- **Panel de recursos (derecha, colapsable)**: 7 tabs — **Compare** (diff entre sesiones), **Cost** (costo de la sesión actual + resumen por proveedor), **Memory** (anchors, crear/borrar), **Plugins**/**Skills** (listar, habilitar/deshabilitar, recargar), **Jobs** (background jobs, cancelar), **Daemon** (status + halt de emergencia con confirmación).
- **Login**: aparece solo si el daemon tiene password configurada (`forge daemon set-password`); `GET /auth/status` sin caché determina si hace falta.

---

## 15. Costos estimados (RNF-6.3)

```bash
forge cost summary        # agregado por proveedor, todas las sesiones
forge session cost <id>   # tokens + costo de una sesión puntual
```

Requiere `price_per_million_input_tokens`/`price_per_million_output_tokens` configurados en el provider correspondiente; sin eso, se reporta el uso de tokens pero se marca explícitamente "not priced" (no "gratis").

---

## 16. Plugins (WASM) y Skills

```bash
forge plugin list/enable/disable/reload/install/remove/new/validate
forge skill list/enable/disable/reload/install/remove/new/validate/mine
```

Plugins viven en `forge-plugins/`, se cargan como WASM y extienden el registry de tools bajo el mismo piso de permisos (`KindCustom`). Skills viven en `.forge/skills/`, son instrucciones en Markdown (`SKILL.md`) que se inyectan lazy cuando son relevantes al mensaje actual (§12). `skill mine` propone skills nuevos minados de sesiones exitosas (requiere aprobación humana explícita, RF-4.3/4.4).

---

## 17. Jobs en background

```bash
forge jobs list                 # cola de background jobs
forge job follow <job-id>       # re-adjunta a un turno detached
forge job cancel <job-id>       # cancela un job corriendo
```

---

## 18. Versionado del spec

```bash
forge spec log       # historial git del archivo de spec
forge spec show       # contenido actual
forge spec diff       # working tree vs ref, o entre dos refs
forge spec validate   # señales mecánicas de divergencia spec↔código (RF-8.3)
```

Read-only: invoca `git log/show/diff` sobre el archivo de spec versionado, nunca lo modifica.

---

## 19. Firma de aprobaciones

```bash
forge keygen [--force]
```

Genera un par ed25519 en `os.UserConfigDir()/forge/keys/` (privada 0600, pública 0644) para firmar aprobaciones HITL. Se niega a sobrescribir sin `--force`.

---

## 20. Logging

JSON estructurado a stderr (+ archivo opcional vía `logging.file`), niveles `debug|info|warn|error`. Redacción automática de secretos ya mencionada en §6 aplica también a los logs.

---

## 21. Problemas conocidos / troubleshooting

- **El daemon "no envía" o tarda mucho**: si tu `default_provider` apunta a un modelo gratuito de un servicio cloud (OpenRouter free tier, etc.), puede tardar minutos o directamente colgarse sin error — no es un bug de Forge, es latencia/disponibilidad del proveedor. La GUI muestra un indicador "thinking…" para que se note, pero el timeout real de 15 minutos por request sigue vigente por debajo.
- **El puerto de la GUI cambió solo**: si no fijaste `daemon.addr` (§3.2), cada `forge serve` elige un puerto nuevo.
- **Login pedido aunque no configuraste password**: si ves esto en una build vieja, era un bug de CSS (`[hidden]` sin prioridad suficiente) ya corregido — actualizá el binario.
- **Puerto ocupado (`bind: Only one usage of each socket address...`)**: ya hay un daemon corriendo en esa dirección — `forge status` o revisá procesos antes de levantar otro con el mismo `--addr`.
