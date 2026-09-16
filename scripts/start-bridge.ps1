param(
    [string]$InstallRoot = (Split-Path -Parent $PSScriptRoot),
    [ValidateSet('both','text','images')][string]$Mode = 'both',
    [int]$TextPort = 8000,
    [int]$ImagePort = 8001,
    [switch]$ConnectIfNeeded
)
$ErrorActionPreference = 'Stop'
$InstallRoot = (Resolve-Path -LiteralPath $InstallRoot).Path
$manifestPath = Join-Path $InstallRoot 'install-manifest.json'
if (Test-Path -LiteralPath $manifestPath) {
    $manifest = Get-Content -LiteralPath $manifestPath -Raw | ConvertFrom-Json
    if (-not $PSBoundParameters.ContainsKey('TextPort')) { $TextPort = [int]$manifest.text_port }
    if (-not $PSBoundParameters.ContainsKey('ImagePort')) { $ImagePort = [int]$manifest.image_port }
}
if ($TextPort -lt 1024 -or $TextPort -gt 65535 -or $ImagePort -lt 1024 -or $ImagePort -gt 65535 -or $TextPort -eq $ImagePort) { throw 'Choose two distinct ports between 1024 and 65535.' }
if (-not (Test-Path -LiteralPath (Join-Path $InstallRoot 'data\tokens\rt_90day.txt'))) {
    if (-not $ConnectIfNeeded) { throw 'Microsoft sign-in is required. Click connect-microsoft.cmd, or Connect Microsoft account in Content Studio.' }
    Push-Location -LiteralPath $InstallRoot
    $previousLoginConfigSource = $env:M365_CONFIG_FROM_INSTALLATION
    try {
        $env:M365_CONFIG_FROM_INSTALLATION = '1'
        & (Join-Path $InstallRoot 'm365-bridge-browser-login.exe') login-browser
        if ($LASTEXITCODE -ne 0) { throw 'Microsoft sign-in did not finish. Run connect-microsoft.cmd to try again.' }
    } finally {
        $env:M365_CONFIG_FROM_INSTALLATION = $previousLoginConfigSource
        Pop-Location
    }
}
$previousRoute = $env:M365_BROWSER_IMAGE_ROUTING
$previousConfigSource = $env:M365_CONFIG_FROM_INSTALLATION
try {
    $env:M365_CONFIG_FROM_INSTALLATION = '1'
    $modes = @($Mode)
    if ($Mode -eq 'both') { $modes = @('text','images') }
    foreach ($kind in $modes) {
        $port = $TextPort
        $binary = Join-Path $InstallRoot 'm365-bridge.exe'
        $env:M365_BROWSER_IMAGE_ROUTING = '0'
        if ($kind -eq 'images') {
            $port = $ImagePort
            $binary = Join-Path $InstallRoot 'm365-bridge-images.exe'
            $env:M365_BROWSER_IMAGE_ROUTING = '1'
        }
        $listeners = @(Get-NetTCPConnection -LocalPort $port -State Listen -ErrorAction SilentlyContinue)
        if ($listeners.Count) {
            foreach ($listener in $listeners) {
                $owner = Get-Process -Id $listener.OwningProcess -ErrorAction Stop
                if ($owner.Path -ne $binary) { throw "Port $port belongs to another process. It was not stopped. Close the old bridge or choose different ports during setup." }
            }
            [pscustomobject]@{ mode = $kind; port = $port; state = 'already running' }
            continue
        }
        $process = Start-Process -FilePath $binary -ArgumentList 'serve','--port',"$port" -WorkingDirectory $InstallRoot -WindowStyle Hidden -PassThru
        $deadline = (Get-Date).AddSeconds(20)
        do {
            if ($process.HasExited) { throw "$kind bridge exited while starting. Check Microsoft sign-in and the private data folder." }
            $ready = Get-NetTCPConnection -LocalPort $port -State Listen -ErrorAction SilentlyContinue | Where-Object OwningProcess -eq $process.Id
            if ($ready) { break }
            Start-Sleep -Milliseconds 300
        } while ((Get-Date) -lt $deadline)
        if (-not $ready) { throw "$kind bridge did not start listening on port $port within 20 seconds." }
        [pscustomobject]@{ mode = $kind; port = $port; pid = $process.Id }
    }
} finally {
    $env:M365_BROWSER_IMAGE_ROUTING = $previousRoute
    $env:M365_CONFIG_FROM_INSTALLATION = $previousConfigSource
}
