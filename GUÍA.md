# forge — Guía Rápida (MVP v0)

`forge` es un harness de desarrollo agéntico local-first: corre modelos locales (Ollama), herramientas reales sobre tu workspace y un daemon persistente con parada de emergencia. Esta guía cubre el MVP v0 según la spec `spec-harness-agentic.md` v0.8.

---

## 1. Requisitos

- **Go 1.26+** (compilación)
- **Ollama** corriendo localmente (`ollama serve`) con al menos un modelo descargado:
  ```bash
  ollama pull qwen2.5-coder:7b   # modelo de referencia del MVP
  ```
- **Git** en PATH (para la herramienta `git`)

---

## 2. Instalación

```bash
git clone https://github.com/eduardosanmartin/forge
cd forge
go build -o forge.exe ./cmd/forge
```

El binario `forge.exe` queda en la raíz del repo.

---

## 3. Configuración

`forge` usa configuración en capas (precedencia: **proyecto > global > defaults**).

| Archivo | Ubicación | Qué define |
|---------|-----------|------------|
| Global  | `~/.forge/config.json` | Modelo, allowlist red, logging, storage |
| Proyecto | `.forge/config.json` (en la raíz de tu repo) | Overrides por proyecto, versionable en git |

### Ejemplo mínimo (`.forge/config.json`)

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
  "network": { "allowed_hosts": ["127.0.0.1", "localhost"] },
  "logging": { "level": "info", "file": "" },
  "permissions": {
    "fs": { "read": ["./**"], "write": ["./**"] },
    "shell": { "allow": ["go", "git"], "require_isolation": true },
    "git": { "allow": ["status","add","commit","log","diff","branch","switch","stash","restore","show","remote","fetch"] }
  }
}
```

> **Nota de seguridad (RNF-4.1/4.7/4.9):** por defecto **todo está denegado**. La sección `permissions` abre **solo** lo que vos declares. En Linux (Perfil B) el flag `require_isolation: true` exige Landlock+seccomp; si el kernel no lo soporta, la shell se niega a ejecutar (modo estricto). En macOS/Windows la flag se ignora y rige solo el modelo de permisos (ver spec §6, aclaración 2).

---

## 4. Uso Básico

### 4.1 Levantar el daemon (una vez por sesión)

```bash
forge serve
```

El daemon escribe su dirección en `~/.forge/daemon.addr` y queda escuchando WebSocket JSON-RPC 2.0. Mantené esta terminal abierta (o corré con `nohup`/`systemd` si querés que sobreviva al cierre).

### 4.2 Chat interactivo (REPL)

```bash
forge chat
```

Dentro del REPL:

| Comando | Acción |
|---------|--------|
| `/model <nombre>` | Cambia de modelo en caliente (p.ej. `/model llama3.1:8b-instruct`) |
| `/sessions` | Lista sesiones existentes |
| `/attach <id>` | Se conecta a una sesión existente |
| `/new` | Crea sesión nueva y cambia a ella |
| `/halt [id]` | Parada de emergencia (global si no das id) |
| `/resume <id>` | Reanuda una sesión detenida |
| `/exit` o Ctrl-D | Sale del REPL (el daemon sigue vivo) |

Todo lo que escribas que **no** empiece con `/` se envía como mensaje al agente. El agente puede usar herramientas reales (`fs.write`, `fs.read`, `shell.exec`, `git`) sujetas a los permisos de tu config.

### 4.3 Modo no interactivo (scriptable / CI)

```bash
# Respuesta humana (stdout = respuesta final; stderr = trazas de herramientas)
forge run "Creá un archivo hello.go que imprima Hola"

