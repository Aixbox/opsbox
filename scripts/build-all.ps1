# Full release build for opsbox (Windows):
#   1. Build CLIs (sshctl / sqlctl / redisctl) into internal/climgr/clis (go:embed source)
#   2. Run `wails build` so the desktop exe embeds them (in-app one-click install)
#   3. Optional -NSIS (release mode): additionally build the installer (setup exe)
#      and the portable zip (免安装版), both named with the version
#
# macOS 对应脚本：scripts/build-all.sh（universal .app + DMG 安装版 + portable 免安装版）。
#
# Usage:
#   powershell -ExecutionPolicy Bypass -File scripts\build-all.ps1
#   powershell -ExecutionPolicy Bypass -File scripts\build-all.ps1 -Version 0.2.0
#   powershell -ExecutionPolicy Bypass -File scripts\build-all.ps1 -Version 0.2.0 -NSIS
param(
    [string]$Version = "",
    [switch]$NSIS
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
    $wailsArgs = @('build', '-ldflags', "-X main.appVersion=$Version")
    if ($NSIS) { $wailsArgs += '-nsis' }
    & $wails.Source @wailsArgs
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
} finally {
    Pop-Location
}

Write-Output "done: build\bin\opsbox.exe (CLIs embedded: $Version)"

# 3) Release packaging (-NSIS)：安装版 + 免安装版，文件名带版本号。
#    注意：Wails 找不到 makensis 时只打警告并静默跳过安装包生成，这里提前校验，
#    避免 CI 上构建"成功"却没有安装包的假成功。
if ($NSIS) {
    $binDir = Join-Path $root 'build\bin'
    $rawInstaller = Join-Path $binDir 'opsbox-amd64-installer.exe'
    $installer = Join-Path $binDir "opsbox-$Version-windows-installer.exe"
    $portableZip = Join-Path $binDir "opsbox-$Version-windows-portable.zip"

    if (-not (Get-Command makensis -ErrorAction SilentlyContinue)) {
        Write-Error 'makensis not found on PATH: wails would silently skip the installer. Install NSIS first (e.g. choco install nsis).'
        exit 1
    }
    if (-not (Test-Path $rawInstaller)) { Write-Error "installer missing after wails build: $rawInstaller"; exit 1 }
    Move-Item -Force $rawInstaller $installer

    # 免安装版：单 exe 全自包含（CLI 已内嵌），zip 打包避免浏览器拦截裸 exe 下载
    Compress-Archive -Force -Path (Join-Path $binDir 'opsbox.exe') -DestinationPath $portableZip

    Write-Output "done: $installer (CLIs embedded: $Version)"
    Write-Output "done: $portableZip (CLIs embedded: $Version)"
}
