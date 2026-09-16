# No real processes, sockets, credentials or Microsoft calls are used.
$ErrorActionPreference = 'Stop'
$fixture = Join-Path ([IO.Path]::GetTempPath()) ('bridge-startup-test-' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path (Join-Path $fixture 'data\tokens') -Force | Out-Null
[IO.File]::WriteAllText((Join-Path $fixture 'data\tokens\rt_90day.txt'), 'fixture-only')
[IO.File]::WriteAllText((Join-Path $fixture 'install-manifest.json'), '{"text_port":18000,"image_port":18001}')
$global:BridgeStartupTest = @{ starts=@(); listeners=@{}; foreign=$false; fixture=$fixture }
function Get-NetTCPConnection {
    param($LocalPort,$State,$ErrorAction)
    if ($global:BridgeStartupTest.foreign) { return [pscustomobject]@{OwningProcess=999} }
    if ($global:BridgeStartupTest.listeners.ContainsKey([int]$LocalPort)) { return [pscustomobject]@{OwningProcess=$global:BridgeStartupTest.listeners[[int]$LocalPort].Id} }
}
function Get-Process {
    param($Id,$ErrorAction)
    if ($global:BridgeStartupTest.foreign) { return [pscustomobject]@{Path='C:\unrelated.exe'} }
    return @($global:BridgeStartupTest.listeners.Values | Where-Object Id -eq $Id)[0]
}
function Start-Process {
    param($FilePath,$ArgumentList,$WorkingDirectory,$WindowStyle,[switch]$PassThru)
    if ($WorkingDirectory -ne $global:BridgeStartupTest.fixture -or $WindowStyle -ne 'Hidden') { throw 'Unexpected process launch boundary.' }
    if ($env:M365_CONFIG_FROM_INSTALLATION -ne '1') { throw 'Managed launch could inherit stale credentials.' }
    $process = [pscustomobject]@{Id=(100+$global:BridgeStartupTest.starts.Count); Path=$FilePath; HasExited=$false}
    $global:BridgeStartupTest.starts += [pscustomobject]@{path=$FilePath; route=$env:M365_BROWSER_IMAGE_ROUTING; port=[int]$ArgumentList[-1]}
    $global:BridgeStartupTest.listeners[[int]$ArgumentList[-1]] = $process
    return $process
}
$starter = Join-Path $PSScriptRoot 'start-bridge.ps1'
$previousRoute = $env:M365_BROWSER_IMAGE_ROUTING
& $starter -InstallRoot $fixture | Out-Null
if ($global:BridgeStartupTest.starts.Count -ne 2 -or $global:BridgeStartupTest.starts[0].route -ne '0' -or $global:BridgeStartupTest.starts[1].route -ne '1') { throw 'Text and image routing did not stay separate.' }
if ($global:BridgeStartupTest.starts[0].port -ne 18000 -or $global:BridgeStartupTest.starts[1].port -ne 18001) { throw 'Custom manifest ports were ignored.' }
& $starter -InstallRoot $fixture | Out-Null
if ($global:BridgeStartupTest.starts.Count -ne 2) { throw 'Second start created duplicate processes.' }
$global:BridgeStartupTest.foreign = $true
$rejected = $false
try { & $starter -InstallRoot $fixture | Out-Null } catch { $rejected = $true }
if (-not $rejected -or $global:BridgeStartupTest.starts.Count -ne 2) { throw 'Foreign listener was not protected.' }
if ($env:M365_BROWSER_IMAGE_ROUTING -ne $previousRoute) { throw 'Image flag leaked to the parent process.' }
Write-Host 'PASS: custom ports, text/image flags, hidden launches, idempotent start, foreign-process protection, parent environment preservation.'