# Salida JSON para integración (stdout = solo JSON; stderr = vacío en éxito)
forge run --json "Creá un test para hello.go"
```

La salida JSON (`--json`) sigue el schema `OneShotResult`:

```json
{
  "session_id": "uuid",
  "model": "qwen2.5-coder:7b",
  "response": "Archivo creado...",
  "tool_calls": [{"name":"fs.write","args":{"path":"..."},"ok":true}],
  "usage": {"prompt_tokens":120,"completion_tokens":45,"total_tokens":165},
  "duration_ms": 45000
}
```

Códigos de salida: `0` éxito, `1` fallo de ejecución, `2` error de uso/flags.

### 4.4 Gestión de sesiones

```bash
forge sessions           # lista todas (id, creado, mensajes, modelo)
forge attach <id>        # conecta REPL a sesión existente
forge halt               # parada de emergencia global (RNF-4.8)
forge halt <session-id>  # para una sesión específica
forge resume <id>        # reanuda sesión detenida
forge status             # health-check del daemon
```

---

## 5. Herramientas Disponibles (MCP-shape)

| Tool | Descripción | Permiso requerido |
|------|-------------|-------------------|
| `fs.read` | Lee archivo (offset/limit opcional, UTF-8; binario → base64) | `fs.read` |
| `fs.write` | Escribe archivo (create_dirs, encoding utf8/base64, atómico) | `fs.write` |
| `fs.list` | Lista directorio (recursive, pattern glob) | `fs.read` |
| `shell.exec` | Ejecuta comando (timeout 120s máx 300s, 50KB truncado) | `shell.allow` (basename) |
| `git` | Subcomandos git (add, commit, status, log, diff, branch, switch, stash, restore, show, remote, fetch) | `git.allow` (subcomando) |

> **Fencing RNF-4.5**: cada resultado de herramienta vuelve envuelto en bloques determinísticos:
> ```
> <<TOOL_RESULT:fs.write>>
> <CONTENT>
> ...contenido real (redactado si hay secrets)...
> </CONTENT>
> </TOOL_RESULT:fs.write>
> ```
> El modelo **no puede** confundir salida de herramienta con instrucciones.

### Ejemplo de `shell.exec` con `workdir`

```json
{
  "command": "go",
  "args": ["test", "./..."],
  "workdir": "./subproyecto"
}
```
> **Importante**: si omitís `workdir`, se usa la raíz del workspace. **Nunca inventes rutas** como `/workspace` o `/tmp`; el modelo recibirá un error accionable para reintentar sin el campo.

---

## 6. Parada de Emergencia (RNF-4.8)

Desde **cualquier cliente** (REPL, `forge run`, otro `forge chat` adjunto a la misma sesión):

```bash
forge halt           # detiene TODO inmediatamente
forge halt <sesion>  # detiene solo esa sesión
```

El estado `halted` persiste en la DB (`forge.db`). Para reanudar:

```bash
forge resume <id>
```

---

## 7. Cambio de Modelo en Caliente (RF-2.3)

Sin reiniciar el daemon ni la sesión:

```bash
# Desde el REPL
/model qwen2.5-coder:7b
/model llama3.1:8b-instruct
```

O vía RPC (programático): método `switch_model` con `{ "model": "nombre" }`. El cambio aplica a la sesión actual y persiste en metadata.

---

## 8. Estructura de Datos / Persistencia

- **SQLite** (`~/.forge/forge.db` por defecto, configurable en `storage.path`)
- Tablas: `sessions`, `messages` (append-only con `seq`), `config_snapshots`
- **WAL mode**, FK, migraciones embebidas (`go:embed`) versión 1→2→3
- Sobrevive a reinicio del daemon (RF-3.1) y a cierre de cliente (RF-1.4 parcial)

---

## 9. Logging y Redacción (RNF-6.1, RNF-4.4)

- JSON estructurado a stderr (+ archivo opcional via `logging.file`)
- Redacción automática de: claves AWS (`AKIA...`), cabeceras PEM, tokens `sk-...`/`ghp_...`, asignaciones `api_key=...`/`password=...`
- Niveles: `debug`, `info`, `warn`, `error` (configurable en `logging.level`)

---

## 10. Scripts de Verificación E2E

Para validar el criterio de salida contra tu Ollama local:

```powershell
# Windows
.\scripts\run-e2e.ps1

