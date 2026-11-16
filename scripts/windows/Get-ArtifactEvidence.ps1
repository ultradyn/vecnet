#Requires -Version 5.1
<#
.SYNOPSIS
Collect static evidence for one Windows artifact without executing it.
.DESCRIPTION
Reads only the named file, its signature/version metadata, and optionally local
Defender history matching that exact path. Does not build, run, scan, restore,
allow, exclude, or change any security settings. Output is a JSON object, not a
safety verdict. Keep private machine paths out of publicly shared reports.
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string] $LiteralPath,

    [ValidatePattern('^[0-9a-fA-F]{64}$')]
    [string] $ExpectedSha256,

    [switch] $IncludeDetectionHistory
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Get-ErrorEvidence([System.Exception] $Exception) {
    while ($Exception.InnerException) { $Exception = $Exception.InnerException }
    return [ordered]@{
        type = $Exception.GetType().FullName
        hresult = $Exception.HResult
        win32LowWord = ($Exception.HResult -band 0xffff)
        message = $Exception.Message
    }
}

$absolutePath = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($LiteralPath)
$report = [ordered]@{
    collectedAt = [DateTimeOffset]::Now.ToString('o')
    path = $absolutePath
    expectedSha256 = $ExpectedSha256
    file = $null
    authenticode = $null
    version = $null
    detectionHistory = $null
    targetExecuted = $false
    securitySettingsChanged = $false
    verdict = 'Not determined. Static evidence and detection history do not establish safety.'
}

try {
    $item = Get-Item -LiteralPath $absolutePath -Force
    if ($item.PSIsContainer) { throw 'Artifact must be a file, not a directory.' }
    if ($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) {
        throw 'Refusing a reparse-point artifact; name the original regular file.'
    }
    # Hash and parse the same opened file, holding it against write/delete while
    # read. Other metadata below is a separate observation, not an attestation.
    $stream = [System.IO.File]::Open($absolutePath, [System.IO.FileMode]::Open,
        [System.IO.FileAccess]::Read, [System.IO.FileShare]::Read)
    try {
        $hash = [System.Security.Cryptography.SHA256]::Create()
        try { $digest = ([BitConverter]::ToString($hash.ComputeHash($stream))).Replace('-', '').ToLowerInvariant() }
        finally { $hash.Dispose() }
        $file = [ordered]@{ bytes = $stream.Length; sha256 = $digest; matchesExpected = $null; pe = $null }
        if ($ExpectedSha256) { $file.matchesExpected = ($digest -ceq $ExpectedSha256.ToLowerInvariant()) }
        try {
            $reader = New-Object System.IO.BinaryReader($stream)
            $stream.Position = 0
            if ($reader.ReadUInt16() -ne 0x5a4d) { throw 'Not an MZ/PE image.' }
            $stream.Position = 0x3c
            $peOffset = $reader.ReadUInt32()
            if ($peOffset -lt 0x40 -or $peOffset -gt ($stream.Length - 96)) { throw 'Invalid PE header offset.' }
            $stream.Position = $peOffset
            if ($reader.ReadUInt32() -ne 0x00004550) { throw 'Invalid PE signature.' }
            $machine = $reader.ReadUInt16()
            $stream.Position = $peOffset + 24
            $magic = $reader.ReadUInt16()
            $stream.Position = $peOffset + 24 + 68
            $subsystem = $reader.ReadUInt16()
            $architecture = switch ($machine) { 0x8664 { 'x64' } 0xaa64 { 'arm64' } 0x14c { 'x86' } default { 'other' } }
            $file.pe = [ordered]@{
                machine = ('0x{0:x4}' -f $machine)
                architecture = $architecture
                optionalHeaderMagic = ('0x{0:x3}' -f $magic)
                subsystem = $subsystem
            }
        } catch { $file.pe = [ordered]@{ error = (Get-ErrorEvidence $_.Exception) } }
        $report.file = $file
    } finally { $stream.Dispose() }
} catch { $report.file = [ordered]@{ error = (Get-ErrorEvidence $_.Exception) } }

try {
    $signature = Get-AuthenticodeSignature -LiteralPath $absolutePath
    $report.authenticode = [ordered]@{
        status = [string] $signature.Status
        signerSubject = $(if ($signature.SignerCertificate) { $signature.SignerCertificate.Subject } else { $null })
        signerThumbprint = $(if ($signature.SignerCertificate) { $signature.SignerCertificate.Thumbprint } else { $null })
        timestampSubject = $(if ($signature.TimeStamperCertificate) { $signature.TimeStamperCertificate.Subject } else { $null })
    }
} catch { $report.authenticode = [ordered]@{ error = (Get-ErrorEvidence $_.Exception) } }

try {
    $version = [System.Diagnostics.FileVersionInfo]::GetVersionInfo($absolutePath)
    $report.version = [ordered]@{
        companyName = $version.CompanyName
        fileDescription = $version.FileDescription
        fileVersion = $version.FileVersion
        productName = $version.ProductName
        productVersion = $version.ProductVersion
        originalFilename = $version.OriginalFilename
    }
} catch { $report.version = [ordered]@{ error = (Get-ErrorEvidence $_.Exception) } }

if ($IncludeDetectionHistory) {
    try {
        $matches = @(Get-MpThreatDetection | Where-Object {
            $paths = @($_.Resources | ForEach-Object { $_ -replace '^file:_?', '' })
            @($paths | Where-Object { [string]::Equals($_, $absolutePath, [StringComparison]::OrdinalIgnoreCase) }).Count -gt 0
        })
        $threats = @()
        if ($matches.Count -gt 0) { $threats = @(Get-MpThreat) }
        $detections = @($matches | ForEach-Object {
            $detection = $_
            $threat = @($threats | Where-Object { $_.ThreatID -eq $detection.ThreatID }) | Select-Object -First 1
            [ordered]@{
                threatId = $detection.ThreatID
                name = $(if ($threat) { $threat.ThreatName } else { $null })
                initialDetectionTime = ([DateTimeOffset] $detection.InitialDetectionTime).ToString('o')
                lastStatusChangeTime = ([DateTimeOffset] $detection.LastThreatStatusChangeTime).ToString('o')
                actionSuccess = $detection.ActionSuccess
                detectionSourceTypeId = $detection.DetectionSourceTypeID
            }
        })
        $report.detectionHistory = [ordered]@{
            matchingPathOnly = $true
            detections = $detections
            note = 'No matching history is not a clean scan. The path can have held different file bytes.'
        }
    } catch { $report.detectionHistory = [ordered]@{ error = (Get-ErrorEvidence $_.Exception) } }
}

$report | ConvertTo-Json -Depth 8
