[CmdletBinding()]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$Version = '0.4.4-multiplatform-dev'
$Tag = "v$Version"
$ReleaseBase = "https://github.com/Cytech-Team/CyComAgent-MCP/releases/download/$Tag"
$Archive = "CyComAgent-MCP-v$Version-windows.zip"
$ChecksumFile = 'SHA256SUMS-windows'
$Work = Join-Path ([System.IO.Path]::GetTempPath()) ("cycomagent-bootstrap-" + [Guid]::NewGuid().ToString('N'))

function Invoke-CurlDownload {
    param([Parameter(Mandatory)][string]$Url,[Parameter(Mandatory)][string]$OutFile)
    $curl = Get-Command curl.exe -ErrorAction SilentlyContinue
    if ($null -eq $curl) { throw 'curl.exe is required (included with current Windows 10/11).' }
    & $curl.Source -fL --retry 3 --retry-delay 1 --connect-timeout 15 $Url -o $OutFile
    if ($LASTEXITCODE -ne 0) { throw "curl.exe failed downloading $Url" }
}

try {
    New-Item -ItemType Directory -Path $Work -Force | Out-Null
    $ArchivePath = Join-Path $Work $Archive
    $ChecksumPath = Join-Path $Work $ChecksumFile

    Write-Host "== CyComAgent-MCP $Version =="
    Write-Host 'Detected: Windows'
    Write-Host "Downloading $Archive..."
    Invoke-CurlDownload -Url "$ReleaseBase/$Archive" -OutFile $ArchivePath
    Invoke-CurlDownload -Url "$ReleaseBase/$ChecksumFile" -OutFile $ChecksumPath

    $line = Get-Content -LiteralPath $ChecksumPath | Where-Object { $_ -match ([regex]::Escape($Archive) + '$') } | Select-Object -First 1
    if ([string]::IsNullOrWhiteSpace($line)) { throw "Checksum for $Archive is missing." }
    $expected = ($line -split '\s+')[0].ToLowerInvariant()
    $actual = (Get-FileHash -LiteralPath $ArchivePath -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($actual -ne $expected) { throw "SHA-256 verification failed for $Archive" }
    Write-Host 'SHA-256: verified'

    $Extract = Join-Path $Work 'extract'
    Expand-Archive -LiteralPath $ArchivePath -DestinationPath $Extract -Force
    $Installer = Join-Path $Extract 'CyComAgent-MCP\platform\windows\Install-CyComAgent-Windows-Bridge.ps1'
    if (-not (Test-Path -LiteralPath $Installer -PathType Leaf)) { throw "Installer missing from release: $Installer" }

    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = New-Object Security.Principal.WindowsPrincipal($identity)
    $isAdmin = $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)

    if ($isAdmin) {
        & powershell.exe -NoLogo -NoProfile -ExecutionPolicy Bypass -File $Installer
        if ($LASTEXITCODE -ne 0) { throw "Windows installer exited with code $LASTEXITCODE" }
    } else {
        Write-Host 'Administrator permission is required; Windows will show a UAC prompt.'
        $args = '-NoLogo -NoProfile -ExecutionPolicy Bypass -File "{0}"' -f $Installer
        $process = Start-Process -FilePath 'powershell.exe' -Verb RunAs -ArgumentList $args -Wait -PassThru
        if ($process.ExitCode -ne 0) { throw "Elevated installer exited with code $($process.ExitCode)" }
    }

    Start-Sleep -Milliseconds 700
    try {
        $health = & curl.exe -fsS --max-time 3 'http://127.0.0.1:7332/health'
        if ($LASTEXITCODE -eq 0) { Write-Host 'Health: OK' }
    } catch { }
    Write-Host 'MCP: http://127.0.0.1:7332/mcp'
    Write-Host 'Installed CyComAgent Windows Bridge.'
}
finally {
    Remove-Item -LiteralPath $Work -Recurse -Force -ErrorAction SilentlyContinue
}
