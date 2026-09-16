param(
    [string]$InstallRoot = (Join-Path $env:LOCALAPPDATA 'M365Bridge'),
    [switch]$NoLogin,
    [switch]$RegisterAutoStart
)
$ErrorActionPreference = 'Stop'
foreach ($dependency in @(@('git.exe','Git.Git'), @('go.exe','GoLang.Go'))) {
    if (-not (Get-Command $dependency[0] -ErrorAction SilentlyContinue)) {
        if (-not (Get-Command winget.exe -ErrorAction SilentlyContinue)) { throw 'Install Windows App Installer (winget), then run setup again.' }
        & winget.exe install --id $dependency[1] --exact --silent --accept-package-agreements --accept-source-agreements
        if ($LASTEXITCODE -ne 0) { throw "Could not install $($dependency[1]). Run setup again after resolving the installer error." }
        $env:Path = $env:Path + ';' + [Environment]::GetEnvironmentVariable('Path','Machine') + ';' + [Environment]::GetEnvironmentVariable('Path','User')
    }
}
$source = Split-Path -Parent $PSScriptRoot
if (-not (@("${env:ProgramFiles(x86)}\Microsoft\Edge\Application\msedge.exe", "$env:ProgramFiles\Microsoft\Edge\Application\msedge.exe", "$env:LOCALAPPDATA\Microsoft\Edge\Application\msedge.exe") | Where-Object { Test-Path -LiteralPath $_ })) {
    & winget.exe install --id Microsoft.Edge --exact --silent --accept-package-agreements --accept-source-agreements
    if ($LASTEXITCODE -ne 0) { throw 'Microsoft Edge could not be installed. Install Edge and rerun setup for browser sign-in.' }
}
& (Join-Path $PSScriptRoot 'install-bridge.ps1') -SourceRoot $source -InstallRoot $InstallRoot -NoStart
if (-not $NoLogin) { & (Join-Path $InstallRoot 'scripts\connect-microsoft.ps1') -InstallRoot $InstallRoot }
if ($RegisterAutoStart) {
    $args = '-NoProfile -WindowStyle Hidden -ExecutionPolicy Bypass -File "' + (Join-Path $InstallRoot 'scripts\start-bridge.ps1') + '" -InstallRoot "' + $InstallRoot + '"'
    $action = New-ScheduledTaskAction -Execute 'powershell.exe' -Argument $args
    $trigger = New-ScheduledTaskTrigger -AtLogOn -User $env:USERNAME
    Register-ScheduledTask -TaskName 'M365Bridge Local' -Action $action -Trigger $trigger -Description 'Starts the local text and image bridges after Windows sign-in.' -Force | Out-Null
}
Write-Host "Bridge installed at $InstallRoot. This PC keeps its own account and private gateway key."
