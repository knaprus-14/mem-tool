# Exercises the actual rollback and cleanup blocks with private fixture files.
# Does not build binaries or touch the PATH installation.
$ErrorActionPreference = 'Stop'
$scriptPath = Join-Path $PSScriptRoot 'windows.ps1'
$parseErrors = $null
$tokens = $null
$ast = [Management.Automation.Language.Parser]::ParseFile($scriptPath, [ref]$tokens, [ref]$parseErrors)
if ($parseErrors.Count -gt 0) { throw ($parseErrors -join [Environment]::NewLine) }
$cleanup = $ast.Find({ param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq 'Remove-Stage' }, $true)
$rollback = $ast.Find({ param($node) $node -is [Management.Automation.Language.CatchClauseAst] -and $node.Extent.Text.Contains('$rollbackErrors = @()') }, $true)
if (-not $cleanup -or -not $rollback) { throw 'Deployment recovery blocks not found.' }
. ([scriptblock]::Create($cleanup.Extent.Text))
$body = $rollback.Body.Extent.Text
$rollbackBlock = [scriptblock]::Create($body.Substring(1, $body.Length - 2))
$tempBase = [IO.Path]::GetFullPath([IO.Path]::GetTempPath())
$fixtureRoot = Join-Path $tempBase ('mem-deploy-recovery-test-' + [Guid]::NewGuid().ToString('N'))
$StageRoot = Join-Path $fixtureRoot 'stage'
$targetRoot = Join-Path $fixtureRoot 'target'
$Binaries = @(@{ Name = 'mem.exe' }, @{ Name = 'mem-index.exe' }, @{ Name = 'mem-bot.exe' })
$PreserveStage = $false
try {
    New-Item -ItemType Directory -Path $StageRoot, $targetRoot | Out-Null
    $attempted = @()
    foreach ($binary in $Binaries) {
        $backup = Join-Path $StageRoot ('previous-' + $binary.Name)
        $destination = Join-Path $targetRoot $binary.Name
        [IO.File]::WriteAllText($destination, 'new')
        [IO.File]::WriteAllText((Join-Path $StageRoot $binary.Name), 'staged')
        # A missing backup causes one real Copy-Item failure during rollback.
        if ($binary.Name -ne 'mem-index.exe') { [IO.File]::WriteAllText($backup, 'old') }
        $attempted += [pscustomobject]@{ Binary=$binary; Backup=$backup; Destination=$destination; HadOriginal=$true }
    }
    $unrelated = Join-Path $targetRoot 'keep.txt'
    [IO.File]::WriteAllText($unrelated, 'user data')
    $caught = $null
    try {
        try { throw 'Injected deployment failure' } catch { . $rollbackBlock }
    } catch { $caught = $_.Exception.Message }
    if (-not $caught -or -not $caught.Contains('rollback was incomplete')) { throw "Missing rollback failure: $caught" }
    if (-not $PreserveStage) { throw 'Incomplete rollback did not preserve recovery artifacts.' }
    Remove-Stage
    foreach ($name in @('mem.exe', 'mem-bot.exe')) {
        if ([IO.File]::ReadAllText((Join-Path $targetRoot $name)) -ne 'old') { throw "$name was not restored." }
        if (-not (Test-Path -LiteralPath (Join-Path $StageRoot ('previous-' + $name)))) { throw "$name backup was deleted." }
    }
    if ([IO.File]::ReadAllText($unrelated) -ne 'user data') { throw 'Unrelated target file changed.' }
    $PreserveStage = $false
    Remove-Stage
    if (Test-Path -LiteralPath $StageRoot) { throw 'Successful cleanup left staging files.' }
    Write-Host 'PASS: incomplete rollback retains backups; unaffected binaries restore; cleanup preserves unrelated data.'
} finally {
    $resolvedFixture = [IO.Path]::GetFullPath($fixtureRoot)
    $prefix = $tempBase.TrimEnd([IO.Path]::DirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar
    if (-not $resolvedFixture.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase)) { throw 'Unsafe fixture cleanup path.' }
    if (Test-Path -LiteralPath $resolvedFixture) { Remove-Item -LiteralPath $resolvedFixture -Recurse -Force }
}
