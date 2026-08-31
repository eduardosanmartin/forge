<#
.SYNOPSIS
    End-to-end smoke of the real forge binary against a live local model.

.DESCRIPTION
    Builds forge.exe, isolates HOME into a temp directory, starts
    `forge serve` on an ephemeral port inside a fresh temp git workspace,
    waits for ~/.forge/daemon.addr, then drives six prompts through
    `forge run --json` in ONE sustained session (write file, read back,
    git status, shell exec, rewrite, commit). Prints a PASS/FAIL table with
    latency and token usage from the JSON outputs, verifies the artifacts on
    disk and in git history, stops the daemon, and exits non-zero on any
    failure.

.PARAMETER Model
    Model name advertised in the generated config (default qwen2.5-coder:7b).

.PARAMETER BaseUrl
    OpenAI-compatible base URL of the local inference server.

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File scripts\run-e2e.ps1
#>
param(
    [string]$Model = "qwen2.5-coder:7b",
    [string]$BaseUrl = "https://opencode.ai/zen/v1"
)

$ErrorActionPreference = "Stop"
$repoRoot = Split-Path -Parent $PSScriptRoot

$failures = 0
$proc = $null
$savedUserProfile = $env:USERPROFILE
$savedHome = $env:HOME
$tmpHome = $null
$tmpWs = $null
$tmpBin = $null

function Cleanup {
    if ($proc -and -not $proc.HasExited) {
        try { Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue } catch { }
    }
    foreach ($pair in @(("USERPROFILE", $savedUserProfile), ("HOME", $savedHome))) {
        if ($null -eq $pair[1]) { Remove-Item "Env:$($pair[0])" -ErrorAction SilentlyContinue }
        else { Set-Item "Env:$($pair[0])" $pair[1] }
    }
}

