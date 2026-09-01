# Build the urlcheck dogfood plugin WASM and copy to testdata.
# Regeneration entry point for WU6: cargo build + copy.

$ErrorActionPreference = "Stop"
$here = Split-Path -Parent $MyInvocation.MyCommand.Path
$root = Resolve-Path (Join-Path $here "..")
$plugDir = Join-Path $root "internal\pluginwasm\testdata\urlcheck"
$manifest = Join-Path $plugDir "Cargo.toml"
$wasmOut = Join-Path $plugDir "target\wasm32-unknown-unknown\release\urlcheck.wasm"
$dest = Join-Path $plugDir "urlcheck.wasm"

# Discover cargo: FORGE_CARGO, PATH, fallback
$cargo = $env:FORGE_CARGO
if ([string]::IsNullOrWhiteSpace($cargo)) {
    try { $cargo = (Get-Command cargo -ErrorAction Stop).Source } catch { $cargo = "" }
}
if ([string]::IsNullOrWhiteSpace($cargo) -or -not (Test-Path $cargo)) {
    $fallback = "C:\Users\eduar\.cargo\bin\cargo.exe"
    if (Test-Path $fallback) { $cargo = $fallback }
}
if ([string]::IsNullOrWhiteSpace($cargo) -or -not (Test-Path $cargo)) {
    throw "cargo not found (checked FORGE_CARGO, PATH, and C:\Users\eduar\.cargo\bin\cargo.exe). Install Rust 1.98+."
}

Write-Host "Using cargo: $cargo"
& $cargo --version
if ($LASTEXITCODE -ne 0) { throw "cargo --version failed" }

if (-not (Test-Path $manifest)) { throw "missing $manifest" }

Write-Host "Building urlcheck plugin..."
& $cargo build --target wasm32-unknown-unknown --release --manifest-path $manifest
if ($LASTEXITCODE -ne 0) { throw "cargo build failed" }

if (-not (Test-Path $wasmOut)) { throw "expected wasm not found at $wasmOut" }
Copy-Item -Force $wasmOut $dest
$info = Get-Item $dest
Write-Host "Done: $dest ($($info.Length) bytes)"
if ($info.Length -lt 500) { throw "wasm too small, expected >=500 bytes" }