# Linux/macOS
./scripts/run-e2e.sh
```

El script levanta un daemon efímero, ejecuta prompts de prueba contra `qwen2.5-coder:7b` (u otro modelo vía `-Model`), imprime tabla PASS/FAIL con latencia y tokens, y apaga el daemon. Requiere `FORGE_E2E_LIVE=1` en el entorno o pasa `--live`.

---

## 11. Roadmap (resumen de la spec §6)

| MVP | Tema | Estado v0 |
|-----|------|-----------|
| v0 | Núcleo interactivo mínimo | **Hecho** |
| v1 | Eficiencia de contexto/tokens (retrieval, compactación) | Próximo |
| v2 | Plugins WASM + skills + adaptadores remotos | Pendiente |
| v3 | Subagentes / branching / multi-run | Pendiente |
| v4 | GUI Web / SDD formal / auto-aprendizaje | Pendiente |
| v1.0 | Ejecución autónoma end-to-end (RF-11) | Último |

---

## 13. Modelos Recomendados (Alternativas a qwen2.5-coder:7b)

`qwen2.5-coder:7b` (4.7 GB) es el modelo de referencia del MVP v0, pero hay alternativas **más pequeñas, rápidas y eficientes** que funcionan mejor en hardware limitado o para tareas específicas.

### Tabla comparativa

| Modelo | Tamaño (Q4) | Parámetros | Contexto | Especialidad | RAM Mínima | Velocidad CPU | Tool-Calling / FIM | Mejor Para |
|--------|-------------|------------|----------|--------------|------------|---------------|-------------------|------------|
| **qwen2.5-coder:0.5b** | ~1 GB | 0.5B | 32K (8K FIM) | **FIM-optimizado**, chat, code | 2-4 GB | **200-500 tok/s** (GPU), ~50-200 CPU | ✅ FIM nativo, chat | **Completions ultra-rápidas**, laptops, CI, edge |
| **qwen2.5-coder:1.5b** | ~1.5 GB | 1.5B | 32K | Chat, code, reasoning | 4-8 GB | ~100-200 tok/s CPU | ✅ Tool-calling bueno | **Balance velocidad/calidad** (recomendado principal) |
| **qwen2.5-coder:3b** | ~2-4 GB | 3B | 32K | Chat, code, reasoning | 6-8 GB | ~50-100 tok/s CPU | ✅ Tool-calling nativo | **Sweet spot** calidad/velocidad |
| **neolinschen/llama-coding:1b** | ~1.3 GB | 1B | **128K** | Coding-optimizado (Llama 3.2) | 2-4 GB | **Muy rápido** | ⚠️ Limitado | Contexto largo, velocidad extrema |
| **neolinschen/llama-coding:3b** | ~2 GB | 3B | **128K** | Coding-optimizado (Llama 3.2) | 4-6 GB | Rápido | ⚠️ Limitado | Contexto largo + calidad |
| **relational/VULCAN (4B)** | ~2.5 GB | 4B | 16K | **Fine-tuned Qwen3 para coding** | 4-6 GB | ~50-80 tok/s | ✅ Tool-calling fuerte | **Mejor calidad coding < 5B** |
| **qwen2.5-coder:7b** (actual) | **~4.7 GB** | **7B** | **128K** | **Referencia actual** | **8-12 GB** | **~12s/turno** | ✅ **Completo** | **Baseline actual** |

### Recomendaciones por Caso de Uso

| Objetivo | Modelo Recomendado | Comando |
|----------|-------------------|---------|
| **Desarrollo diario / velocidad** | `qwen2.5-coder:1.5b` | `ollama pull qwen2.5-coder:1.5b` |
| **Calidad máxima < 5B (tool-calling fuerte)** | `relational/VULCAN` | `ollama pull relational/VULCAN` |
| **Contexto largo (128K) + embeddings** | `neolinschen/llama-coding:3b` | `ollama pull neolinschen/llama-coding:3b` |
| **Testing CI / ultra-rápido** | `qwen2.5-coder:0.5b` | `ollama pull qwen2.5-coder:0.5b` |

### Configuración recomendada para v1 (multi-modelo)

```json
{
  "schema_version": 3,
  "default_provider": "ollama",
  "providers": {
    "ollama": {
      "kind": "openai-compatible",
      "base_url": "http://127.0.0.1:11434/v1",
      "models": [
        "qwen2.5-coder:1.5b",
        "neolinschen/llama-coding:3b",
        "relational/VULCAN",
        "qwen2.5-coder:0.5b",
        "qwen2.5-coder:7b"
      ]
    }
  }
}
```

> **Nota**: v1 usará **ruteo por costo** (RF-2.4/2.5): modelo pequeño (1.5b/0.5b) para pasos baratos (clasificación, retrieval queries, resúmenes) y modelo mayor (3b/7b/VULCAN) para generación real. El ruteo es configurable por tipo de paso del ciclo (§3.2 spec).

---

## 13. Licencia y Contribución