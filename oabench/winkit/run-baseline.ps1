# OpenAir Windows baseline — D-22 / D-23 / D-32
#
# Runs entirely on this one machine over loopback. No second host, no network
# emulation, no firewall prompts, no admin rights.
#
# Why loopback is the right test rather than a compromise: the Windows question
# is per-packet syscall cost, because quic-go falls back to a path with no send
# offload and no batched receive. On loopback there is no network bottleneck, so
# throughput is bounded by exactly that cost. quic-go also paces at a fixed
# ~1200-byte packet size regardless of link MTU, so the comparison against Linux
# holds.

$ErrorActionPreference = 'Continue'
$ProgressPreference    = 'SilentlyContinue'

$root = if ($PSScriptRoot) { $PSScriptRoot } else { (Get-Location).Path }
$arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }
$exe  = Join-Path $root "oabench-$arch.exe"
$out  = Join-Path $root 'results-windows.jsonl'
$addr = '127.0.0.1:9300'
$size = '512MiB'
$srvLog = Join-Path $env:TEMP 'oabench-server.log'

if (-not (Test-Path $exe)) { Write-Host "Missing $exe" -ForegroundColor Red; exit 1 }
if (Test-Path $out) { Remove-Item $out }

$env:QUIC_GO_DISABLE_RECEIVE_BUFFER_WARNING = '1'

Write-Host ""
Write-Host "OpenAir Windows baseline" -ForegroundColor Cyan
Write-Host "  binary   : $exe"
Write-Host "  payload  : $size per run, streams 1 and 4, 2 runs each"
Write-Host "  output   : $out"
Write-Host ""
Write-Host "Takes roughly 3-6 minutes. Close heavy applications first." -ForegroundColor Yellow
Write-Host ""

function Invoke-Sweep {
    param([string]$Transport, [string]$Label)

    Write-Host "== $Label ==" -ForegroundColor Green
    $srv = Start-Process -FilePath $exe `
        -ArgumentList 'serve','-transport',$Transport,'-addr',$addr `
        -NoNewWindow -PassThru -RedirectStandardOutput $srvLog -RedirectStandardError "$srvLog.err"
    Start-Sleep -Milliseconds 900

    & $exe send -transport $Transport -addr $addr -size $size `
        -streams '1,4' -runs 2 -label $Label 1>> $out

    if (-not $srv.HasExited) { Stop-Process -Id $srv.Id -Force -ErrorAction SilentlyContinue }
    Start-Sleep -Milliseconds 400

    $lines = if (Test-Path $out) { (Get-Content $out | Measure-Object -Line).Lines } else { 0 }
    if ($lines -le $script:seen) {
        Write-Host "No results recorded for $Label. Server log:" -ForegroundColor Red
        if (Test-Path "$srvLog.err") { Get-Content "$srvLog.err" | Select-Object -Last 5 }
        Write-Host "The usual cause is port 9300 already in use." -ForegroundColor Yellow
    }
    $script:seen = $lines
    Write-Host ""
}

$script:seen = 0

Invoke-Sweep -Transport 'tcp'  -Label 'win-tcp'
Invoke-Sweep -Transport 'quic' -Label 'win-quic'

Write-Host "Done. Results written to:" -ForegroundColor Cyan
Write-Host "  $out"
Write-Host ""
Write-Host "Paste the contents of that file back. It is one JSON object per line." -ForegroundColor Cyan
Write-Host ""
Write-Host "--- copy from here ---" -ForegroundColor DarkGray
Get-Content $out
Write-Host "--- to here ---" -ForegroundColor DarkGray
