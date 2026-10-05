param(
    [Parameter(Mandatory = $true)]
    [ValidateSet('Store', 'Direct')]
    [string]$Channel,

    [ValidateSet('x64', 'arm64')]
    [string]$Architecture = 'x64',

    [string]$IdentityName,
    [string]$Publisher,
    [string]$PublisherDisplayName = 'DiskAtlas',
    [string]$PackageVersion,
    [string]$SignThumbprint,
    [string]$SignToolPath,
    [string]$TimestampUrl = 'http://timestamp.digicert.com',
    [switch]$Unsigned
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$root = (Resolve-Path (Join-Path $PSScriptRoot '../..')).Path
Set-Location $root

function Invoke-Checked {
    param([string]$File, [string[]]$Arguments)
    & $File @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "$File failed with exit code $LASTEXITCODE"
    }
}

function Find-WindowsSdkTool {
    param([string]$Name)
    $found = Get-Command $Name -ErrorAction SilentlyContinue
    if ($found) { return $found.Source }

    $sdkRoot = Join-Path ${env:ProgramFiles(x86)} 'Windows Kits/10/bin'
    if (Test-Path $sdkRoot) {
        $candidate = Get-ChildItem $sdkRoot -Directory |
            Sort-Object Name -Descending |
            ForEach-Object { Join-Path $_.FullName "x64/$Name.exe" } |
            Where-Object { Test-Path $_ } |
            Select-Object -First 1
        if ($candidate) { return $candidate }
    }
    throw "$Name was not found. Install the Windows 10/11 SDK and add its tool directory to PATH."
}

function Sign-File {
    param([string]$Path, [string]$Tool)
    Invoke-Checked $Tool @('sign', '/fd', 'SHA256', '/sha1', $SignThumbprint, '/tr', $TimestampUrl, '/td', 'SHA256', $Path)
    Invoke-Checked $Tool @('verify', '/pa', '/v', $Path)
}

if (-not $env:OS -or $env:OS -ne 'Windows_NT') {
    throw 'Windows release builds must run on Windows.'
}
$wails = (Get-Command wails -ErrorAction Stop).Source
$versionInfo = Get-Content (Join-Path $root 'wails.json') -Raw | ConvertFrom-Json
$version = [string]$versionInfo.info.productVersion
$wailsArch = if ($Architecture -eq 'x64') { 'amd64' } else { 'arm64' }
$appxArch = $Architecture
$exe = Join-Path $root 'build/bin/diskatlas.exe'
$releaseDir = Join-Path $root "dist/release/windows/$($Channel.ToLowerInvariant())"
New-Item -ItemType Directory -Force -Path $releaseDir | Out-Null

if ($Channel -eq 'Direct') {
    if (-not $Unsigned -and [string]::IsNullOrWhiteSpace($SignThumbprint)) {
        throw 'For a public direct release, pass -SignThumbprint. Use -Unsigned only for an internal test build.'
    }
    if ($Unsigned -and -not [string]::IsNullOrWhiteSpace($SignThumbprint)) {
        throw 'Choose either -Unsigned or -SignThumbprint.'
    }

    Invoke-Checked $wails @('build', '-platform', "windows/$wailsArch", '-clean', '-nsis', '-installscope', 'user')
    if (-not (Test-Path $exe)) { throw "Wails did not produce $exe" }

    $makensis = (Get-Command makensis -ErrorAction Stop).Source
    $installerName = "DiskAtlas-$wailsArch-installer.exe"
    $installer = Join-Path $root "build/bin/$installerName"
    if (-not $Unsigned) {
        if ([string]::IsNullOrWhiteSpace($SignToolPath)) { $SignToolPath = Find-WindowsSdkTool 'signtool' }
        if (-not (Test-Path $SignToolPath)) { throw "SignTool not found: $SignToolPath" }
        Sign-File $exe $SignToolPath
    }

    Push-Location (Join-Path $root 'build/windows/installer')
    try {
        $binaryDefine = if ($Architecture -eq 'x64') { '-DARG_WAILS_AMD64_BINARY=..\..\bin\diskatlas.exe' } else { '-DARG_WAILS_ARM64_BINARY=..\..\bin\diskatlas.exe' }
        Invoke-Checked $makensis @($binaryDefine, '-DWAILS_INSTALL_SCOPE=user', '-DREQUEST_EXECUTION_LEVEL=user', 'project.nsi')
    } finally {
        Pop-Location
    }
    if (-not (Test-Path $installer)) { throw "NSIS did not produce $installer" }
    if (-not $Unsigned) { Sign-File $installer $SignToolPath }

    $output = Join-Path $releaseDir $installerName
    Copy-Item $installer $output -Force
    Write-Host "Windows direct-release installer created: $output"
    exit 0
}

