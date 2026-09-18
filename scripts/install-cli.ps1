# Install opsbox CLIs (sshctl / sqlctl / redisctl) to %LOCALAPPDATA%\Programs\opsbox\bin
# and add that directory to the user PATH (idempotent).
#
# Usage (from anywhere):
#   powershell -ExecutionPolicy Bypass -File scripts\install-cli.ps1
#   powershell -ExecutionPolicy Bypass -File scripts\install-cli.ps1 -SkipBuild   # reuse existing bin\*.exe
param(
    [switch]$SkipBuild
)

$ErrorActionPreference = 'Stop'
$root = Join-Path $PSScriptRoot '..'
$binDir = Join-Path $root 'bin'
$installDir = Join-Path $env:LOCALAPPDATA 'Programs\opsbox\bin'

if (-not $SkipBuild) {
    Write-Output 'Building CLIs...'
    if (-not (Test-Path $binDir)) { New-Item -ItemType Directory -Force -Path $binDir | Out-Null }
    $version = 'dev'
    try {
        $describe = git -C $root describe --tags --always --dirty 2>$null
        if ($LASTEXITCODE -eq 0 -and $describe) { $version = "$describe".Trim() }
    } catch {}
    foreach ($tool in 'sshctl', 'sqlctl', 'redisctl') {
        Write-Output "  build $tool"
        go -C $root build -trimpath -ldflags "-s -w -X main.version=$version" -o (Join-Path $binDir "$tool.exe") "./cmd/$tool"
        if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    }
}

if (-not (Test-Path (Join-Path $binDir 'sshctl.exe'))) {
    Write-Error "bin\sshctl.exe not found. Run without -SkipBuild first."
    exit 1
}

New-Item -ItemType Directory -Force -Path $installDir | Out-Null
foreach ($tool in 'sshctl.exe', 'sqlctl.exe', 'redisctl.exe') {
    Copy-Item -Force (Join-Path $binDir $tool) (Join-Path $installDir $tool)
    Write-Output "installed: $installDir\$tool"
}

# Append install dir to the user PATH if missing
$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if ($userPath -notlike "*opsbox\bin*") {
    [Environment]::SetEnvironmentVariable('Path', $userPath.TrimEnd(';') + ';' + $installDir, 'User')
    Write-Output "PATH updated: $installDir appended (reopen your terminal to take effect)"
} else {
    Write-Output 'PATH already contains install dir'
}

& (Join-Path $installDir 'sshctl.exe') --version
Write-Output 'done.'
