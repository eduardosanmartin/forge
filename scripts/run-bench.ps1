# run-bench.ps1 - RNF-10 / RNF-2.3 context-token reduction benchmark.
#
# Thin wrapper around the Go benchmark: the measurement itself is
# deterministic and model-free and lives in internal/bench (real
# ContextAssembler + real SQLite store + real v1 packages over a scripted
# conversation). This script just runs the TestBenchTokenReduction test and
# surfaces the result.
#
# Usage:
#   scripts\run-bench.ps1              # 40 turns, human-readable summary
#   scripts\run-bench.ps1 -Turns 80    # longer session
#   scripts\run-bench.ps1 -Json        # single JSON object with the numbers
param(
    [int]$Turns = 40,
    [switch]$Json
)

$repoRoot = Split-Path -Parent $PSScriptRoot
Push-Location $repoRoot
try {
    $env:FORGE_BENCH_TURNS = [string]$Turns

    # 'Continue' around the native call: go test writes compile errors to
    # stderr, and PS 5.1 turns redirected stderr into error records that
    # would otherwise abort under 'Stop'.
    $ErrorActionPreference = 'Continue'
    $output = & go test ./internal/bench -run TestBenchTokenReduction -v -count=1 2>&1
    $exitCode = $LASTEXITCODE
    $ErrorActionPreference = 'Stop'

    if ($exitCode -ne 0) {
        $output | ForEach-Object { $_.ToString() }
        exit $exitCode
    }

    if ($Json) {
        $matchInfo = $output | Select-String -Pattern 'BENCH_JSON (\{.*\})' | Select-Object -First 1
        if ($null -eq $matchInfo) {
            Write-Error "bench JSON line not found in test output"
            exit 1
        }
        Write-Output $matchInfo.Matches[0].Groups[1].Value
    }
    else {
        $output | ForEach-Object { $_.ToString() }
    }
}
finally {
    Remove-Item Env:FORGE_BENCH_TURNS -ErrorAction SilentlyContinue
    Pop-Location
}