try {
    # --- Build -------------------------------------------------------------
    Write-Host "== Building forge.exe..."
    $tmpBin = Join-Path ([System.IO.Path]::GetTempPath()) ("forge-e2e-" + [guid]::NewGuid().ToString("N"))
    New-Item -ItemType Directory -Path $tmpBin | Out-Null
    $exe = Join-Path $tmpBin "forge.exe"
    & go build -o $exe (Join-Path $repoRoot "cmd\forge")
    if ($LASTEXITCODE -ne 0) { throw "go build failed" }

    # --- Isolated HOME + workspace ----------------------------------------
    $stamp = [guid]::NewGuid().ToString("N")
    $tmpHome = Join-Path ([System.IO.Path]::GetTempPath()) "forge-e2e-home-$stamp"
    $tmpWs = Join-Path ([System.IO.Path]::GetTempPath()) "forge-e2e-ws-$stamp"
    New-Item -ItemType Directory -Path $tmpHome | Out-Null
    New-Item -ItemType Directory -Path $tmpWs | Out-Null
    $env:USERPROFILE = $tmpHome
    $env:HOME = $tmpHome

    Write-Host "== Preparing temp git workspace..."
    & git -C $tmpWs init | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "git init failed (is git installed?)" }
    & git -C $tmpWs config user.email "e2e@forge.local"
    & git -C $tmpWs config user.name "Forge E2E"

    # --- Config (global only; no project config in the workspace) ----------
    $cfgDir = Join-Path $tmpWs ".forge"
    New-Item -ItemType Directory -Path $cfgDir | Out-Null
    $config = @{
        schema_version   = 4
        default_provider = "zen"
        providers        = @{
            zen = @{
                kind     = "openai-compatible"
                base_url = $BaseUrl
                models   = @($Model)
            }
        }
        storage          = @{ path = (Join-Path $tmpHome "forge.db") }
        network          = @{ allowed_hosts = @("127.0.0.1", "localhost", "opencode.ai") }
        logging          = @{ level = "info" }
        permissions      = @{
            fs    = @{ read = @("./**"); write = @("./**") }
            shell = @{ allow = @("go"); require_isolation = $true }
            git   = @{ allow = @("status", "add", "commit", "log", "diff", "branch",
                                  "switch", "stash", "restore", "show", "remote", "fetch") }
        }
    }
    $config | ConvertTo-Json -Depth 6 | Set-Content -Encoding ASCII (Join-Path $cfgDir "config.json")

    # --- Start daemon -------------------------------------------------------
    Write-Host "== Starting forge serve (ephemeral port)..."
    $outLog = Join-Path $tmpBin "serve.out.log"
    $errLog = Join-Path $tmpBin "serve.err.log"
    $proc = Start-Process -FilePath $exe -ArgumentList @("serve") `
        -WorkingDirectory $tmpWs -PassThru -WindowStyle Hidden `
        -RedirectStandardOutput $outLog -RedirectStandardError $errLog

    # Daemon address discovery: forge always writes daemon.addr under the
    # process home directory (os.UserHomeDir()/.forge), which this script
    # isolates to $tmpHome — regardless of where the config file lives.
    $addrFile = Join-Path (Join-Path $tmpHome ".forge") "daemon.addr"
    $deadline = (Get-Date).AddSeconds(30)
    while (-not (Test-Path $addrFile)) {
        if ($proc.HasExited) { throw "forge serve exited early; see $errLog" }
        if ((Get-Date) -gt $deadline) { throw "timeout waiting for $addrFile" }
        Start-Sleep -Milliseconds 200
    }
    $addr = (Get-Content $addrFile -Raw).Trim()
    Write-Host "   daemon at $addr"

    # --- Prompts (single sustained session) ---------------------------------
    $prompts = @(
        @{ id = 1; desc = "fs_write creates file";
           prompt = 'Use the fs_write tool to create a file named cli-notes.md whose entire content is exactly this single line: CLI_E2E_MARKER=zulu-7 . Then reply DONE.' },
        @{ id = 2; desc = "fs_read reads it back";
           prompt = 'Use the fs_read tool to read cli-notes.md, then reply with the exact value of CLI_E2E_MARKER.' },
        @{ id = 3; desc = "git status";
           prompt = 'Use the git tool with subcommand status to show the repository status, then summarize it in one sentence.' },
        @{ id = 4; desc = "shell_exec go version";
           prompt = 'Use the shell_exec tool to run the command go with argument version, then reply with the exact output.' },
        @{ id = 5; desc = "fs_write rewrites file";
           prompt = 'Use the fs_write tool to rewrite cli-notes.md so its entire content becomes exactly two lines: CLI_E2E_MARKER=zulu-7 and updated-by-cli-run . Then reply DONE.' },
        @{ id = 6; desc = "git add + commit";
           prompt = 'Commit the change using the git tool twice: first subcommand add with argument cli-notes.md, then subcommand commit with commit message cli-e2e-commit . Reply with the confirmation.' }
    )

    $sessionId = $null
    $rows = @()
    foreach ($p in $prompts) {
        $runArgs = @("run", "--json")
        if ($sessionId) { $runArgs += @("--session", $sessionId) }
        $runArgs += $p.prompt

        $sw = [System.Diagnostics.Stopwatch]::StartNew()
        $runErrLog = Join-Path $tmpBin "run-$($p.id).err.log"
        $stdout = & $exe @runArgs 2>$runErrLog
        $code = $LASTEXITCODE
        $sw.Stop()

        $ok = $false; $respLen = 0; $tokens = 0; $tools = "-"
        $json = $null
        try {
            $json = ($stdout -join "`n") | ConvertFrom-Json
            if (-not $sessionId -and $json.session_id) { $sessionId = $json.session_id }
            $respLen = ([string]$json.response).Length
            if ($json.usage) { $tokens = [int]$json.usage.total_tokens }
            if ($json.tool_calls) { $tools = (@($json.tool_calls) | ForEach-Object { $_.name }) -join "," }
            $ok = ($code -eq 0) -and ($respLen -gt 0)
        } catch {
            $ok = $false
        }
        if (-not $ok) { $script:failures++ }

        $rows += [pscustomobject]@{
            Turn   = $p.id
            Desc   = $p.desc
            Result = $(if ($ok) { "PASS" } else { "FAIL" })
            Ms     = $sw.ElapsedMilliseconds
            Tokens = $tokens
            Tools  = $tools
            RespLn = $respLen
        }
        if ($rows[-1].Result -eq "FAIL") {
            Write-Host "   turn $($p.id) raw stdout:" -ForegroundColor Yellow
            Write-Host (($stdout -join "`n").Substring(0, [Math]::Min(400, ($stdout -join "`n").Length)))
        }
    }

    # --- Artifact verification ----------------------------------------------
    $notesPath = Join-Path $tmpWs "cli-notes.md"
    $artifactOk = $true
    if (-not (Test-Path $notesPath)) {
        Write-Host "ARTIFACT FAIL: cli-notes.md missing" -ForegroundColor Red
        $artifactOk = $false
    } else {
        $content = Get-Content $notesPath -Raw
        if ($content -notmatch "zulu-7" -or $content -notmatch "updated-by-cli-run") {
            Write-Host "ARTIFACT FAIL: unexpected cli-notes.md content:" -ForegroundColor Red
            Write-Host $content
            $artifactOk = $false
        }
    }
    $subject = $null
    try { $subject = (& git -C $tmpWs log "-1" "--format=%s" 2>$null) } catch { $subject = $null }
    if ($LASTEXITCODE -ne 0 -or "$subject".Trim() -ne "cli-e2e-commit") {
        Write-Host "ARTIFACT FAIL: expected HEAD subject 'cli-e2e-commit', got '$subject'" -ForegroundColor Red
        $artifactOk = $false
    }
    if (-not $artifactOk) { $script:failures++ }

    # --- Report ---------------------------------------------------------------
    Write-Host ""
    Write-Host "== E2E results (model: $Model, session: $sessionId)"
    $rows | Format-Table Turn, Desc, Result, Ms, Tokens, RespLn, Tools -AutoSize | Out-String | Write-Host
    Write-Host ("Artifact checks: " + $(if ($artifactOk) { "PASS" } else { "FAIL" }))
    if ($sessionId) {
        Write-Host "Session persisted across all six turns (sustained conversation)."
    }
}
catch {
    Write-Host "FAIL: $_" -ForegroundColor Red
    $script:failures++
}
finally {
    Cleanup
    foreach ($d in @($tmpHome, $tmpWs, $tmpBin)) {
        if ($d -and (Test-Path $d)) {
            Remove-Item -Recurse -Force $d -ErrorAction SilentlyContinue
        }
    }
}

if ($failures -gt 0) {
    Write-Host "RESULT: FAIL ($failures failure(s))" -ForegroundColor Red
    exit 1
}
Write-Host "RESULT: PASS" -ForegroundColor Green
exit 0
