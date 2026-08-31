#!/usr/bin/env bash
# v1-bootstrap.sh — Bootstrap v1 development with forge v0
# Run from a FRESH terminal session (fresh session recommended).
# Usage: ./scripts/v1-bootstrap.sh [--model MODEL] [--base-url URL] [--no-verify]

set -euo pipefail

# Colors
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
CYAN='\033[0;36m'
GRAY='\033[0;90m'
NC='\033[0m' # No Color

MODEL="qwen2.5-coder:7b"
BASE_URL="http://127.0.0.1:11434/v1"
NO_VERIFY=false

# Parse args
while [[ $# -gt 0 ]]; do
    case $1 in
        --model) MODEL="$2"; shift 2 ;;
        --base-url) BASE_URL="$2"; shift 2 ;;
        --no-verify) NO_VERIFY=true; shift ;;
        *) echo "Unknown option: $1"; exit 1 ;;
    esac
done

GREEN='\033[0;32m'; YELLOW='\033[1;33m'; RED='\033[0;31m'; CYAN='\033[0;36m'; GRAY='\033[0;90m'; NC='\033[0m'

header() { echo -e "\n${CYAN}=== $1 ===${NC}"; }
ok() { echo -e "  ${GREEN}✅ $1${NC}"; }
warn() { echo -e "  ${YELLOW}⚠️  $1${NC}"; }
err() { echo -e "  ${RED}❌ $1${NC}"; }
info() { echo -e "  ${GRAY}ℹ️  $1${NC}"; }

# --- 1. Pre-flight checks ---
header "🔍 Verificaciones pre-v1"

# 1.1 Repo root
if [[ -f "spec-harness-agentic.md" ]]; then
    REPO_ROOT=$(pwd)
elif [[ -f "../spec-harness-agentic.md" ]]; then
    cd .. && REPO_ROOT=$(pwd)
else
    err "spec-harness-agentic.md no encontrado. Ejecutá desde la raíz del repo forge."
    exit 1
fi
ok "Repo forge detectado en: $REPO_ROOT"

# 1.2 Commit/tag base
HEAD=$(git rev-parse --short HEAD 2>/dev/null)
EXPECTED="ac552c1"
TAG="v0.0-mvp-complete"
if [[ "$HEAD" == "$EXPECTED" ]]; then
    ok "HEAD en commit esperado: $HEAD (v0.0-mvp-complete)"
elif git tag -l "v0.0-mvp-complete" | grep -q "v0.0-mvp-complete"; then
    ok "Tag v0.0-mvp-complete existe"
else
    warn "HEAD=$HEAD (esperado $EXPECTED o tag $TAG). Continuando..."
fi

# 1.3 Git clean
if [[ -n "$(git status --porcelain)" ]]; then
    warn "Git NO limpio: $(git status --porcelain | wc -l) cambios. Continuando..."
else
    ok "Git limpio"
fi

# 1.4 Ollama alive
info "Probando Ollama en $BASE_URL..."
if curl -sf "$BASE_URL/api/version" >/dev/null; then
    VER=$(curl -sf "$BASE_URL/api/version" | jq -r '.version // "unknown"')
    ok "Ollama responde: $VER"
else
    err "Ollama no responde en $BASE_URL. Inicialo con: ollama serve"
    [[ "$NO_VERIFY" == "true" ]] || exit 1
fi

# 1.5 Model available
MODELS_JSON=$(curl -sf "$BASE_URL/api/tags" 2>/dev/null || echo '{"models":[]}')
if echo "$MODELS_JSON" | jq -e ".models[] | select(.name == \"$MODEL\")" >/dev/null; then
    ok "Modelo '$MODEL' disponible"
else
    AVAIL=$(echo "$MODELS_JSON" | jq -r '.models[].name' | paste -sd, -)
    warn "Modelo '$MODEL' NO descargado. Disponibles: $AVAIL"
    info "Descargalo con: ollama pull $MODEL"
    [[ "$NO_VERIFY" == "true" ]] || exit 1
fi

# 1.6 Forge binary
FORGE_EXE=""
if [[ -f "./forge" ]]; then
    FORGE_EXE="./forge"
