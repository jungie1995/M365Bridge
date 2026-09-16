param(
    [Parameter(Mandatory=$true)][string]$CandidatePath,
    [string]$InstallRoot = 'C:\m365bridge',
    [string]$SourceRevision = 'unknown',
    [int]$TextPort = 8000,
    [int]$ImagePort = 8001,
    [switch]$NoStart
)
$ErrorActionPreference = 'Stop'
$existingManifest = Join-Path $InstallRoot 'install-manifest.json'
if (Test-Path -LiteralPath $existingManifest) {
    $existing = Get-Content -LiteralPath $existingManifest -Raw | ConvertFrom-Json
    if (-not $PSBoundParameters.ContainsKey('TextPort')) { $TextPort = [int]$existing.text_port }
    if (-not $PSBoundParameters.ContainsKey('ImagePort')) { $ImagePort = [int]$existing.image_port }
}
if ($TextPort -lt 1024 -or $TextPort -gt 65535 -or $ImagePort -lt 1024 -or $ImagePort -gt 65535 -or $TextPort -eq $ImagePort) { throw 'Choose two distinct ports between 1024 and 65535.' }
if (-not (Test-Path -LiteralPath $CandidatePath -PathType Leaf)) { throw 'Candidate binary does not exist.' }
$candidate = (Resolve-Path -LiteralPath $CandidatePath).Path
$help = (& $candidate --help 2>&1 | Out-String)
if ($LASTEXITCODE -ne 0 -or $help -notmatch 'task-continuity' -or $help -notmatch 'browser-first-login') {
    throw 'Candidate is not the edited M365Bridge fork; build this repository before installing.'
}
if (-not (Test-Path -LiteralPath $InstallRoot -PathType Container)) {
    New-Item -ItemType Directory -Path $InstallRoot -Force | Out-Null
}
$InstallRoot = (Resolve-Path -LiteralPath $InstallRoot).Path
$hash = (Get-FileHash -LiteralPath $candidate -Algorithm SHA256).Hash
$targets = @('m365-bridge.exe', 'm365-bridge-images.exe', 'm365-bridge-browser-login.exe')
$freshInstall = -not (Test-Path -LiteralPath (Join-Path $InstallRoot 'm365-bridge.exe'))
$startAfterInstall = -not $NoStart -and -not $freshInstall
$paths = @($targets | ForEach-Object { Join-Path $InstallRoot $_ })
$managed = @(Get-Process -Name 'm365-bridge','m365-bridge-images','m365-bridge-browser-login' -ErrorAction SilentlyContinue | Where-Object { $_.Path -in $paths })
if (@($managed | Where-Object { $_.ProcessName -eq 'm365-bridge-browser-login' }).Count -gt 0) { throw 'Finish the current browser sign-in before upgrading.' }
$active = @(Get-NetTCPConnection -State Established -ErrorAction SilentlyContinue | Where-Object { $_.LocalPort -in @($TextPort,$ImagePort) -and $_.OwningProcess -in $managed.Id })
if ($active.Count -gt 0) { throw 'Bridge clients are connected; retry installation when the endpoints are idle.' }
# Only manage the legacy task when its running image process belongs to this
# installation. Installing a second copy must never stop the production task.
$imageTask = $null
if (@($managed | Where-Object { $_.ProcessName -eq 'm365-bridge-images' }).Count -gt 0) {
    $imageTask = Get-ScheduledTask -TaskName 'ContentStudio-ImageBridge' -ErrorAction SilentlyContinue
}
$stamp = Get-Date -Format 'yyyyMMdd-HHmmss-fffffff'
$backups = @{}
foreach ($name in $targets) {
    $path = Join-Path $InstallRoot $name
    if ($path -eq $candidate) { throw 'Candidate must be a separate file.' }
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { $backups[$name] = $null; continue }
    $backup = Join-Path $InstallRoot ($name.Replace('.exe', ".before-upgrade-$stamp.exe"))
    if (Test-Path -LiteralPath $backup) { throw 'Backup already exists.' }
    Copy-Item -LiteralPath $path -Destination $backup
    $backups[$name] = $backup
}

