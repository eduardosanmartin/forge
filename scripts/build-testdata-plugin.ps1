# Build the greeter test plugin WASM for internal/pluginwasm/testdata/greeter.
# The reference source is Go (GOOS=wasip1 GOARCH=wasm + //go:wasmexport) documented in
# internal/pluginwasm/testdata/greeter/main.go:
#   GOOS=wasip1 GOARCH=wasm go build -o greeter.wasm .
# That artifact is 3+ MB and requires a full WASI + proc_exit stub to keep the
# module alive after _start. For deterministic, fast tests the checked-in testdata
# is a minimal equivalent built from greeter.wat via wabt (wat2wasm) — same ABI,
# same host imports, same JSON-over-linear-memory convention, but without the Go
# runtime. Both artifacts satisfy the forge ABI; the WAT version is used so unit
# tests do not need a 3 MB binary or a heavy WASI filesystem setup.
#
# Regeneration (requires Node + wabt npm):
#   npm install --prefix $env:TEMP wabt
#   node -e "const wabt=require('C:\Users\eduar\AppData\Local\Temp\opencode\node_modules\wabt'); wabt().then(...)"
#
# Alternative (large) Go build:
#   $env:GOOS="wasip1"; $env:GOARCH="wasm"; go build -o greeter.wasm .; Remove-Item Env:GOOS; Remove-Item Env:GOARCH

$ErrorActionPreference = "Stop"
$here = Split-Path -Parent $MyInvocation.MyCommand.Path
$root = Resolve-Path (Join-Path $here "..")
$greeterDir = Join-Path $root "internal\pluginwasm\testdata\greeter"
$watPath = Join-Path $greeterDir "greeter.wat"
$wasmPath = Join-Path $greeterDir "greeter.wasm"

if (-not (Test-Path $watPath)) { throw "missing $watPath" }

# Prefer wabt via Node if available
$wabtPath = "C:\Users\eduar\AppData\Local\Temp\opencode\node_modules\wabt"
$useNode = $false
try {
    $null = Get-Command node -ErrorAction Stop
    if (Test-Path $wabtPath) { $useNode = $true }
} catch {}

if ($useNode) {
    Write-Host "Compiling greeter.wat -> greeter.wasm via wabt (Node)..."
    $watEsc = $watPath -replace '\\','\\'
    $wasmEsc = $wasmPath -replace '\\','\\'
    node -e @"
const wabt=require('C:\\Users\\eduar\\AppData\\Local\\Temp\\opencode\\node_modules\\wabt');
wabt().then(w=>{
  const mod=w.parseWat('greeter.wat', require('fs').readFileSync('$watEsc','utf8'));
  mod.resolveNames(); mod.validate();
  const bin=mod.toBinary({write_debug_names:true});
  require('fs').writeFileSync('$wasmEsc', Buffer.from(bin.buffer));
  console.log('wrote', bin.buffer.length, 'bytes to $wasmEsc');
}).catch(e=>{console.error(e); process.exit(1)})
"@
    if ($LASTEXITCODE -ne 0) { throw "wabt compile failed" }
} else {
    # Fallback: try Go build (produces large WASI reactor; host runtime has a proc_exit stub that keeps it alive only for the WAT build)
    Write-Host "wabt not found, trying GOOS=wasip1 GOARCH=wasm go build..."
    Push-Location $greeterDir
    $env:GOOS="wasip1"; $env:GOARCH="wasm"
    try {
        go build -o greeter.wasm . 2>&1 | Write-Host
        if ($LASTEXITCODE -ne 0) { throw "go build failed" }
    } finally {
        Remove-Item Env:GOOS -ErrorAction SilentlyContinue
        Remove-Item Env:GOARCH -ErrorAction SilentlyContinue
        Pop-Location
    }
}

$wasmInfo = Get-Item $wasmPath
Write-Host "Done: $wasmPath ($($wasmInfo.Length) bytes)"
if ($wasmInfo.Length -lt 500) { throw "wasm too small, expected >=500 bytes" }