elif command -v forge >/dev/null 2>&1; then
    FORGE_EXE="forge"
else
    err "Forge no encontrado. Compilá: go build -o forge ./cmd/forge"
    exit 1
fi
ok "Forge binario: $FORGE_EXE"

# --- 2. Prompt v1 ---
read -r -d '' V1_PROMPT <<'EOF'
Vamos a implementar la **primera historia de v1**: **retrieval + compactación jerárquica** (RNF-2.2/2.3/2.4, RF-3.2-3.5).

Contexto:
- Estamos en forge v0 (MVP completo, commit `b8ef0de` / tag `v0.0-mvp-complete`).
- La spec es `spec-harness-agentic.md` v0.8, §6 MVP v1.
- v1 se construye **100% con forge v0 + modelo local** (bootstrapping). Sin OpenCode, sin OpenChamber.
- Stack actual: config v3, perms engine, MCP tools (fs/shell/git), Ollama adapter, SQLite store, daemon JSON-RPC, agent loop, REPL, Landlock+seccomp isolation.

Tarea:
1. **Leé la spec v0.8 §6 MVP v1** (retrieval, compactación jerárquica, anclaje, ruteo por costo).
2. **Descomponé en tareas atómicas** con criterio de "hecho" verificable (tests, lint, build).
3. **Presentá el plan** (tareas, orden, dependencias, riesgos) antes de codear.
4. Empezá por la **primera tarea** (sugerencia: infraestructura de embeddings + vector store SQLite-vec / LanceDB).

Reglas de v1:
- **Bootstrapping estricto**: todo código v1 se escribe usando `forge chat` / `forge run` contra el modelo local (`qwen2.5-coder:7b` o el configurado).
- **Tests first**: cada tarea debe tener test verificable antes de codear (TDD estricto si el proyecto lo soporta).
- **Commits atómicos**: cada tarea = 1 commit convencional (`feat:`, `fix:`, etc.).
- **Checkpoint humano**: antes de cada commit, confirmá conmigo (simulá pregunta o pedí confirmación).
- **Docs**: actualizá README/DESARROLLO.md/CONFIGURACIÓN.md si cambias APIs/config.

¿Empezamos? Confirmame y arrancamos con la **Tarea 1: infraestructura de embeddings + vector store**.
EOF

# --- 3. Launch ---
header "🚀 Lanzando forge chat con prompt v1 inicial"
info "Modelo: $MODEL | BaseUrl: $BASE_URL"
info "Prompt: ${#V1_PROMPT} chars"
info "Presioná Ctrl+C para salir del chat."

# Write prompt to temp file for clean passing
PROMPT_FILE=$(mktemp)
printf '%s' "$V1_PROMPT" > "$PROMPT_FILE"
trap 'rm -f "$PROMPT_FILE"' EXIT

info "Ejecutando primera vuelta con 'forge run' (no interactivo)..."
info "Si querés seguir interactivo, corré después: forge chat  o  forge attach <session-id>"

FORGE_EXE="./forge"
if [[ ! -x "./forge" ]]; then
    FORGE_EXE="forge"  # fallback to PATH
fi

# Run with JSON output for clean parsing
"$FORGE_EXE" run --json "$V1_PROMPT"
EXIT_CODE=$?

if [[ $EXIT_CODE -eq 0 ]]; then
    echo -e "\n${GREEN}✅ forge run completado (exit=0).${NC}"
    info "Para seguir en modo interactivo, corré:"
    info "  forge chat"
    info "  o: forge attach <session-id>  (la sesión creada se muestra arriba)"
else
    echo -e "\n${RED}❌ forge run falló (exit=$?). Revisá logs arriba.${NC}"
fi

header "✅ Bootstrap v1 completado"
info "Próximos pasos:"
info "  1. Leé la respuesta de forge arriba — debería proponer plan de tareas."
info "  2. Si todo OK, corré: forge chat  para continuar interactivo."
info "  3. Si algo falla: git checkout v0.0-mvp-complete  (o b8ef0de) y re-planificamos."

exit $EXIT_CODE