$data = Join-Path $InstallRoot 'data'
if (-not (Test-Path -LiteralPath $data)) {
    New-Item -ItemType Directory -Path $data | Out-Null
    $acl = Get-Acl -LiteralPath $data
    $acl.SetAccessRuleProtection($true,$false)
    $owner = [System.Security.Principal.WindowsIdentity]::GetCurrent().User
    foreach ($sid in @($owner, [System.Security.Principal.SecurityIdentifier]::new('S-1-5-18'))) {
        $acl.AddAccessRule([System.Security.AccessControl.FileSystemAccessRule]::new($sid,'FullControl','ContainerInherit,ObjectInherit','None','Allow'))
    }
    Set-Acl -LiteralPath $data -AclObject $acl
}
$envFile = Join-Path $data '.env'
if (-not (Test-Path -LiteralPath $envFile)) {
    $bytes = New-Object byte[] 32
    $rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
    try { $rng.GetBytes($bytes) } finally { $rng.Dispose() }
    $key = [BitConverter]::ToString($bytes).Replace('-','').ToLowerInvariant()
    [IO.File]::WriteAllText($envFile,"# Local gateway settings; keep private.`nM365_API_KEY=$key`nM365_MAX_TOOL_ROUNDS=128`nM365_ENABLE_CODE_TOOLS=0`n",[Text.UTF8Encoding]::new($false))
}

function Stop-BridgeProcesses {
    if ($imageTask -and $imageTask.State -eq 'Running') {
        Stop-ScheduledTask -InputObject $imageTask -ErrorAction SilentlyContinue
    }
    Get-Process -Name 'm365-bridge','m365-bridge-images' -ErrorAction SilentlyContinue |
        Where-Object { $_.Path -in $paths } |
        Stop-Process -ErrorAction SilentlyContinue
    Start-Sleep -Milliseconds 500
}

function Start-BridgeProcesses {
    if (-not $startAfterInstall) { return }
    $previousRoute = $env:M365_BROWSER_IMAGE_ROUTING
    $previousConfigSource = $env:M365_CONFIG_FROM_INSTALLATION
    try {
        $env:M365_CONFIG_FROM_INSTALLATION = '1'
        $env:M365_BROWSER_IMAGE_ROUTING = '0'
        Start-Process -FilePath (Join-Path $InstallRoot 'm365-bridge.exe') -ArgumentList 'serve','--port',"$TextPort" -WorkingDirectory $InstallRoot -WindowStyle Hidden
        if ($imageTask) {
            Start-ScheduledTask -InputObject $imageTask
        } else {
            $env:M365_BROWSER_IMAGE_ROUTING = '1'
            Start-Process -FilePath (Join-Path $InstallRoot 'm365-bridge-images.exe') -ArgumentList 'serve','--port',"$ImagePort" -WorkingDirectory $InstallRoot -WindowStyle Hidden
        }
    } finally {
        $env:M365_BROWSER_IMAGE_ROUTING = $previousRoute
        $env:M365_CONFIG_FROM_INSTALLATION = $previousConfigSource
    }
    $deadline = (Get-Date).AddSeconds(30)
    do {
        $listeners = @(Get-NetTCPConnection -LocalPort $TextPort,$ImagePort -State Listen -ErrorAction SilentlyContinue)
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
        if ($backups[$name]) {
            Copy-Item -LiteralPath $backups[$name] -Destination (Join-Path $InstallRoot $name) -Force
        } else {
            Remove-Item -LiteralPath (Join-Path $InstallRoot $name) -ErrorAction SilentlyContinue
        }
    }
    Start-BridgeProcesses
    throw $failure
}
$manifest = [ordered]@{ setup_version = 2; repository = 'https://github.com/jungie1995/M365Bridge'; revision = $SourceRevision; sha256 = $hash; installed = $targets; text_port = $TextPort; image_port = $ImagePort; installed_at = (Get-Date).ToUniversalTime().ToString('o') }
[IO.File]::WriteAllText((Join-Path $InstallRoot 'install-manifest.json'),($manifest | ConvertTo-Json -Depth 3),[Text.UTF8Encoding]::new($false))
[pscustomobject]@{ sha256 = $hash; revision = $SourceRevision; installed = $targets; backups = $backups; started = $startAfterInstall; account_setup_required = (-not (Test-Path -LiteralPath (Join-Path $data 'tokens\token_cache.json'))) } | ConvertTo-Json -Depth 3
