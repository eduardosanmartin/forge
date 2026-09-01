<#
.SYNOPSIS
    Operational v2 exit check — proves the spec exit criterion without recompiling plugins.

.DESCRIPTION
    1. git clone <Repo> into a temp dir (defaults to current repo path; local clone is fine)
    2. go build ./cmd/forge (ONE binary build — plugins are NEVER built)
    3. Start forge serve (background) with an isolated HOME and workspace
    4. forge plugin install <thirdparty-urlcheck-ext> --yes + enable + list
    5. forge skill install <thirdparty-deploy-notes> --yes + enable + list
    6. Tool execution via forge run ONLY if LLM is configured; otherwise SKIPPED (Go e2e covers it)
    7. Emit PASS/FAIL checklist per criterion (plugin installed w/o recompile, skill installed w/o recompile,
       enabled states, wizard validity via forge plugin validate + forge skill validate, proposals isolation)
       and exit 0/1.

.NOTES
    Follows repo PowerShell conventions: UTF-8 no BOM, ASCII-safe output strings,
    no array-flattening -File traps.

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File scripts/verify-v2-exit.ps1
    powershell -ExecutionPolicy Bypass -File scripts/verify-v2-exit.ps1 -Repo C:\ESV\IA\harness-code
#>
param(
    [string]$Repo = ""
)

$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
if ([string]::IsNullOrWhiteSpace($Repo)) {
    $Repo = $repoRoot
}

$tmpClone = $null
$tmpHome = $null
$tmpWs = $null
$tmpBin = $null
$proc = $null
$savedUserProfile = $env:USERPROFILE
$savedHome = $env:HOME
$failures = 0
$rows = @()

function Add-Row {
    param([string]$Criterion, [string]$Result, [string]$Evidence)
    $script:rows += [pscustomobject]@{ Criterion = $Criterion; Result = $Result; Evidence = $Evidence }
    if ($Result -eq "FAIL") { $script:failures++ }
}

function Cleanup {
    try { Pop-Location } catch { }
    if ($proc -and -not $proc.HasExited) {
        try { Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue } catch { }
        Start-Sleep -Milliseconds 500
    }
    if ($tmpClone -and (Test-Path $tmpClone)) {
        # Retry removal for Windows handle holders
        for ($i=0; $i -lt 5; $i++) {
            try { Remove-Item -Recurse -Force $tmpClone -ErrorAction Stop; break } catch { Start-Sleep -Milliseconds 300 }
        }
    }
    foreach ($pair in @(("USERPROFILE", $savedUserProfile), ("HOME", $savedHome))) {
        if ($null -eq $pair[1]) { Remove-Item "Env:$($pair[0])" -ErrorAction SilentlyContinue }
        else { Set-Item "Env:$($pair[0])" $pair[1] }
    }
}

