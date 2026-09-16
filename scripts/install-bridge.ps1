param(
    [string]$InstallRoot = 'C:\m365bridge',
    [string]$SourceRoot = (Split-Path -Parent $PSScriptRoot),
    [string]$GoExecutable = 'go',
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
$SourceRoot = (Resolve-Path -LiteralPath $SourceRoot).Path
foreach ($required in @('go.mod','pkg\toolcalling\continuity.go','pkg\auth\browser.go')) {
    if (-not (Test-Path -LiteralPath (Join-Path $SourceRoot $required))) { throw "Edited-fork source is incomplete: $required" }
}
$go = (Get-Command $GoExecutable -ErrorAction SilentlyContinue).Source
if (-not $go) { throw 'Go is not installed. Double-click setup-bridge.cmd to install build dependencies automatically.' }
$revision = 'source-archive'
if (Test-Path -LiteralPath (Join-Path $SourceRoot '.git')) {
    $revision = & git -C $SourceRoot rev-parse HEAD
    if ($LASTEXITCODE -ne 0) { throw 'Cannot read the source checkout revision. Check Git permissions.' }
    $dirty = & git -C $SourceRoot status --porcelain --untracked-files=no
    if ($LASTEXITCODE -ne 0) { throw 'Cannot check the source checkout changes. Check Git permissions.' }
    if ($dirty) { $revision += '+dirty' }
}
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
foreach ($pair in @(
    @('scripts\start-bridge.ps1','scripts\start-bridge.ps1'),
    @('scripts\connect-microsoft.ps1','scripts\connect-microsoft.ps1'),
    @('connect-microsoft.cmd','connect-microsoft.cmd'),
    @('start-bridges.cmd','start-bridges.cmd')
)) {
    $sourceFile = [IO.Path]::GetFullPath((Join-Path $SourceRoot $pair[0]))
    $targetFile = [IO.Path]::GetFullPath((Join-Path $InstallRoot $pair[1]))
    if ($sourceFile -ne $targetFile) { Copy-Item -LiteralPath $sourceFile -Destination $targetFile -Force }
}
"Installed our fork in $InstallRoot. Click connect-microsoft.cmd for first sign-in or reconnect. Both bridges start after successful login. No browser-console export or account ID copying is needed."
