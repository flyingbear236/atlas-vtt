$ErrorActionPreference = 'Stop'

$version = '0.0.12'
$archiveSHA256 = '60A5A96A61CE5855D85C388378D79E1FE8DEC55EAF7009152DFE4C0758E09502'
$archiveURL = "https://registry.npmjs.org/@3d-dice/dice-box-threejs/-/dice-box-threejs-$version.tgz"
$repository = Split-Path -Parent $PSScriptRoot
$work = Join-Path ([System.IO.Path]::GetTempPath()) "atlas-dice-box-threejs-$version-$([guid]::NewGuid())"
$archive = Join-Path $work 'package.tgz'

try {
    New-Item -ItemType Directory -Path $work | Out-Null
    Invoke-WebRequest -Uri $archiveURL -OutFile $archive
    $actualHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $archive).Hash
    if ($actualHash -ne $archiveSHA256) {
        throw "Unexpected @3d-dice/dice-box-threejs archive SHA-256: $actualHash"
    }
    tar -xf $archive -C $work
    $manifest = Get-Content -Raw -Encoding UTF8 (Join-Path $work 'package/package.json') | ConvertFrom-Json
    if ($manifest.version -ne $version -or $manifest.dependencies.three -ne '^0.143.0' -or $manifest.dependencies.'cannon-es' -ne '^0.20.0') {
        throw 'Published dependency metadata differs from the pinned vendor manifest'
    }
    Copy-Item -Force -LiteralPath (Join-Path $work 'package/dist/dice-box-threejs.es.js') -Destination (Join-Path $repository 'web/vendor-dice-box-threejs.es.js')
    Copy-Item -Force -LiteralPath (Join-Path $work 'package/LICENSE') -Destination (Join-Path $repository 'web/vendor-dice-box-threejs.LICENSE.txt')
    $assetRoot = Join-Path $repository 'web/dice-assets/sounds'
    New-Item -ItemType Directory -Force -Path (Join-Path $assetRoot 'dicehit'),(Join-Path $assetRoot 'surfaces') | Out-Null
    Copy-Item -Force (Join-Path $work 'package/public/sounds/dicehit/dicehit_plastic*.mp3') (Join-Path $assetRoot 'dicehit')
    Copy-Item -Force (Join-Path $work 'package/public/sounds/surfaces/surface_felt*.mp3') (Join-Path $assetRoot 'surfaces')
}
finally {
    if (Test-Path -LiteralPath $work) {
        Remove-Item -Recurse -Force -LiteralPath $work
    }
}
