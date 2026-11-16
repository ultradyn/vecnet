param(
  [string] $VecnetBin = $env:VECNET_BIN,
  [switch] $TypecheckOnly
)
$ErrorActionPreference = 'Stop'
$launcher = $PSScriptRoot
$repo = [IO.Path]::GetFullPath((Join-Path $launcher '../..'))
if ($VecnetBin) {
  $resolved = (Resolve-Path -LiteralPath $VecnetBin).Path
  if (-not [IO.File]::Exists($resolved)) { throw 'VecnetBin must name the built native CLI.' }
  $env:VECNET_BIN = $resolved
}
Push-Location $repo
try {
  # Setup once: npm install --prefix connectors/launcher --ignore-scripts --no-package-lock
  & node (Join-Path $launcher 'node_modules/typescript/lib/tsc.js') -p (Join-Path $launcher 'tsconfig.json')
  if ($LASTEXITCODE -ne 0) { throw 'Launcher/hub-client typecheck failed.' }
  if (-not $TypecheckOnly) {
    if (-not $env:VECNET_BIN) { throw 'Pass -VecnetBin PATH to the freshly built vecnet.exe for native security/locking tests.' }
    $files = @(Get-ChildItem -Path (Join-Path $repo 'connectors/hub-client/test/*.test.ts'), (Join-Path $launcher 'test/*.test.ts'), (Join-Path $launcher 'test/backends/*.test.ts') | ForEach-Object FullName)
    & node --disable-warning=ExperimentalWarning --test --test-concurrency=2 --test-timeout=30000 @files
    if ($LASTEXITCODE -ne 0) { throw 'Launcher/hub-client tests failed.' }
  }
} finally { Pop-Location }
