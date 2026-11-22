param(
    [ValidateNotNullOrEmpty()]
    [string]$OutputPath = (Join-Path (Split-Path -Parent $PSScriptRoot) 'atlas-source.zip')
)

$ErrorActionPreference = 'Stop'
$repository = Split-Path -Parent $PSScriptRoot
$destination = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($OutputPath)
if (Test-Path -LiteralPath $destination) {
    throw "Output already exists: $destination"
}

# Read tracked paths from Git and copy their current contents from disk.
$previousEncoding = [Console]::OutputEncoding
try {
    [Console]::OutputEncoding = [System.Text.Encoding]::UTF8
    $listedPaths = git -C $repository ls-files --cached --deduplicate -z
    if ($LASTEXITCODE -ne 0) {
        throw 'Could not list tracked files.'
    }
}
finally {
    [Console]::OutputEncoding = $previousEncoding
}
$paths = ($listedPaths -join "`n").Split([char]0, [StringSplitOptions]::RemoveEmptyEntries)

$dependencyDirectories = '(^|/)(node_modules|vendor|\.deps)(/|$)'
$buildArtifacts = '\.(exe|dll|msi|so(\.\d+)*|dylib|lib|a|o|obj|test|out|class|jar|pyc|wasm|zip)$'
$diceBundle = 'web/vendor-dice-box-threejs.es.js'

Add-Type -AssemblyName System.IO.Compression, System.IO.Compression.FileSystem
$temporary = Join-Path (Split-Path -Parent $destination) ".atlas-source-$([guid]::NewGuid()).zip"
try {
    $zip = [IO.Compression.ZipFile]::Open($temporary, [IO.Compression.ZipArchiveMode]::Create)
    try {
        foreach ($path in $paths) {
            if ($path -match '^data/' -or $path -match $dependencyDirectories -or
                $path -match $buildArtifacts -or $path -eq $diceBundle) {
                continue
            }

            $source = Join-Path $repository $path
            if (!(Test-Path -LiteralPath $source -PathType Leaf)) {
                continue
            }

            # A tracked path may have been replaced locally by a link or junction.
            $item = Get-Item -LiteralPath $source -Force
            while ($item.FullName -ne $repository) {
                if ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) {
                    throw "Cannot archive a path through a link or junction: $path"
                }
                if ($item -is [IO.FileInfo]) { $item = $item.Directory }
                else { $item = $item.Parent }
            }

            [IO.Compression.ZipFileExtensions]::CreateEntryFromFile(
                $zip, $source, $path, [IO.Compression.CompressionLevel]::Optimal
            ) | Out-Null
        }
    }
    finally {
        $zip.Dispose()
    }

    [IO.File]::Move($temporary, $destination)
    Write-Output $destination
}
finally {
    if (Test-Path -LiteralPath $temporary) {
        Remove-Item -LiteralPath $temporary
    }
}
