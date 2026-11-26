@echo off
setlocal
cd /d "%~dp0"
set "PACK_SELF=%~f0"
powershell.exe -NoProfile -ExecutionPolicy Bypass -Command "$ErrorActionPreference='Stop'; $s=[IO.File]::ReadAllText($env:PACK_SELF); $m=[regex]::Match($s,'(?m)^# POWERSHELL START\r?$'); if (!$m.Success) { throw 'Missing script marker' }; & ([scriptblock]::Create($s.Substring($m.Index+$m.Length)))"
set "RESULT=%ERRORLEVEL%"
echo.
pause
exit /b %RESULT%
# POWERSHELL START
$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false)
$root = & git rev-parse --show-toplevel
if ($LASTEXITCODE -ne 0) { throw 'Git repository not found.' }
Set-Location -LiteralPath $root
$archive = Join-Path $root 'project-for-chatgpt.zip'
$temp = Join-Path $root ('project-for-chatgpt-' + [guid]::NewGuid().ToString('N') + '.tmp')
$excludedFolders = 'node_modules','vendor','dist','build','target','coverage','__pycache__','.next','.nuxt','.cache','tmp','temp','logs','data','assets','media','uploads'
$excludedExtensions = '.exe','.dll','.so','.dylib','.a','.lib','.msi','.msix','.apk','.aab','.deb','.rpm','.bin','.dat','.db','.sqlite','.sqlite3','.zip','.7z','.rar','.tar','.gz','.pdf','.docx','.xlsx','.pptx','.png','.jpg','.jpeg','.gif','.webp','.bmp','.ico','.avif','.mp3','.wav','.ogg','.flac','.mp4','.webm','.ttf','.otf','.woff','.woff2','.pyc','.class','.o','.obj','.log','.tmp','.cache','.map','.pem','.key','.pfx','.p12','.code-workspace'
# The browser test log and the vendored dice bundle have ordinary text extensions.
$excludedPaths = @(
    'browser-tests.txt'
    'web/vendor-dice-box-threejs.es.js'
)
# Read Git's NUL-separated UTF-8 output directly, without PowerShell line splitting.
$psi = New-Object System.Diagnostics.ProcessStartInfo
$psi.FileName = 'git'
$psi.Arguments = 'ls-files --cached --others --exclude-standard -z'
$psi.WorkingDirectory = $root
$psi.UseShellExecute = $false
$psi.RedirectStandardOutput = $true
$psi.StandardOutputEncoding = [Text.Encoding]::UTF8
$p = [Diagnostics.Process]::Start($psi)
try { $raw = $p.StandardOutput.ReadToEnd(); $p.WaitForExit(); if ($p.ExitCode -ne 0) { throw 'git ls-files failed.' } } finally { $p.Dispose() }
$paths = $raw.Split([char]0) | Where-Object { $_ } | Sort-Object -Unique
Add-Type -AssemblyName System.IO.Compression
Add-Type -AssemblyName System.IO.Compression.FileSystem
$count = 0
$skipped = 0
try {
    $zip = [IO.Compression.ZipFile]::Open($temp,[IO.Compression.ZipArchiveMode]::Create)
    try {
        foreach ($path in $paths) {
            $parts = $path.Split('/')
            $name = $parts[-1]
            $full = Join-Path $root $path
            $inExcludedFolder = $false
            for ($i = 0; $i -lt $parts.Length - 1; $i++) {
                if ($excludedFolders -contains $parts[$i]) {
                    $inExcludedFolder = $true
                    break
                }
            }
            $isLocalEnvironment = $name -eq '.env' -or (
                $name -like '.env.*' -and $name -notin @('.env.example','.env.sample','.env.template')
            )
            if ($inExcludedFolder -or
                $path -in $excludedPaths -or
                [IO.Path]::GetExtension($name) -in $excludedExtensions -or
                $name -like '*.min.js' -or $name -like '*.min.css' -or
                $isLocalEnvironment -or $full -eq $env:PACK_SELF) {
                $skipped++
                continue
            }
            if (!(Test-Path -LiteralPath $full -PathType Leaf)) { $skipped++; continue }
            $item = Get-Item -LiteralPath $full -Force
            if ($item.Length -gt 5MB -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint)) { $skipped++; continue }
            [void][IO.Compression.ZipFileExtensions]::CreateEntryFromFile($zip,$full,$path,[IO.Compression.CompressionLevel]::Optimal)
            $count++
        }
    } finally { $zip.Dispose() }
    if ($count -eq 0) { throw 'No files selected.' }
    Move-Item -LiteralPath $temp -Destination $archive -Force
} finally { if (Test-Path -LiteralPath $temp) { Remove-Item -LiteralPath $temp -Force } }
Write-Host "Archive: $archive"
Write-Host "Included: $count; skipped: $skipped"
Write-Host "Size: $([math]::Round((Get-Item -LiteralPath $archive).Length / 1MB,2)) MB"