if ([string]::IsNullOrWhiteSpace($IdentityName) -or [string]::IsNullOrWhiteSpace($Publisher)) {
    throw 'Store packaging requires -IdentityName and -Publisher copied from the app identity in Partner Center.'
}
if ($Unsigned -or $SignThumbprint) {
    throw 'Store MSIX packages are signed by Microsoft after Partner Center submission; do not pass signing options.'
}
if ([string]::IsNullOrWhiteSpace($PackageVersion)) {
    $PackageVersion = if ($version.Split('.').Count -eq 3) { "$version.0" } else { $version }
}
if ($PackageVersion -notmatch '^\d+\.\d+\.\d+\.\d+$' -or @($PackageVersion.Split('.') | Where-Object { [int]$_ -gt 65535 }).Count -gt 0) {
    throw 'PackageVersion must contain four numeric components, each no greater than 65535 (for example 0.1.0.0).'
}

Invoke-Checked $wails @('build', '-platform', "windows/$wailsArch", '-clean', '-nopackage')
if (-not (Test-Path $exe)) { throw "Wails did not produce $exe" }
$makeAppx = Find-WindowsSdkTool 'MakeAppx'
$stage = Join-Path ([IO.Path]::GetTempPath()) "DiskAtlas-msix-$PID"
if (Test-Path $stage) { Remove-Item $stage -Recurse -Force }
$assets = Join-Path $stage 'Assets'
New-Item -ItemType Directory -Force -Path $assets | Out-Null
Copy-Item $exe (Join-Path $stage 'diskatlas.exe')

Add-Type -AssemblyName System.Drawing
$source = [System.Drawing.Image]::FromFile((Join-Path $root 'build/appicon.png'))
try {
    foreach ($asset in @(@{ Name = 'Square150x150Logo.png'; Size = 150 }, @{ Name = 'Square44x44Logo.png'; Size = 44 }, @{ Name = 'StoreLogo.png'; Size = 50 })) {
        $bitmap = [System.Drawing.Bitmap]::new([int]$asset.Size, [int]$asset.Size)
        try {
            $graphics = [System.Drawing.Graphics]::FromImage($bitmap)
            try {
                $graphics.InterpolationMode = [System.Drawing.Drawing2D.InterpolationMode]::HighQualityBicubic
                $graphics.SmoothingMode = [System.Drawing.Drawing2D.SmoothingMode]::HighQuality
                $graphics.DrawImage($source, 0, 0, $asset.Size, $asset.Size)
            } finally { $graphics.Dispose() }
            $bitmap.Save((Join-Path $assets $asset.Name), [System.Drawing.Imaging.ImageFormat]::Png)
        } finally { $bitmap.Dispose() }
    }
} finally { $source.Dispose() }

$xmlIdentity = [Security.SecurityElement]::Escape($IdentityName)
$xmlPublisher = [Security.SecurityElement]::Escape($Publisher)
$xmlPublisherDisplayName = [Security.SecurityElement]::Escape($PublisherDisplayName)
$manifest = @"
<?xml version="1.0" encoding="utf-8"?>
<Package xmlns="http://schemas.microsoft.com/appx/manifest/foundation/windows10" xmlns:uap="http://schemas.microsoft.com/appx/manifest/uap/windows10" xmlns:rescap="http://schemas.microsoft.com/appx/manifest/foundation/windows10/restrictedcapabilities" IgnorableNamespaces="uap rescap">
  <Identity Name="$xmlIdentity" Publisher="$xmlPublisher" Version="$PackageVersion" ProcessorArchitecture="$appxArch" />
  <Properties>
    <DisplayName>DiskAtlas</DisplayName>
    <PublisherDisplayName>$xmlPublisherDisplayName</PublisherDisplayName>
    <Logo>Assets\StoreLogo.png</Logo>
  </Properties>
  <Dependencies>
    <TargetDeviceFamily Name="Windows.Desktop" MinVersion="10.0.17763.0" MaxVersionTested="10.0.26100.0" />
  </Dependencies>
  <Resources><Resource Language="en-US" /></Resources>
  <Applications>
    <Application Id="DiskAtlas" Executable="diskatlas.exe" EntryPoint="Windows.FullTrustApplication">
      <uap:VisualElements DisplayName="DiskAtlas" Description="Explore disk usage" Square150x150Logo="Assets\Square150x150Logo.png" Square44x44Logo="Assets\Square44x44Logo.png" BackgroundColor="transparent" />
    </Application>
  </Applications>
  <Capabilities><rescap:Capability Name="runFullTrust" /></Capabilities>
</Package>
"@
Set-Content -Path (Join-Path $stage 'AppxManifest.xml') -Value $manifest -Encoding UTF8

$baseName = "DiskAtlas-$PackageVersion-$appxArch"
$msix = Join-Path $releaseDir "$baseName.msix"
Invoke-Checked $makeAppx @('pack', '/d', $stage, '/p', $msix, '/o')
$uploadZip = Join-Path $releaseDir "$baseName-upload.zip"
$msixUpload = Join-Path $releaseDir "$baseName.msixupload"
if (Test-Path $uploadZip) { Remove-Item $uploadZip -Force }
if (Test-Path $msixUpload) { Remove-Item $msixUpload -Force }
Compress-Archive -LiteralPath $msix -DestinationPath $uploadZip
Move-Item $uploadZip $msixUpload
Remove-Item $stage -Recurse -Force
Write-Host 'Windows Store packages created:'
Write-Host "  $msix"
Write-Host "  $msixUpload"
Write-Host 'The Store applies its signature after submission. The runFullTrust capability needs Store approval.'
