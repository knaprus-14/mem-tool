$ErrorActionPreference = 'Stop'
$reviewRoot = $PSScriptRoot
$repoRoot = Split-Path -Parent (Split-Path -Parent $reviewRoot)
$replace = @{}
$replace[(Join-Path $repoRoot 'pkg/mem/review_20260905_test.go')] = Join-Path $reviewRoot 'mem_probes.go.txt'
$replace[(Join-Path $repoRoot 'pkg/fileindex/review_20260905_test.go')] = Join-Path $reviewRoot 'fileindex_probes.go.txt'
@{ Replace = $replace } | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $reviewRoot 'overlay.json') -Encoding utf8
$previous = @{}
foreach ($name in @('GOCACHE', 'GOPROXY', 'GOTOOLCHAIN')) {
    $previous[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
}
Push-Location $repoRoot
try {
    $env:GOCACHE = Join-Path ([IO.Path]::GetTempPath()) 'mem-tool-gocache'
    $env:GOPROXY = 'off'
    $env:GOTOOLCHAIN = 'local'
    & go test -overlay (Join-Path $reviewRoot 'overlay.json') ./pkg/mem ./pkg/fileindex -run '^TestReview(FixedChunkStopsAtEnd|ParagraphRespectsMaxSize|FilteredSearchIgnoresExcludedLexicalHits|DateIncludesEntireLastDay|SQLiteWaitsForShortExternalWrite|IndexExplicitHiddenRoot|SourceDoesNotServeChangedPDF|ScanInvalidatesChangedContentVector)$' -count=1 -v -timeout=30s 2>&1 | Tee-Object -FilePath (Join-Path $reviewRoot 'probe-results.txt')
    $probeExitCode = $LASTEXITCODE
} finally {
    Pop-Location
    foreach ($name in $previous.Keys) {
        [Environment]::SetEnvironmentVariable($name, $previous[$name], 'Process')
    }
}
exit $probeExitCode
