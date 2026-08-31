<#>
.SYNOPSIS
    Bootstrap script para iniciar v1 development con forge v0.
    Ejecutar desde una NUEVA sesión de terminal (fresh session).

.DESCRIPTION
    Este script prepara el entorno y lanza `forge chat` con el prompt inicial
    para comenzar v1 development usando forge v0 como herramienta.

    Requisitos previos:
    - forge compilado y en PATH (o .\forge.exe desde repo root)
    - Ollama corriendo con qwen2.5-coder:7b (u otro modelo configurado)
    - Git limpio en commit v0.0-mvp-complete (o tag)

.NOTES
    Ejecutar desde la raíz del repo forge:
    .\scripts\v1-bootstrap.ps1

    O desde cualquier ubicación si forge está en PATH:
    v1-bootstrap.ps1
#>

param(
    [string]$Model = "qwen2.5-coder:3b",
    [string]$BaseUrl = "http://127.0.0.1:11434/v1",
    [switch]$NoVerify
)

# Colores para output
$Green  = [ConsoleColor]::Green
$Yellow = [ConsoleColor]::Yellow
$Red    = [ConsoleColor]::Red
$Cyan   = [ConsoleColor]::Cyan
$Gray   = [ConsoleColor]::Gray

function Write-Header($msg) { Write-Host "`n=== $msg ===" -ForegroundColor $Cyan }
function Write-Ok($msg)     { Write-Host "  ✅ $msg" -ForegroundColor $Green }
function Write-Warn($msg)   { Write-Host "  ⚠️  $msg" -ForegroundColor $Yellow }
function Write-Err($msg)    { Write-Host "  ❌ $msg" -ForegroundColor $Red }
function Write-Info($msg)   { Write-Host "  ℹ️  $msg" -ForegroundColor $Gray }

# --- 1. Verificaciones previas ---
Write-Header "🔍 Verificaciones pre-v1"

# 1.1 ¿Estamos en repo forge?
$repoRoot = if (Test-Path "spec-harness-agentic.md") { Get-Location } 
            elseif (Test-Path "..\spec-harness-agentic.md") { Set-Location ".."; Get-Location }
            else { Write-Err "No se encuentra spec-harness-agentic.md. Ejecutá desde la raíz del repo forge."; exit 1 }
Write-Ok "Repo forge detectado en: $repoRoot"

# 1.2 Verificar commit/tag base
$head = git rev-parse --short HEAD 2>$null
$expected = "ac552c1"  # HEAD actual con rollback docs
$tag = "v0.0-mvp-complete"
if ($head -eq $expected) { Write-Ok "HEAD en commit esperado: $head (v0.0-mvp-complete)" }
elseif (git tag -l "v0.0-mvp-complete" | Select-String "v0.0-mvp-complete") { Write-Ok "Tag v0.0-mvp-complete existe" }
else { Write-Warn "HEAD=$head (esperado $expected o tag $tag). Continuando igual..." }

# 1.3 Git limpio
$status = git status --porcelain
if ($status) { Write-Warn "Git NO limpio:`n$status`nContinuando igual..." } else { Write-Ok "Git limpio" }

# 1.4 Ollama vivo
Write-Info "Probando Ollama en $BaseUrl..."
try {
    $ollama = Invoke-RestMethod -Uri "$($BaseUrl -replace '/v1/?$', '')/api/version" -TimeoutSec 5 -ErrorAction Stop
    Write-Ok "Ollama responde: $($ollama.version)"
} catch {
    Write-Err "Ollama no responde en $BaseUrl. Inicialo con: `ollama serve"
    if (-not $NoVerify) { exit 1 }
}

# 1.5 Modelo disponible
try {
    $models = Invoke-RestMethod -Uri "$($BaseUrl -replace '/v1/?$', '')/api/tags" -TimeoutSec 5
    if ($models.models.name -contains $Model) {
        Write-Ok "Modelo '$Model' disponible"
    } else {
        Write-Warn "Modelo '$Model' NO está descargado. Disponibles: $($models.models.name -join ', ')"
        Write-Info "Descargalo con: ollama pull $Model"
        if (-not $NoVerify) { exit 1 }
    }
} catch {
    Write-Err "No se pudo listar modelos"
    if (-not $NoVerify) { exit 1 }
}

# 1.6 Forge binario
$forgeExe = if (Test-Path ".\forge.exe") { ".\forge.exe" } elseif (Get-Command forge -ErrorAction SilentlyContinue) { "forge" } else { $null }
if ($forgeExe) { Write-Ok "Forge binario: $forgeExe" } else { Write-Err "Forge no encontrado. Compilá con: `go build -o forge.exe ./cmd/forge"; exit 1 }

# --- 2. Prompt inicial v1 ---
$v1Prompt = @"
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
"@

# --- 3. Lanzar forge chat ---
Write-Header "🚀 Lanzando forge chat con prompt v1 inicial"
Write-Info "Modelo: $Model | BaseUrl: $BaseUrl"
Write-Info "Prompt longitud: $($v1Prompt.Length) chars"
Write-Info "Presioná Ctrl+C en cualquier momento para salir del chat.`n"

# Guardar prompt en archivo temporal para pasarlo a forge (evita escaping issues)
$promptFile = [IO.Path]::GetTempFileName()
$v1Prompt | Out-File -FilePath $promptFile -Encoding UTF8

# Lanzar forge chat con el prompt como primer mensaje
# Opción A: forge run --json "..." (no interactivo)
# Opción B: forge chat (interactivo) — preferible para v1
# Usamos: echo $prompt | forge chat  → pero forge chat lee de stdin línea a línea
# Mejor: pasamos el prompt como primer mensaje via --print? No, forge chat es interactivo.
# Estrategia: escribimos el prompt en un archivo y usamos `forge run` para la primera vuelta,
# luego seguimos con `forge chat` si querés continuar interactivo.
# MÁS SIMPLE: usamos `forge run` con el prompt completo, vemos la respuesta, y si querés seguir,
# corrés `forge chat` adjuntando la sesión creada.

Write-Info "Ejecutando primera vuelta con 'forge run' (no interactivo) para ver respuesta inicial..."
Write-Info "Si querés seguir interactivo, corré después: `forge chat` o forge attach <session-id>"

$cmd = "$forgeExe run --json `"$v1Prompt`""
Write-Info "Comando: $cmd"

$exitCode = 0
try {
    & $forgeExe run --json $v1Prompt
    $exitCode = $LASTEXITCODE
} catch {
    Write-Err "Error ejecutando forge: $_"
    $exitCode = 1
}

if ($exitCode -eq 0) {
    Write-Ok "`nforge run completado (exit=$exitCode)."
    Write-Info "Para seguir en modo interactivo, corré:"
    Write-Info "  forge chat"
    Write-Info "  o bien: forge attach <session-id>  (la sesión creada se muestra en la salida de arriba)"
} else {
    Write-Err "forge run falló (exit=$exitCode). Revisá logs arriba."
}

# Limpieza
Remove-Item $promptFile -Force -ErrorAction SilentlyContinue

Write-Header "✅ Bootstrap v1 completado"
Write-Info "Próximos pasos sugeridos:"
Write-Info "  1. Leé la respuesta de forge arriba — debería haber propuesto plan de tareas."
Write-Info "  2. Si todo OK, corré: `forge chat` para continuar interactivo."
Write-Info "  3. Si algo falla: `git checkout v0.0-mvp-complete` (o `b8ef0de`) y re-planificamos."

exit $exitCode
