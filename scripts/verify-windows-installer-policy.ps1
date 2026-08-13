$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
$installerPath = Join-Path $repoRoot "installer\windows\DDGo.iss"
$iss = Get-Content $installerPath -Raw

$setupMatch = [regex]::Match(
    $iss,
    '(?ms)^\s*\[Setup\]\s*$\r?\n(?<body>.*?)(?=^\s*\[|\z)'
)

if (-not $setupMatch.Success) {
    Write-Error "Windows installer is missing its [Setup] section."
    exit 1
}

$setup = $setupMatch.Groups['body'].Value
$minimumVersions = [regex]::Matches($setup, '(?m)^\s*MinVersion\s*=.*$')

if ($minimumVersions.Count -ne 1 -or
    $minimumVersions[0].Value -notmatch '^\s*MinVersion\s*=\s*10\.0\s*$') {
    Write-Error "Windows installer must contain exactly MinVersion=10.0 in [Setup]."
    exit 1
}

if ($setup -notmatch '(?m)^\s*ArchitecturesAllowed\s*=\s*x64compatible\s*$') {
    Write-Error "Windows installer must remain x64-compatible."
    exit 1
}

if ($setup -notmatch '(?m)^\s*ArchitecturesInstallIn64BitMode\s*=\s*x64compatible\s*$') {
    Write-Error "Windows installer must install in 64-bit mode."
    exit 1
}

Write-Host "Windows installer policy verified: Windows 10 or later, x64-compatible."
