$ErrorActionPreference = 'Stop'

$rootDir = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
Set-Location $rootDir

if (-not $env:VERSION -or [string]::IsNullOrWhiteSpace($env:VERSION)) {
    $describe = (git describe --tags --always --dirty)
    if ($LASTEXITCODE -ne 0) {
        throw "failed to derive VERSION from git describe"
    }
    $env:VERSION = $describe.TrimStart('v')
}

if (-not $env:ARCH -or [string]::IsNullOrWhiteSpace($env:ARCH)) {
    $env:ARCH = 'amd64'
}

$goArch = switch ($env:ARCH.ToLowerInvariant()) {
    'amd64' { 'amd64' }
    'arm64' { 'arm64' }
    default { throw "unsupported ARCH '$($env:ARCH)'. Use amd64 or arm64." }
}

$wixPlatform = switch ($goArch) {
    'amd64' { 'x64' }
    'arm64' { 'arm64' }
    default { throw "unsupported Go arch '$goArch' for WiX" }
}

$wixVersion = '7.0.0'

function Convert-ToMsiVersion {
    param([string]$Version)

    $parts = [regex]::Matches($Version, '\d+') | ForEach-Object { $_.Value }
    if ($parts.Count -eq 0) {
        return '0.0.0'
    }

    $major = [int]$parts[0]
    $minor = if ($parts.Count -gt 1) { [int]$parts[1] } else { 0 }
    $patch = if ($parts.Count -gt 2) { [int]$parts[2] } else { 0 }

    if ($major -gt 255) { $major = 255 }
    if ($minor -gt 65535) { $minor = 65535 }
    if ($patch -gt 65535) { $patch = 65535 }

    return "$major.$minor.$patch"
}

function Find-WixCommand {
    $fromPath = Get-Command wix -ErrorAction SilentlyContinue
    if ($fromPath) {
        return $fromPath.Source
    }

    throw "wix command not found. Install WiX Toolset .NET tool v7 and ensure it is in PATH."
}

$wix = Find-WixCommand

& $wix eula accept wix7
if ($LASTEXITCODE -ne 0) {
    throw "failed to accept WiX v7 EULA (wix7)"
}

& $wix extension add -g "WixToolset.UI.wixext/$wixVersion"
if ($LASTEXITCODE -ne 0) {
    throw "failed to add WixToolset.UI.wixext/$wixVersion"
}

& $wix extension add -g "WixToolset.Util.wixext/$wixVersion"
if ($LASTEXITCODE -ne 0) {
    throw "failed to add WixToolset.Util.wixext/$wixVersion"
}

New-Item -ItemType Directory -Path "bin\packages" -Force | Out-Null

$env:GOOS = 'windows'
$env:GOARCH = $goArch

& go build -o "bin\ollama-gateway.exe" "./cmd/gateway"
if ($LASTEXITCODE -ne 0) {
    throw "failed to build gateway binary"
}

& go build -o "bin\installer-bootstrap.exe" "./cmd/installer-bootstrap"
if ($LASTEXITCODE -ne 0) {
    throw "failed to build installer bootstrap helper"
}

$msiVersion = Convert-ToMsiVersion -Version $env:VERSION
$wxsPath = Join-Path $rootDir "packaging\windows\ollama-gateway.wxs"
$target = Join-Path $rootDir ("bin\packages\ollama-gateway_{0}_windows_{1}.msi" -f $env:VERSION, $goArch)

& $wix build -nologo -arch $wixPlatform -d ProductVersion=$msiVersion -d Platform=$wixPlatform -ext WixToolset.UI.wixext -ext WixToolset.Util.wixext -o $target $wxsPath
if ($LASTEXITCODE -ne 0) {
    throw "WiX build failed"
}

Write-Host "built $target"
