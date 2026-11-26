param([switch]$SkipBrowserProfile)
$ErrorActionPreference = 'Continue'
Set-Location (Split-Path -Parent $PSScriptRoot)
$report = Join-Path (Get-Location) 'atlas-diagnostics.txt'
if ($env:ATLAS_DIAGNOSTICS_REPORT) { $report = $env:ATLAS_DIAGNOSTICS_REPORT }
$profile = Join-Path (Get-Location) ('atlas-profile-' + (Get-Date -Format 'yyyyMMdd-HHmmss-fff'))
$script:failed = $false
function Add-Report([string]$line) {
    Write-Host $line
    $line | Out-File -FilePath $report -Append -Encoding utf8
}
function Run-Check([string]$label, [string]$exe, [string[]]$arguments) {
    Add-Report "`n=== $label ==="
    Add-Report ($exe + ' ' + ($arguments -join ' '))
    if (-not (Get-Command $exe -ErrorAction SilentlyContinue)) {
        Add-Report "ERROR: executable unavailable: $exe"
        $script:failed = $true
        return
    }
    $watch = [Diagnostics.Stopwatch]::StartNew()
    & $exe @arguments 2>&1 | ForEach-Object { Add-Report ([string]$_) }
    $code = $LASTEXITCODE
    $watch.Stop()
    Add-Report ('EXIT={0} ELAPSED_SEC={1:F2}' -f $code, $watch.Elapsed.TotalSeconds)
    if ($code -ne 0) { $script:failed = $true }
}
'ATLAS DIAGNOSTICS v1' | Out-File -FilePath $report -Encoding utf8
Add-Report (Get-Date -Format o)
Add-Report ('OS=' + [Environment]::OSVersion.VersionString + ' logical_cpus=' + [Environment]::ProcessorCount)
try {
    $cpu = Get-CimInstance Win32_Processor -ErrorAction Stop | Select-Object -First 1
    $ram = Get-CimInstance Win32_ComputerSystem -ErrorAction Stop
    Add-Report ('CPU=' + $cpu.Name + ' RAM_bytes=' + $ram.TotalPhysicalMemory)
} catch { Add-Report 'Hardware details unavailable' }
Run-Check 'Go version' 'go' @('version')
Run-Check 'Node version' 'node' @('--version')
if (Get-Command git -ErrorAction SilentlyContinue) { Run-Check 'Git revision' 'git' @('rev-parse','HEAD') }
$oldChrome = $env:ATLAS_CHROME
$oldProfile = $env:ATLAS_PROFILE
$oldSkipMaps = $env:ATLAS_PROFILE_SKIP_MAPS
try {
    # Avoid inheriting expensive optional profiling from an unrelated session.
    Remove-Item Env:ATLAS_PROFILE -ErrorAction SilentlyContinue
    if (-not $env:ATLAS_CHROME) {
        $roots = @($env:ProgramFiles, ${env:ProgramFiles(x86)}, $env:LOCALAPPDATA) | Where-Object { $_ }
        foreach ($root in $roots) {
            foreach ($relative in @('Google\Chrome\Application\chrome.exe','Microsoft\Edge\Application\msedge.exe')) {
                $candidate = Join-Path $root $relative
                if (Test-Path $candidate) { $env:ATLAS_CHROME = $candidate; break }
            }
            if ($env:ATLAS_CHROME) { break }
        }
    }
    Add-Report ('Browser=' + $env:ATLAS_CHROME)
    if (-not $env:ATLAS_CHROME) { Add-Report 'SKIP: real browser checks require Chrome or Edge (ATLAS_CHROME).' }
    Run-Check 'Server diagnostics (synthetic fixtures, not production data)' 'go' @('test','-run','^TestAtlasPerformanceDiagnostics$','-count=1','-timeout=20m','-v','.')
    Run-Check 'Go functional and integration tests' 'go' @('test','-run','Test','-skip','^TestAtlasPerformanceDiagnostics$','-count=1','-timeout=20m','-v','./...')
    $js = @(Get-ChildItem -Path web -Filter '*.test.js' -File | Sort-Object Name | ForEach-Object { $_.FullName })
    if ($js.Count -gt 0) { Run-Check 'JavaScript tests' 'node' (@('--test') + $js) }
    else { Add-Report 'ERROR: no JavaScript test files'; $script:failed = $true }
    Run-Check 'Existing map index benchmarks (ns/op, B/op, allocs/op)' 'go' @('test','-run','^$','-bench','^Benchmark(SceneElementSpatialIndex|OversizedElementQuery|TokenPositionIndexUpdate)$','-benchmem','-benchtime=300ms','-count=3','-timeout=10m','.')
    if ($env:ATLAS_CHROME -and -not $SkipBrowserProfile) {
        New-Item -ItemType Directory -Path $profile -Force | Out-Null
        $env:ATLAS_PROFILE = $profile
        $env:ATLAS_PROFILE_SKIP_MAPS = '1'
        Run-Check 'Existing browser/map profile (4096 map subset)' 'go' @('test','-run','^TestProfileAtlas$','-count=1','-timeout=30m','-v','.')
        try {
            $results = Get-Content -LiteralPath (Join-Path $profile 'results.json') -Raw -ErrorAction Stop | ConvertFrom-Json -ErrorAction Stop
            $measurements = @($results.rows | ForEach-Object { $_.clients } | Where-Object { $_.metrics.frame.count -gt 0 })
            if ($measurements.Count -eq 0) { throw 'No browser frame measurements in results.json' }
            Add-Report ('Measured browser client samples: ' + $measurements.Count)
        } catch {
            Add-Report ('ERROR: browser profile did not produce frame measurements: ' + $_.Exception.Message)
            $script:failed = $true
        }
        foreach ($name in @('summary.csv','results.json')) {
            $path = Join-Path $profile $name
            if (Test-Path $path) {
                Add-Report "`n=== Browser $name ==="
                Get-Content -LiteralPath $path | ForEach-Object { Add-Report $_ }
            }
        }
        Add-Report ('Raw browser profile directory: ' + $profile)
    } else { Add-Report 'SKIP: browser performance profile (disabled or no browser).' }
} finally {
    $env:ATLAS_CHROME = $oldChrome
    $env:ATLAS_PROFILE = $oldProfile
    $env:ATLAS_PROFILE_SKIP_MAPS = $oldSkipMaps
}
Add-Report "`n=== Finished ==="
Add-Report ('Failed checks: ' + $script:failed)
Add-Report ('Report: ' + $report)
if ($script:failed) { exit 1 }
exit 0
