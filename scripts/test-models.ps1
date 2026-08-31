<#
.SYNOPSIS
    Test forge tool-calling with specified models (local Ollama or remote OpenAI-compatible).
.DESCRIPTION
    For each model: starts a forge daemon pinned to that model + a temp workspace,
    runs fs_write, fs_read, shell_exec, git.status, git.commit, fs_list via
    'forge run --json' (with a per-run timeout so a hung model fails gracefully),
    then stops the daemon. Prints a report.

    Remote providers: pass -ProviderName/-ProviderBaseUrl and set -APIKeyEnv to the
    name of an env var holding the API key (forge reads it; config api_key stays empty).
#>
param(
    [string[]]$Models = @("qwen3:1.7b", "qwen3:4b"),
    [string]$ProviderName = "ollama",
    [string]$ProviderKind = "openai-compatible",
    [string]$ProviderBaseUrl = "http://127.0.0.1:11434/v1",
    [string]$APIKeyEnv = "",
    [string[]]$AllowedHosts = @("127.0.0.1", "localhost")
)

$ErrorActionPreference = "Stop"
$ForgeExe = (Resolve-Path ".\forge.exe").Path

function Write-Header { param([string]$m) Write-Host "`n=== $m ===" -ForegroundColor Cyan }
function Write-Ok     { param([string]$m) Write-Host "  [OK]   $m" -ForegroundColor Green }
function Write-Err    { param([string]$m) Write-Host "  [FAIL] $m" -ForegroundColor Red }
function Write-Warn   { param([string]$m) Write-Host "  [WARN] $m" -ForegroundColor Yellow }
function Write-Info   { param([string]$m) Write-Host "  [INFO] $m" -ForegroundColor Gray }

function New-DaemonConfig {
    param([string]$Model, [string]$StorageJson, [string]$OutPath)
    $apiKeyValue = ""
    if ($APIKeyEnv -ne "") {
        $apiKeyValue = [Environment]::GetEnvironmentVariable($APIKeyEnv)
        if ($null -eq $apiKeyValue) { $apiKeyValue = "" }
    }
    $hostsJson = ($AllowedHosts | ForEach-Object { '"{0}"' -f $_ }) -join ","
    $cfg = @"
{
  "schema_version": 3,
  "default_provider": "$ProviderName",
  "providers": {
    "$ProviderName": {
      "kind": "$ProviderKind",
      "base_url": "$ProviderBaseUrl",
      "models": [ "$Model" ],
      "api_key": "$apiKeyValue"
    }
  },
  "storage": { "path": "$StorageJson" },
  "network": { "allowed_hosts": [ $hostsJson ] },
  "logging": { "level": "warn", "file": "" },
  "permissions": {
    "fs": { "read": [ "./**" ], "write": [ "./**" ] },
    "shell": { "allow": [ "go", "git" ], "require_isolation": false },
    "git": { "allow": [ "status","add","commit","log","diff","branch","switch","stash","restore","show","remote","fetch" ] }
  }
}
"@
    $cfg | Set-Content -Path $OutPath -Encoding ASCII
}

