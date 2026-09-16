param(
    [Parameter(Mandatory=$true)][string]$TestRoot,
    [string]$GoExecutable = 'go'
)
$ErrorActionPreference = 'Stop'
if (Test-Path -LiteralPath $TestRoot) { throw 'Choose a new disposable test directory.' }
New-Item -ItemType Directory -Path $TestRoot | Out-Null
$source = Split-Path -Parent $PSScriptRoot
$runningBefore = @(Get-Process -Name 'm365-bridge','m365-bridge-images' -ErrorAction SilentlyContinue | ForEach-Object { $_.Id })
$install = Join-Path $TestRoot 'fresh install with spaces'
& (Join-Path $PSScriptRoot 'install-bridge.ps1') -InstallRoot $install -SourceRoot $source -GoExecutable $GoExecutable -TextPort 18000 -ImagePort 18001 -NoStart
$names = @('m365-bridge.exe','m365-bridge-images.exe','m365-bridge-browser-login.exe')
$manifest = [IO.File]::ReadAllText((Join-Path $install 'install-manifest.json')) | ConvertFrom-Json
foreach ($name in $names) {
    if ((Get-FileHash -LiteralPath (Join-Path $install $name)).Hash -ne $manifest.sha256) { throw 'Fresh binary aliases differ.' }
}
if ($manifest.repository -ne 'https://github.com/jungie1995/M365Bridge') { throw 'Wrong source repository recorded.' }
if ($manifest.setup_version -ne 2) { throw 'First-login setup contract was not recorded.' }
$settingsPath = Join-Path $install 'data\.env'
$settings = [IO.File]::ReadAllText($settingsPath)
if ($settings -notmatch '(?m)^M365_API_KEY=[0-9a-f]{64}\r?$' -or $settings -notmatch 'M365_MAX_TOOL_ROUNDS=128') { throw 'Fresh installation defaults were not initialized.' }
if (-not (Test-Path -LiteralPath (Join-Path $install 'scripts\start-bridge.ps1'))) { throw 'Starter was not installed.' }
if (-not (Test-Path -LiteralPath (Join-Path $install 'connect-microsoft.cmd'))) { throw 'Reconnect shortcut was not installed.' }
$tokenDir = Join-Path $install 'data\tokens'
New-Item -ItemType Directory -Path $tokenDir | Out-Null
[IO.File]::WriteAllText((Join-Path $tokenDir 'token_cache.json'),'{"fixture":true}')
[IO.File]::WriteAllText((Join-Path $install 'data\setup.json'),'{"fixture":true}')
$protected = @($settingsPath,(Join-Path $tokenDir 'token_cache.json'),(Join-Path $install 'data\setup.json'))
$before = @{}
foreach ($path in $protected) { $before[$path] = (Get-FileHash -LiteralPath $path).Hash }
$candidate = Join-Path $source 'bin\m365-bridge-install-candidate.exe'
# Reproduce a legacy installation that has account settings but no private key.
$legacy = Join-Path $TestRoot 'legacy missing key'
New-Item -ItemType Directory -Path (Join-Path $legacy 'data') -Force | Out-Null
$legacyEnv = Join-Path $legacy 'data\.env'
[IO.File]::WriteAllText($legacyEnv,"M365_TENANT_ID=fixture-tenant`nM365_API_KEY=`nM365_MAX_TOOL_ROUNDS=64`n")
& (Join-Path $PSScriptRoot 'install-verified-bridge.ps1') -InstallRoot $legacy -CandidatePath $candidate -SourceRevision 'legacy-key-regression' -NoStart
$legacySettings = [IO.File]::ReadAllText($legacyEnv)
if ($legacySettings -notmatch '(?m)^M365_API_KEY=[0-9a-f]{64}\r?$' -or $legacySettings -notmatch 'M365_TENANT_ID=fixture-tenant' -or $legacySettings -notmatch 'M365_MAX_TOOL_ROUNDS=64') { throw 'Legacy missing-key repair did not preserve account settings.' }
$legacyHash = (Get-FileHash -LiteralPath $legacyEnv).Hash
& (Join-Path $PSScriptRoot 'install-verified-bridge.ps1') -InstallRoot $legacy -CandidatePath $candidate -SourceRevision 'legacy-key-repeat' -NoStart
if ((Get-FileHash -LiteralPath $legacyEnv).Hash -ne $legacyHash) { throw 'Repeated setup replaced the repaired private key.' }
& (Join-Path $PSScriptRoot 'install-verified-bridge.ps1') -InstallRoot $install -CandidatePath $candidate -SourceRevision 'installation-regression' -NoStart
foreach ($path in $protected) {
    if ((Get-FileHash -LiteralPath $path).Hash -ne $before[$path]) { throw 'Upgrade changed existing account/configuration data.' }
}
$updated = [IO.File]::ReadAllText((Join-Path $install 'install-manifest.json')) | ConvertFrom-Json
if ($updated.revision -ne 'installation-regression') { throw 'Upgrade manifest was not updated.' }
if ($updated.text_port -ne 18000 -or $updated.image_port -ne 18001) { throw 'Upgrade reset the configured custom ports.' }
$unpatched = Join-Path $TestRoot 'unpatched.ps1'
[IO.File]::WriteAllText($unpatched,'"Stock bridge help"')
$rejected = $false
try {
    & (Join-Path $PSScriptRoot 'install-verified-bridge.ps1') -InstallRoot (Join-Path $TestRoot 'rejected') -CandidatePath $unpatched -NoStart
} catch { $rejected = $true }
if (-not $rejected) { throw 'Unpatched candidate was accepted.' }
foreach ($id in $runningBefore) {
    if (-not (Get-Process -Id $id -ErrorAction SilentlyContinue)) { throw 'An unrelated running bridge was stopped.' }
}
$installedProcesses = @(Get-Process -Name 'm365-bridge','m365-bridge-images' -ErrorAction SilentlyContinue | Where-Object { $_.Path -like "$install\*" })
if ($installedProcesses.Count -ne 0) { throw 'NoStart installation unexpectedly launched a service.' }
$report = [pscustomobject]@{ fresh_install = $true; aliases_match = $true; upgrade_preserves_data = $true; unpatched_candidate_rejected = $true; path_with_spaces = $true; services_not_started = $true }
$report | ConvertTo-Json
[IO.File]::WriteAllText((Join-Path $TestRoot 'report.json'),($report | ConvertTo-Json),[Text.UTF8Encoding]::new($false))
