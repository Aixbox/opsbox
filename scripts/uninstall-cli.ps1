# Uninstall opsbox CLIs: remove %LOCALAPPDATA%\Programs\opsbox and drop it from the user PATH.
#
# Usage:
#   powershell -ExecutionPolicy Bypass -File scripts\uninstall-cli.ps1
$ErrorActionPreference = 'Stop'
$installRoot = Join-Path $env:LOCALAPPDATA 'Programs\opsbox'

if (Test-Path $installRoot) {
    Remove-Item -Recurse -Force -Confirm:$false $installRoot
    Write-Output "removed: $installRoot"
} else {
    Write-Output 'install dir not found, nothing to remove'
}

$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
$filtered = ($userPath -split ';' | Where-Object { $_ -and ($_ -notlike '*Programs\opsbox*') }) -join ';'
if ($filtered -ne $userPath) {
    [Environment]::SetEnvironmentVariable('Path', $filtered, 'User')
    Write-Output 'PATH updated: opsbox bin entry removed (reopen your terminal to take effect)'
} else {
    Write-Output 'PATH has no opsbox entry'
}
Write-Output 'done.'
