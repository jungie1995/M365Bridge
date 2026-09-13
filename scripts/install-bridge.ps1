param(
    [string]$InstallRoot = 'C:\m365bridge',
    [string]$SourceRoot = (Split-Path -Parent $PSScriptRoot),
    [string]$GoExecutable = 'go',
    [int]$TextPort = 8000,
    [int]$ImagePort = 8001,
    [switch]$NoStart
)
$ErrorActionPreference = 'Stop'
$SourceRoot = (Resolve-Path -LiteralPath $SourceRoot).Path
foreach ($required in @('go.mod','pkg\toolcalling\continuity.go','pkg\auth\browser.go')) {
    if (-not (Test-Path -LiteralPath (Join-Path $SourceRoot $required))) { throw "Edited-fork source is incomplete: $required" }
}
$go = (Get-Command $GoExecutable -ErrorAction Stop).Source
$revision = & git -C $SourceRoot rev-parse HEAD
if ($LASTEXITCODE -ne 0) { throw 'Install from a Git checkout of jungie1995/M365Bridge.' }
$dirty = & git -C $SourceRoot status --porcelain --untracked-files=no
if ($dirty) { $revision += '+dirty' }
$build = Join-Path $SourceRoot 'bin'
New-Item -ItemType Directory -Path $build -Force | Out-Null
$candidate = Join-Path $build 'm365-bridge-install-candidate.exe'
$previousCGO = $env:CGO_ENABLED
try {
    $env:CGO_ENABLED = '0'
    Push-Location -LiteralPath $SourceRoot
    try {
        & $go build -trimpath -o $candidate ./cmd/cli
        if ($LASTEXITCODE -ne 0) { throw 'Build failed; the installed bridge was not changed.' }
    } finally { Pop-Location }
} finally { $env:CGO_ENABLED = $previousCGO }
& (Join-Path $SourceRoot 'scripts\install-verified-bridge.ps1') -CandidatePath $candidate -InstallRoot $InstallRoot -SourceRevision $revision -TextPort $TextPort -ImagePort $ImagePort -NoStart:$NoStart
$runtimeScripts = Join-Path $InstallRoot 'scripts'
New-Item -ItemType Directory -Path $runtimeScripts -Force | Out-Null
Copy-Item -LiteralPath (Join-Path $SourceRoot 'scripts\start-bridge.ps1') -Destination $runtimeScripts -Force
Copy-Item -LiteralPath (Join-Path $SourceRoot 'scripts\connect-microsoft.cmd') -Destination (Join-Path $InstallRoot 'connect-microsoft.cmd') -Force
"Installed our fork in $InstallRoot. Fresh installations: connect the Microsoft account using setup-wizard from that directory, then run scripts\start-bridge.ps1. The client API key is stored privately in data\.env."
