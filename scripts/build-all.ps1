# Full release build for opsbox:
#   1. Build CLIs (sshctl / sqlctl / redisctl) into internal/climgr/clis (go:embed source)
#   2. Run `wails build` so the desktop exe embeds them (in-app one-click install)
#
# Usage:
#   powershell -ExecutionPolicy Bypass -File scripts\build-all.ps1
#   powershell -ExecutionPolicy Bypass -File scripts\build-all.ps1 -Version 0.2.0
param(
    [string]$Version = ""
)

$ErrorActionPreference = 'Stop'
$root = Join-Path $PSScriptRoot '..'
$clisDir = Join-Path $root 'internal\climgr\clis'

if (-not $Version) {
    $Version = 'dev'
    try {
        $describe = git -C $root describe --tags --always --dirty 2>$null
        if ($LASTEXITCODE -eq 0 -and $describe) { $Version = "$describe".Trim() }
    } catch {}
}

# 1) CLIs -> embed directory (bin\ copies kept for dev use as well)
New-Item -ItemType Directory -Force -Path $clisDir, (Join-Path $root 'bin') | Out-Null
foreach ($tool in 'sshctl', 'sqlctl', 'redisctl') {
    Write-Output "build $tool ($Version)"
    go -C $root build -trimpath -ldflags "-s -w -X main.version=$Version" -o (Join-Path $clisDir "$tool.exe") "./cmd/$tool"
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    Copy-Item -Force (Join-Path $clisDir "$tool.exe") (Join-Path (Join-Path $root 'bin') "$tool.exe")
}
Set-Content -Path (Join-Path $clisDir 'VERSION') -Value $Version

# 2) Desktop app (locates wails.exe via PATH, then GOPATH\bin)
$wails = Get-Command wails -ErrorAction SilentlyContinue
if (-not $wails) {
    $gopath = (& go env GOPATH).Trim()
    $candidate = Join-Path $gopath 'bin\wails.exe'
    if (Test-Path $candidate) { $wails = @{ Source = $candidate } }
}
if (-not $wails) { Write-Error 'wails not found (install with: go install github.com/wailsapp/wails/v2/cmd/wails@latest)'; exit 1 }
Write-Output "wails build (version $Version)"
Push-Location $root
try {
    & $wails.Source build -ldflags "-X main.appVersion=$Version"
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
} finally {
    Pop-Location
}

Write-Output "done: build\bin\opsbox.exe (CLIs embedded: $Version)"
