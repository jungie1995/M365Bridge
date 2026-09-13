param(
    [string]$InstallRoot = (Split-Path -Parent $PSScriptRoot),
    [ValidateSet('both','text','images')][string]$Mode = 'both',
    [int]$TextPort = 8000,
    [int]$ImagePort = 8001
)
$ErrorActionPreference = 'Stop'
$InstallRoot = (Resolve-Path -LiteralPath $InstallRoot).Path
if (-not (Test-Path -LiteralPath (Join-Path $InstallRoot 'data\tokens\token_cache.json'))) {
    throw 'Connect the Microsoft account with setup-wizard before starting the bridge.'
}
$previousRoute = $env:M365_BROWSER_IMAGE_ROUTING
try {
    $modes = @($Mode)
    if ($Mode -eq 'both') { $modes = @('text','images') }
    foreach ($kind in $modes) {
        $port = $TextPort
        $binary = Join-Path $InstallRoot 'm365-bridge.exe'
        $env:M365_BROWSER_IMAGE_ROUTING = $null
        if ($kind -eq 'images') {
            $port = $ImagePort
            $binary = Join-Path $InstallRoot 'm365-bridge-images.exe'
            $env:M365_BROWSER_IMAGE_ROUTING = '1'
        }
        if (Get-NetTCPConnection -LocalPort $port -State Listen -ErrorAction SilentlyContinue) { throw "Port $port is already in use." }
        $process = Start-Process -FilePath $binary -ArgumentList 'serve','--port',"$port" -WorkingDirectory $InstallRoot -WindowStyle Hidden -PassThru
        [pscustomobject]@{ mode = $kind; port = $port; pid = $process.Id }
    }
} finally { $env:M365_BROWSER_IMAGE_ROUTING = $previousRoute }
