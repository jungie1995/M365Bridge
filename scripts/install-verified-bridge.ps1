param(
    [Parameter(Mandatory=$true)][string]$CandidatePath,
    [string]$InstallRoot = 'C:\m365bridge'
)
$ErrorActionPreference = 'Stop'
if (-not (Test-Path -LiteralPath $InstallRoot -PathType Container)) { throw 'Install directory does not exist.' }
if (-not (Test-Path -LiteralPath $CandidatePath -PathType Leaf)) { throw 'Candidate binary does not exist.' }
$candidate = (Resolve-Path -LiteralPath $CandidatePath).Path
$hash = (Get-FileHash -LiteralPath $candidate -Algorithm SHA256).Hash
$targets = @('m365-bridge.exe', 'm365-bridge-images.exe')
$stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
$backups = @{}
foreach ($name in $targets) {
    $path = Join-Path $InstallRoot $name
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw "Installed binary is missing: $name" }
    if ($path -eq $candidate) { throw 'Candidate must be a separate file.' }
    $backup = Join-Path $InstallRoot ($name.Replace('.exe', ".before-continuity-$stamp.exe"))
    if (Test-Path -LiteralPath $backup) { throw 'Backup already exists.' }
    Copy-Item -LiteralPath $path -Destination $backup
    $backups[$name] = $backup
}

# Refuse to interrupt an established client connection. This is an installation
# operation, not an instruction to cancel any running agent task.
$active = @(Get-NetTCPConnection -LocalPort 8000,8001 -State Established -ErrorAction SilentlyContinue)
if ($active.Count -gt 0) { throw 'Bridge clients are connected; retry installation when the endpoints are idle.' }
$imageTask = Get-ScheduledTask -TaskName 'ContentStudio-ImageBridge' -ErrorAction SilentlyContinue

function Stop-BridgeProcesses {
    if ($imageTask -and $imageTask.State -eq 'Running') {
        Stop-ScheduledTask -InputObject $imageTask
    }
    Get-Process -Name 'm365-bridge','m365-bridge-images' -ErrorAction SilentlyContinue |
        Where-Object { $_.Path -in @((Join-Path $InstallRoot 'm365-bridge.exe'), (Join-Path $InstallRoot 'm365-bridge-images.exe')) } |
        Stop-Process
    Start-Sleep -Milliseconds 500
}

function Start-BridgeProcesses {
    $previousRoute = $env:M365_BROWSER_IMAGE_ROUTING
    try {
        $env:M365_BROWSER_IMAGE_ROUTING = $null
        Start-Process -FilePath (Join-Path $InstallRoot 'm365-bridge.exe') -ArgumentList 'serve','--port','8000' -WorkingDirectory $InstallRoot -WindowStyle Hidden
        if ($imageTask) {
            Start-ScheduledTask -InputObject $imageTask
        } else {
            $env:M365_BROWSER_IMAGE_ROUTING = '1'
            Start-Process -FilePath (Join-Path $InstallRoot 'm365-bridge-images.exe') -ArgumentList 'serve','--port','8001' -WorkingDirectory $InstallRoot -WindowStyle Hidden
        }
    } finally {
        $env:M365_BROWSER_IMAGE_ROUTING = $previousRoute
    }
    $deadline = (Get-Date).AddSeconds(30)
    do {
        $listeners = @(Get-NetTCPConnection -LocalPort 8000,8001 -State Listen -ErrorAction SilentlyContinue)
        if (@($listeners.LocalPort | Sort-Object -Unique).Count -eq 2) { return }
        Start-Sleep -Milliseconds 500
    } while ((Get-Date) -lt $deadline)
    throw 'Both bridge listeners did not return after installation.'
}

try {
    Stop-BridgeProcesses
    foreach ($name in $targets) {
        $path = Join-Path $InstallRoot $name
        Copy-Item -LiteralPath $candidate -Destination $path -Force
        if ((Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash -ne $hash) { throw 'Installed binary hash mismatch.' }
    }
    Start-BridgeProcesses
} catch {
    $failure = $_
    Stop-BridgeProcesses
    foreach ($name in $targets) {
        Copy-Item -LiteralPath $backups[$name] -Destination (Join-Path $InstallRoot $name) -Force
    }
    Start-BridgeProcesses
    throw $failure
}
[pscustomobject]@{ sha256 = $hash; installed = $targets; backups = $backups } | ConvertTo-Json -Depth 3
