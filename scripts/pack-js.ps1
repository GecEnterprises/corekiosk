$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
$outDir = Join-Path $repoRoot 'dist\npm'
New-Item -ItemType Directory -Force -Path $outDir | Out-Null
Remove-Item -Path (Join-Path $outDir '*.tgz') -Force -ErrorAction SilentlyContinue

foreach ($pkg in @('client', 'telemetry-rn', 'rn')) {
    $pkgDir = Join-Path $repoRoot "js\packages\$pkg"
    Push-Location $pkgDir
    try {
        npm run build
        if ($LASTEXITCODE -ne 0) { throw "build failed for $pkg" }
        npm pack --pack-destination $outDir
        if ($LASTEXITCODE -ne 0) { throw "pack failed for $pkg" }
    } finally {
        Pop-Location
    }
}

Get-ChildItem $outDir -Filter '*.tgz' | Select-Object Name, Length