function Start-Daemon {
    param([string]$Model, [string]$Workspace)
    $storage = Join-Path $Workspace "daemon.db"
    $storageJson = $storage.Replace('\', '\\')
    $dcfg = Join-Path $Workspace "daemon-config.json"
    New-DaemonConfig -Model $Model -StorageJson $storageJson -OutPath $dcfg

    Get-Process -Name "forge" -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
    $addrFile = Join-Path $env:USERPROFILE ".forge\daemon.addr"
    if (Test-Path $addrFile) { Remove-Item $addrFile -Force }

    Start-Process -FilePath $ForgeExe -ArgumentList "serve","--config",$dcfg `
        -WorkingDirectory $Workspace -WindowStyle Hidden `
        -RedirectStandardOutput (Join-Path $Workspace "daemon.out.log") `
        -RedirectStandardError (Join-Path $Workspace "daemon.err.log")

    $t = 0
    while ((-not (Test-Path $addrFile)) -and $t -lt 30) { Start-Sleep -Seconds 1; $t++ }
    if (-not (Test-Path $addrFile)) {
        Write-Err "daemon failed to start for $Model"
        throw "daemon start failed: $Model"
    }
    Start-Sleep -Seconds 2
}

function Stop-Daemon {
    Get-Process -Name "forge" -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
    $addrFile = Join-Path $env:USERPROFILE ".forge\daemon.addr"
    if (Test-Path $addrFile) { Remove-Item $addrFile -Force }
}

function Invoke-ForgeRun {
    param([string]$Prompt, [int]$TimeoutSec = 100)
    $job = Start-Job -ScriptBlock {
        param($exe, $p)
        & $exe run --json $p 2>$null
    } -ArgumentList $ForgeExe, $Prompt
    $done = Wait-Job $job -Timeout $TimeoutSec
    if (-not $done) {
        Stop-Job $job -ErrorAction SilentlyContinue
        Remove-Job $job -ErrorAction SilentlyContinue
        return [pscustomobject]@{ Exit = -1; Json = $null; TimedOut = $true }
    }
    $stdout = Receive-Job $job -ErrorAction SilentlyContinue
    Remove-Job $job -ErrorAction SilentlyContinue
    $obj = $null
    try { $obj = $stdout | ConvertFrom-Json -ErrorAction Stop } catch { $obj = $null }
    return [pscustomobject]@{ Exit = 0; Json = $obj; TimedOut = $false }
}

function HasToolCall {
    param($Json, [string]$ToolName)
    if ($null -eq $Json) { return $false }
    if ($null -eq $Json.tool_calls) { return $false }
    foreach ($tc in $Json.tool_calls) {
        if ($tc.name -eq $ToolName -and $tc.ok -eq $true) { return $true }
    }
    return $false
}

function Test-Model {
    param([string]$Model)
    Write-Header "Testing model: $Model"
    $ws = Join-Path $env:TEMP ("forge-ws-" + ($Model -replace "[:/]", "-") + "-" + [guid]::NewGuid().ToString("N").Substring(0,8))
    New-Item -ItemType Directory -Force -Path $ws | Out-Null
    git init -q $ws
    git -C $ws config user.email "test@forge.local"
    git -C $ws config user.name "Forge Test"

    Start-Daemon -Model $Model -Workspace $ws
    Set-Location $ws

    $r = @{ Model = $Model; Tests = @(); Passed = 0; Failed = 0; Start = Get-Date; ModelUsed = ""; Hung = $false }

    # T1 fs_write
    Write-Info "T1 fs_write"
    $p = "Use the fs_write tool to create a file named test.txt containing exactly: hello from $Model"
    $res = Invoke-ForgeRun -Prompt $p
    if ($res.TimedOut) { $r.Hung = $true }
    $ok = (-not $res.TimedOut) -and (Test-Path "test.txt") -and (HasToolCall $res.Json "fs_write")
    $r.Tests += [pscustomobject]@{ Name="fs_write"; Passed=$ok; Detail=if($ok){"file created"}elseif($res.TimedOut){"TIMEOUT"}else{"file=$(Test-Path 'test.txt')"} }
    if($ok){$r.Passed++;Write-Ok "fs_write"}else{$r.Failed++;Write-Err "fs_write"}

    # T2 fs_read
    Write-Info "T2 fs_read"
    $p = "Use the fs_read tool to read test.txt and tell me its contents"
    $res = Invoke-ForgeRun -Prompt $p
    if ($res.TimedOut) { $r.Hung = $true }
    $ok = (-not $res.TimedOut) -and (HasToolCall $res.Json "fs_read")
    $r.Tests += [pscustomobject]@{ Name="fs_read"; Passed=$ok; Detail=if($ok){"read ok"}elseif($res.TimedOut){"TIMEOUT"}else{"no tool_call"} }
    if($ok){$r.Passed++;Write-Ok "fs_read"}else{$r.Failed++;Write-Err "fs_read"}

    # T3 shell_exec
    Write-Info "T3 shell_exec"
    $p = "Use the shell_exec tool to run the command: go version"
    $res = Invoke-ForgeRun -Prompt $p
    if ($res.TimedOut) { $r.Hung = $true }
    $ok = (-not $res.TimedOut) -and (HasToolCall $res.Json "shell_exec")
    $r.Tests += [pscustomobject]@{ Name="shell_exec"; Passed=$ok; Detail=if($ok){"shell ok"}elseif($res.TimedOut){"TIMEOUT"}else{"no tool_call"} }
    if($ok){$r.Passed++;Write-Ok "shell_exec"}else{$r.Failed++;Write-Err "shell_exec"}

    # T4 git status
    Write-Info "T4 git.status"
    $p = "Use the git tool to run: status"
    $res = Invoke-ForgeRun -Prompt $p
    if ($res.TimedOut) { $r.Hung = $true }
    $ok = (-not $res.TimedOut) -and (HasToolCall $res.Json "git")
    $r.Tests += [pscustomobject]@{ Name="git.status"; Passed=$ok; Detail=if($ok){"git ok"}elseif($res.TimedOut){"TIMEOUT"}else{"no tool_call"} }
    if($ok){$r.Passed++;Write-Ok "git.status"}else{$r.Failed++;Write-Err "git.status"}

    # T5 git add + commit (no embedded quotes to avoid arg splitting)
    Write-Info "T5 git.commit"
    $p = "Use the git tool to run: add . and then commit with message test-commit-from-$Model"
    $res = Invoke-ForgeRun -Prompt $p
    if ($res.TimedOut) { $r.Hung = $true }
    $ok = (-not $res.TimedOut) -and (HasToolCall $res.Json "git")
    $r.Tests += [pscustomobject]@{ Name="git.commit"; Passed=$ok; Detail=if($ok){"commit ok"}elseif($res.TimedOut){"TIMEOUT"}else{"no tool_call"} }
    if($ok){$r.Passed++;Write-Ok "git.commit"}else{$r.Failed++;Write-Err "git.commit"}

    # T6 fs_list
    Write-Info "T6 fs_list"
    $p = "Use the fs_list tool to list files in the current directory"
    $res = Invoke-ForgeRun -Prompt $p
    if ($res.TimedOut) { $r.Hung = $true }
    $ok = (-not $res.TimedOut) -and (HasToolCall $res.Json "fs_list")
    $r.Tests += [pscustomobject]@{ Name="fs_list"; Passed=$ok; Detail=if($ok){"list ok"}elseif($res.TimedOut){"TIMEOUT"}else{"no tool_call"} }
    if($ok){$r.Passed++;Write-Ok "fs_list"}else{$r.Failed++;Write-Err "fs_list"}

    if ($null -ne $res.Json) { $r.ModelUsed = $res.Json.model }
    if ($r.ModelUsed -and $r.ModelUsed -ne $Model) { Write-Warn "daemon used $($r.ModelUsed) not $Model" }

    $r.End = Get-Date
    $r.Duration = [math]::Round(($r.End - $r.Start).TotalSeconds, 1)
    Set-Location $env:TEMP
    Stop-Daemon
    Remove-Item -Recurse -Force $ws -ErrorAction SilentlyContinue
    return $r
}

# Main
Write-Header "forge tool-calling test suite"
Write-Info "Provider: $ProviderName ($ProviderBaseUrl)"
Write-Info "Models: $($Models -join ', ')"
Write-Info "Forge: $ForgeExe"

if (-not (Test-Path $ForgeExe)) { Write-Err "forge exe not found: $ForgeExe"; exit 1 }

$all = @()
foreach ($m in $Models) { $all += Test-Model $m }

# Report
Write-Header "FINAL REPORT"
$globalPass = 0; $globalFail = 0; $globalTotal = 0
foreach ($r in $all) {
    Write-Header "Model: $($r.Model) (daemon used: $($r.ModelUsed))"
    if ($r.Hung) { Write-Warn "Model hung/timeout on tool calls (see environment note)" }
    Write-Info "Duration: $($r.Duration)s | Passed: $($r.Passed) | Failed: $($r.Failed)"
    foreach ($t in $r.Tests) {
        $s = if($t.Passed){"[PASS]"}else{"[FAIL]"}
        Write-Host ("  {0,-12} {1} - {2}" -f $t.Name, $s, $t.Detail)
    }
    $globalPass += $r.Passed; $globalFail += $r.Failed; $globalTotal += $r.Tests.Count
}
Write-Header "SUMMARY"
Write-Host "Models tested: $($Models.Count)"
Write-Host "Total tests: $globalTotal"
Write-Ok "Passed: $globalPass"
if ($globalFail -gt 0) { Write-Err "Failed: $globalFail" } else { Write-Ok "Failed: 0" }
$rate = if($globalTotal -gt 0){[math]::Round(($globalPass/$globalTotal)*100,1)}else{0}
Write-Info "Success rate: $rate%"
if ($globalFail -gt 0) { exit 1 } else { exit 0 }
