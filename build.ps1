$ErrorActionPreference = "Stop"

$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = [Security.Principal.WindowsPrincipal]$identity
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    Write-Error "Build must be run as administrator (required for testing)"
    exit 1
}

Write-Host "Building temporal-color-depth-switcher..."

if (Test-Path "build") {
    Remove-Item -Recurse -Force "build"
}
New-Item -ItemType Directory -Path "build" | Out-Null

go build -ldflags "-H windowsgui -s -w" -o "build/temporal-color-depth-switcher.exe" .

if ($LASTEXITCODE -ne 0) {
    Write-Error "Build failed"
    exit 1
}

Copy-Item "config.json" "build/config.json" -ErrorAction SilentlyContinue

Write-Host "Build complete: build/temporal-color-depth-switcher.exe"