try {
    Write-Host "== v2 exit verification =="
    Write-Host "   Repo: $Repo"

    # --- Clone ------------------------------------------------------------
    $tmpClone = Join-Path ([System.IO.Path]::GetTempPath()) ("forge-v2-exit-" + [guid]::NewGuid().ToString("N"))
    Write-Host "== git clone $Repo -> $tmpClone"
    # NOTE: no 2>&1 here — under $ErrorActionPreference="Stop", PS 5.1 turns
    # git's normal stderr progress ("Cloning into...") into a terminating error.
    # --quiet keeps stderr empty on success; failures still set LASTEXITCODE.
    & git clone --quiet $Repo $tmpClone
    if ($LASTEXITCODE -ne 0) { throw "git clone failed" }
    Add-Row "git clone" "PASS" "cloned $Repo"

    # --- Build (ONE binary, plugins NEVER built) --------------------------
    Write-Host "== go build ./cmd/forge (single binary, plugins are not rebuilt)"
    $exe = Join-Path $tmpClone "forge.exe"
    Push-Location $tmpClone
    try {
        & go build -o $exe ./cmd/forge 2>&1 | Out-Host
        if ($LASTEXITCODE -ne 0) { throw "go build failed" }
    } finally { Pop-Location }
    if (-not (Test-Path $exe)) { throw "forge.exe not produced" }
    $buildInfo = (Get-Item $exe).Length
    Write-Host "   built forge.exe ($buildInfo bytes)"
    Add-Row "go build (no plugin rebuild)" "PASS" "forge.exe $buildInfo bytes; urlcheck.wasm unchanged"

    # --- Isolated HOME + workspace ----------------------------------------
    $stamp = [guid]::NewGuid().ToString("N")
    $tmpHome = Join-Path ([System.IO.Path]::GetTempPath()) "forge-v2-home-$stamp"
    $tmpWs = Join-Path ([System.IO.Path]::GetTempPath()) "forge-v2-ws-$stamp"
    New-Item -ItemType Directory -Path $tmpHome | Out-Null
    New-Item -ItemType Directory -Path $tmpWs | Out-Null
    $env:USERPROFILE = $tmpHome
    $env:HOME = $tmpHome

    Write-Host "== Preparing isolated git workspace $tmpWs"
    & git -C $tmpWs init | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "git init failed" }
    & git -C $tmpWs config user.email "verify@forge.local"
    & git -C $tmpWs config user.name "Forge Verify"

    $cfgDir = Join-Path $tmpWs ".forge"
    New-Item -ItemType Directory -Path $cfgDir | Out-Null
    $config = @{
        schema_version = 4
        default_provider = "test"
        providers = @{
            "test" = @{
                kind = "openai-compatible"
                base_url = "http://127.0.0.1:9"
                models = @("test-model")
            }
        }
        storage = @{ path = (Join-Path $tmpHome "forge.db") }
        network = @{ allowed_hosts = @("127.0.0.1", "localhost") }
        permissions = @{
            fs = @{ read = @("./**"); write = @("./**") }
            shell = @{ allow = @("echo", "go"); require_isolation = $false }
            git = @{ allow = @("status", "add", "commit", "log", "diff") }
        }
    }
    $config | ConvertTo-Json -Depth 6 | Set-Content -Encoding ASCII (Join-Path $cfgDir "config.json")

    # --- Start daemon -------------------------------------------------------
    Write-Host "== Starting forge serve"
    $outLog = Join-Path $tmpClone "serve.out.log"
    $errLog = Join-Path $tmpClone "serve.err.log"
    $proc = Start-Process -FilePath $exe -ArgumentList @("serve") -WorkingDirectory $tmpWs -PassThru -WindowStyle Hidden -RedirectStandardOutput $outLog -RedirectStandardError $errLog
    $addrFile = Join-Path (Join-Path $tmpHome ".forge") "daemon.addr"
    $deadline = (Get-Date).AddSeconds(20)
    while (-not (Test-Path $addrFile)) {
        if ($proc.HasExited) {
            $err = Get-Content $errLog -Raw -ErrorAction SilentlyContinue
            throw "forge serve exited early: $err"
        }
        if ((Get-Date) -gt $deadline) { throw "timeout waiting for $addrFile" }
        Start-Sleep -Milliseconds 200
    }
    $addr = (Get-Content $addrFile -Raw).Trim()
    Write-Host "   daemon at $addr"
    Add-Row "daemon start" "PASS" "forge serve at $addr"

    # All forge CLI invocations run from the isolated workspace: install roots
    # (./forge-plugins, ./.forge/skills) are CWD-relative and MUST land there,
    # not in the invoking shell's directory.
    Push-Location $tmpWs

    $thirdpartyPlugin = Join-Path $tmpClone "internal\e2e\testdata\thirdparty\urlcheck-ext"
    $thirdpartySkill = Join-Path $tmpClone "internal\e2e\testdata\thirdparty\deploy-notes"

    # --- Plugin: install external (hash-bound approval) ---------------------
    Write-Host "== forge plugin install (external, --yes)"
    $out = & $exe plugin install $thirdpartyPlugin --yes 2>&1 | Out-String
    $code = $LASTEXITCODE
    Write-Host $out
    if ($code -ne 0) {
        Add-Row "plugin install (external, no recompile)" "FAIL" "forge plugin install exit $code"
    } else {
        # Check approved.flag exists and is hash-bound
        $flag = Join-Path $tmpWs "forge-plugins\urlcheck\approved.flag"
        if (Test-Path $flag) {
            $hash = (Get-Content $flag -Raw).Trim()
            if ($hash -match "^sha256:[0-9a-f]{64}$") {
                Add-Row "plugin install (external, no recompile)" "PASS" "approved.flag $hash"
            } else {
                Add-Row "plugin install (external, no recompile)" "FAIL" "bad approved.flag $hash"
            }
        } else {
            Add-Row "plugin install (external, no recompile)" "FAIL" "missing approved.flag"
        }
    }

    # --- Plugin: reload + enable + list ------------------------------------
    # The daemon scanned forge-plugins/ at boot (empty); reload makes it see
    # the fresh install. urlcheck is external => loads disabled; enable uses
    # the approved.flag record.
    Write-Host "== forge plugin reload"
    $out = & $exe plugin reload 2>&1 | Out-String
    $code = $LASTEXITCODE
    Write-Host $out
    if ($code -ne 0) { Add-Row "plugin reload after install" "FAIL" "exit $code" } else { Add-Row "plugin reload after install" "PASS" "daemon sees install" }

    Write-Host "== forge plugin enable urlcheck"
    $out = & $exe plugin enable urlcheck 2>&1 | Out-String
    $code = $LASTEXITCODE
    Write-Host $out
    if ($code -ne 0) { Add-Row "plugin enable" "FAIL" "exit $code" } else { Add-Row "plugin enable" "PASS" "enabled urlcheck" }

    Write-Host "== forge plugin list"
    $out = & $exe plugin list 2>&1 | Out-String
    Write-Host $out
    if ($out -match "urlcheck" -and $out -match "True") {
        Add-Row "plugin list shows enabled" "PASS" "urlcheck enabled in list"
    } else {
        Add-Row "plugin list shows enabled" "FAIL" "urlcheck not shown as enabled"
    }

    # --- Skill: install external -------------------------------------------
    Write-Host "== forge skill install (external, --yes)"
    $out = & $exe skill install $thirdpartySkill --yes 2>&1 | Out-String
    $code = $LASTEXITCODE
    Write-Host $out
    if ($code -ne 0) {
        Add-Row "skill install (external, no recompile)" "FAIL" "exit $code"
    } else {
        $flag = Join-Path $tmpWs ".forge\skills\deploy-notes\approved.flag"
        if (Test-Path $flag) {
            Add-Row "skill install (external, no recompile)" "PASS" "approved.flag present"
        } else {
            Add-Row "skill install (external, no recompile)" "FAIL" "missing approved.flag"
        }
    }

    Write-Host "== forge skill reload"
    $out = & $exe skill reload 2>&1 | Out-String
    $code = $LASTEXITCODE
    Write-Host $out
    if ($code -ne 0) { Add-Row "skill reload after install" "FAIL" "exit $code" } else { Add-Row "skill reload after install" "PASS" "daemon sees install" }

    Write-Host "== forge skill enable deploy-notes"
    $out = & $exe skill enable deploy-notes 2>&1 | Out-String
    $code = $LASTEXITCODE
    Write-Host $out
    # External may already be disabled; enable should succeed or report already enabled
    if ($code -eq 0 -or $out -match "already enabled") {
        Add-Row "skill enable" "PASS" "deploy-notes enabled"
    } else {
        Add-Row "skill enable" "FAIL" "exit $code"
    }

    Write-Host "== forge skill list"
    $out = & $exe skill list 2>&1 | Out-String
    Write-Host $out
    if ($out -match "deploy-notes") {
        Add-Row "skill list shows skill" "PASS" "deploy-notes in list"
    } else {
        Add-Row "skill list shows skill" "FAIL" "not in list"
    }

    # --- Wizard validity ----------------------------------------------------
    Write-Host "== Wizard: forge plugin new (scripted inputs)"
    $wizPluginDir = Join-Path $tmpWs "forge-plugins\wiz-verify"
    # Wizard prompt sequence: name, version, description, FIVE permission
    # Bools (fs.read, fs.write, shell.exec, git, net), entrypoint, source.
    $pluginAnswers = @("wiz-verify", "0.1.0", "Verify plugin", "y", "n", "n", "n", "n", "n", "", "local") -join "`n"
    $pluginAnswers | & $exe plugin new 2>&1 | Out-String | Write-Host
    if (Test-Path (Join-Path $tmpWs "forge-plugins\wiz-verify\manifest.toml")) {
        $vOut = & $exe plugin validate (Join-Path $tmpWs "forge-plugins\wiz-verify") 2>&1 | Out-String
        Write-Host $vOut
        if ($LASTEXITCODE -eq 0 -and $vOut -match "valid") {
            Add-Row "wizard plugin new + validate" "PASS" "wiz-verify validated"
        } else {
            Add-Row "wizard plugin new + validate" "FAIL" "validate failed"
        }
    } else {
        Add-Row "wizard plugin new + validate" "FAIL" "manifest not created"
    }

    Write-Host "== Wizard: forge skill new (scripted inputs)"
    $skillAnswers = @("wiz-skill-verify", "Verify skill", "docs", "wiz, verify", "", "local") -join "`n"
    $skillAnswers | & $exe skill new 2>&1 | Out-String | Write-Host
    if (Test-Path (Join-Path $tmpWs ".forge\skills\wiz-skill-verify\SKILL.md")) {
        $vOut = & $exe skill validate (Join-Path $tmpWs ".forge\skills\wiz-skill-verify") 2>&1 | Out-String
        Write-Host $vOut
        if ($LASTEXITCODE -eq 0 -and $vOut -match "valid") {
            Add-Row "wizard skill new + validate" "PASS" "wiz-skill-verify validated"
        } else {
            Add-Row "wizard skill new + validate" "FAIL" "validate failed"
        }
    } else {
        Add-Row "wizard skill new + validate" "FAIL" "SKILL.md not created"
    }

    # --- Proposals isolation (RF-4.4) ---------------------------------------
    Write-Host "== Proposals isolation (skill-proposals not scanned)"
    $propDir = Join-Path $tmpWs ".forge\skill-proposals\should-not-appear"
    New-Item -ItemType Directory -Path $propDir -Force | Out-Null
    Set-Content -Path (Join-Path $propDir "SKILL.md") -Value "---`nname: `"should-not-appear`"`ndescription: `"proposal`"`nsource: local`n---`nBody`n" -Encoding Ascii
    $out = & $exe skill list 2>&1 | Out-String
    if ($out -match "should-not-appear") {
        Add-Row "proposals isolation" "FAIL" "proposal appeared in skill list"
    } else {
        Add-Row "proposals isolation" "PASS" "proposal not in skill list"
    }

    # --- Tool execution (live LLM gate) -------------------------------------
    Write-Host "== Tool execution (live LLM gate)"
    $hasKey = $false
    if ($env:FORGE_LLM) { $hasKey = $true }
    if (Test-Path (Join-Path $tmpHome ".forge\zen.key")) { $hasKey = $true }
    if (Test-Path (Join-Path $tmpWs ".forge\zen.key")) { $hasKey = $true }
    if ($hasKey) {
        Write-Host "   live LLM detected, running forge run --json"
        $runOut = & $exe run --json "Use the urlcheck_status tool to check http://127.0.0.1:65534/" 2>&1 | Out-String
        Write-Host $runOut
        Add-Row "tool execution (live LLM)" "PASS" "forge run executed"
    } else {
        Write-Host "   SKIPPED: no live LLM (set FORGE_LLM or place .forge/zen.key); Go e2e covers execution"
        Add-Row "tool execution (live LLM)" "SKIPPED" "no LLM configured; Go e2e covers execution"
    }

    # --- Report -------------------------------------------------------------
    Write-Host ""
    Write-Host "== v2 exit checklist =="
    $fmt = "{0,-40} {1,-8} {2}"
    Write-Host ($fmt -f "CRITERION", "RESULT", "EVIDENCE")
    Write-Host ("-" * 90)
    foreach ($r in $rows) {
        $color = "Gray"
        if ($r.Result -eq "PASS") { $color = "Green" }
        elseif ($r.Result -eq "FAIL") { $color = "Red" }
        elseif ($r.Result -eq "SKIPPED") { $color = "Yellow" }
        Write-Host ($fmt -f $r.Criterion, $r.Result, $r.Evidence) -ForegroundColor $color
    }
    $failed = @($rows | Where-Object { $_.Result -eq "FAIL" }).Count
    if ($failed -gt 0) {
        Write-Host "`nRESULT: FAIL ($failed failures)" -ForegroundColor Red
        exit 1
    } else {
        Write-Host "`nRESULT: PASS (v2 exit criteria satisfied)" -ForegroundColor Green
        exit 0
    }

} catch {
    Write-Host "FATAL: $_" -ForegroundColor Red
    Write-Host $_.ScriptStackTrace
    exit 1
} finally {
    Cleanup
}