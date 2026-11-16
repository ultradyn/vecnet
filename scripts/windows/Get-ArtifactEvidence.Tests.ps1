#Requires -Version 5.1
[CmdletBinding()]
param()
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$helper = Join-Path $PSScriptRoot 'Get-ArtifactEvidence.ps1'
$root = [IO.Path]::GetFullPath((Join-Path ([IO.Path]::GetTempPath()) ('vecnet-evidence-test-' + [Guid]::NewGuid().ToString('N'))))
[IO.Directory]::CreateDirectory($root) | Out-Null
$checks = 0
function Check([bool] $Condition, [string] $Message) {
    if (-not $Condition) { throw $Message }
    $script:checks++
}
try {
    # Reading this OS image does not launch it. Hash/PE assertions cover an
    # ordinary installed executable, not just the test's malformed file.
    $image = (Get-Process -Id $PID).Path
    $hash = (Get-FileHash -LiteralPath $image -Algorithm SHA256).Hash.ToLowerInvariant()
    $report = & $helper -LiteralPath $image -ExpectedSha256 $hash | ConvertFrom-Json
    Check ($report.file.matchesExpected -eq $true) 'Expected hash did not match.'
    Check ($report.file.pe.architecture -in @('x64', 'arm64', 'x86')) 'PE architecture missing.'
    Check ($report.targetExecuted -eq $false -and $report.securitySettingsChanged -eq $false) 'Evidence flags changed.'
    Check ((Get-FileHash -LiteralPath $image -Algorithm SHA256).Hash -ieq $hash) 'Artifact changed.'
    $wrong = & $helper -LiteralPath $image -ExpectedSha256 ('0' * 64) | ConvertFrom-Json
    Check ($wrong.file.matchesExpected -eq $false) 'Hash mismatch was not reported.'

    $plain = Join-Path $root 'not-an-executable.txt'
    [IO.File]::WriteAllText($plain, 'synthetic test bytes, not a program')
    $nonPE = & $helper -LiteralPath $plain | ConvertFrom-Json
    Check ($null -ne $nonPE.file.pe.error) 'Non-PE input not reported.'
    $missing = & $helper -LiteralPath (Join-Path $root 'absent.exe') | ConvertFrom-Json
    Check ($null -ne $missing.file.error) 'Missing input not reported.'
    $directory = & $helper -LiteralPath $root | ConvertFrom-Json
    Check ($null -ne $directory.file.error) 'Directory input accepted.'

    # Mock only read-only history queries. No Defender command mutates policy.
    function Get-MpThreatDetection { @() }
    $empty = & $helper -LiteralPath $plain -IncludeDetectionHistory | ConvertFrom-Json
    Check ($empty.detectionHistory.matchingPathOnly -eq $true) 'Exact-path history scope missing.'
    Check (@($empty.detectionHistory.detections).Count -eq 0) 'Empty history not preserved.'
    function Get-MpThreatDetection {
        [PSCustomObject]@{ ThreatID = 1; Resources = @('file:_' + $plain); InitialDetectionTime = [DateTime]'2026-01-01T00:00:00Z'; LastThreatStatusChangeTime = [DateTime]'2026-01-01T00:01:00Z'; ActionSuccess = $true; DetectionSourceTypeID = 3 }
        [PSCustomObject]@{ ThreatID = 2; Resources = @('file:_' + $plain + '.different'); InitialDetectionTime = [DateTime]'2026-01-01T00:00:00Z'; LastThreatStatusChangeTime = [DateTime]'2026-01-01T00:01:00Z'; ActionSuccess = $true; DetectionSourceTypeID = 3 }
    }
    function Get-MpThreat {
        [PSCustomObject]@{ ThreatID = 1; ThreatName = 'Synthetic exact-path fixture' }
        [PSCustomObject]@{ ThreatID = 2; ThreatName = 'Must not match' }
    }
    $exact = & $helper -LiteralPath $plain -IncludeDetectionHistory | ConvertFrom-Json
    Check (@($exact.detectionHistory.detections).Count -eq 1) 'History used a prefix or omitted exact entry.'
    Check ($exact.detectionHistory.detections[0].name -ceq 'Synthetic exact-path fixture') 'History threat lookup failed.'
    Write-Output ('PASS: ' + $checks + ' artifact-evidence checks; no executable run or security setting changed.')
} finally {
    # Remove only this randomized test directory, using one shell end to end.
    $resolved = [IO.Path]::GetFullPath($root)
    $tempRoot = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\') + '\'
    if (-not $resolved.StartsWith($tempRoot, [StringComparison]::OrdinalIgnoreCase) -or [IO.Path]::GetFileName($resolved) -notlike 'vecnet-evidence-test-*') { throw 'Refusing out-of-scope test cleanup.' }
    Remove-Item -LiteralPath $resolved -Recurse -Force
}