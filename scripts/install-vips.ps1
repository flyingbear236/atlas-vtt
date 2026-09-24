$ErrorActionPreference = 'Stop'
$project = Split-Path $PSScriptRoot -Parent
$deps = Join-Path $project '.deps'
$version = '8.18.6'
$package = 'vips-dev-x64-web-8.18.6.zip'
$expected = '10086f2ccc8e2a861831facabab75fa1b80b4bd03f0a8447623ec581002aa4b2'
$archive = Join-Path $deps $package
$destination = Join-Path $deps "libvips-$version"
$binary = Join-Path $destination 'vips-dev-8.18/bin/vips.exe'
if (![Environment]::Is64BitOperatingSystem -or $env:PROCESSOR_ARCHITECTURE -eq 'ARM64') {
    throw 'This pinned package is for Windows x64. Install the official package for your platform and set ATLAS_VIPS.'
}
New-Item -ItemType Directory -Force $deps | Out-Null
if (!(Test-Path -LiteralPath $archive)) {
    Invoke-WebRequest -UseBasicParsing "https://github.com/libvips/build-win64-mxe/releases/download/v$version/$package" -OutFile $archive
}
if ((Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash -ne $expected) {
    throw 'SHA-256 mismatch. The archive was not extracted.'
}
if (!(Test-Path -LiteralPath $binary)) {
    Expand-Archive -LiteralPath $archive -DestinationPath $destination -Force
}
& $binary --version
if ($LASTEXITCODE) { throw 'libvips did not start' }
Write-Output "Atlas will use $binary"
