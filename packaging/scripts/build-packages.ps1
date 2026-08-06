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

function Find-WixBinary {
    param([string]$Name)

    $fromPath = Get-Command $Name -ErrorAction SilentlyContinue
    if ($fromPath) {
        return $fromPath.Source
    }

    $defaultPath = "${env:ProgramFiles(x86)}\WiX Toolset v3.11\bin\$Name"
    if (Test-Path $defaultPath) {
        return $defaultPath
    }

    throw "$Name not found. Install WiX Toolset v3 or ensure binaries are in PATH."
}

$candle = Find-WixBinary -Name 'candle.exe'
$light = Find-WixBinary -Name 'light.exe'

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
$objPath = Join-Path $rootDir "bin\packages\ollama-gateway.wixobj"
$target = Join-Path $rootDir ("bin\packages\ollama-gateway_{0}_windows_{1}.msi" -f $env:VERSION, $goArch)

& $candle -nologo -arch $wixPlatform -dProductVersion=$msiVersion -dPlatform=$wixPlatform -out $objPath $wxsPath
if ($LASTEXITCODE -ne 0) {
    throw "WiX compile failed"
}

& $light -nologo -ext WixUIExtension -ext WixUtilExtension -out $target $objPath
if ($LASTEXITCODE -ne 0) {
    throw "WiX link failed"
}

Write-Host "built $target"
