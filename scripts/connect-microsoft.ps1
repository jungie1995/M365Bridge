param(
    [string]$InstallRoot = (Split-Path -Parent $PSScriptRoot),
    [string]$AttemptId = ''
)
$ErrorActionPreference = 'Stop'
if (-not $AttemptId) { $AttemptId = [Guid]::NewGuid().ToString('N') }
$InstallRoot = (Resolve-Path -LiteralPath $InstallRoot).Path
Push-Location -LiteralPath $InstallRoot
$previousConfigSource = $env:M365_CONFIG_FROM_INSTALLATION
try {
    $env:M365_CONFIG_FROM_INSTALLATION = '1'
    & (Join-Path $InstallRoot 'm365-bridge-browser-login.exe') login-browser --attempt-id $AttemptId
    if ($LASTEXITCODE -ne 0) { throw 'Microsoft sign-in did not complete. Enter your password and MFA only in the Microsoft browser window.' }
    & (Join-Path $PSScriptRoot 'start-bridge.ps1') -InstallRoot $InstallRoot
} catch {
    $status = @{ state='failed'; message='Sign-in or bridge startup did not complete. Run connect-microsoft.cmd to see the local error; check for occupied ports and complete Microsoft sign-in.'; updated_at=(Get-Date).ToUniversalTime().ToString('o'); pid=$PID; attempt_id=$AttemptId }
    $statusPath = Join-Path $InstallRoot 'data\browser-login-status.json'
    if (Test-Path -LiteralPath $statusPath) {
        $previous = Get-Content -LiteralPath $statusPath -Raw | ConvertFrom-Json
        if ($previous.state -eq 'failed' -and $previous.attempt_id -eq $AttemptId) { $status.message = $previous.message }
    }
    [IO.File]::WriteAllText($statusPath, ($status | ConvertTo-Json), [Text.UTF8Encoding]::new($false))
    throw
} finally {
    $env:M365_CONFIG_FROM_INSTALLATION = $previousConfigSource
    Pop-Location
}
