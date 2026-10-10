param(
    [Parameter(Mandatory=$true)][string]$ReleaseStage
)

# Run the real installer transaction with already-built binaries and private
# fixture paths. No compilation or PATH installation occurs in this test.
$ErrorActionPreference = 'Stop'
$parseErrors = $null
$tokens = $null
$ast = [Management.Automation.Language.Parser]::ParseFile((Join-Path $PSScriptRoot 'windows.ps1'), [ref]$tokens, [ref]$parseErrors)
if ($parseErrors.Count) { throw ($parseErrors -join [Environment]::NewLine) }
foreach ($function in $ast.FindAll({ param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst] }, $true)) {
    . ([scriptblock]::Create($function.Extent.Text))
}
$install = $ast.FindAll({ param($node)
    $node -is [Management.Automation.Language.TryStatementAst] -and $node.Extent.Text.Contains('$sourceHash = ')
}, $true) | Sort-Object { $_.Extent.Text.Length } | Select-Object -First 1
if (-not $install) { throw 'Installer transaction not found.' }
$installBlock = [scriptblock]::Create($install.Extent.Text)
$tempBase = [IO.Path]::GetFullPath([IO.Path]::GetTempPath())
$fixtureRoot = Join-Path $tempBase ('mem-install-test-' + [Guid]::NewGuid().ToString('N'))
$Binaries = @(@{Name='mem.exe'}, @{Name='mem-index.exe'}, @{Name='mem-bot.exe'})
$lock = $null
try {
    foreach ($scenario in @('success', 'locked second destination')) {
        $StageRoot = Join-Path (Join-Path $fixtureRoot $scenario) 'stage'
        $resolvedTarget = Join-Path (Join-Path $fixtureRoot $scenario) 'target with spaces [literal]'
        [IO.Directory]::CreateDirectory($StageRoot) | Out-Null
        [IO.Directory]::CreateDirectory($resolvedTarget) | Out-Null
        foreach ($binary in $Binaries) {
            Copy-Item -LiteralPath (Join-Path $ReleaseStage $binary.Name) -Destination (Join-Path $StageRoot $binary.Name)
            [IO.File]::WriteAllText((Join-Path $resolvedTarget $binary.Name), ('previous ' + $binary.Name))
        }
        $marker = Join-Path $resolvedTarget 'mem.exe.new'
        [IO.File]::WriteAllText($marker, 'unrelated file')
        $prepared = @()
        $incomingPaths = @()
        $PreserveStage = $true
        $stagedVersion = Test-BinaryVersions $StageRoot
        $caught = $null
        if ($scenario -ne 'success') {
            $lock = [IO.File]::Open((Join-Path $resolvedTarget 'mem-index.exe'), [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::None)
        }
        try { . $installBlock } catch { $caught = $_.Exception.Message } finally {
            if ($lock) { $lock.Dispose(); $lock = $null }
        }
        if ($scenario -eq 'success') {
            if ($caught) { throw "Successful installation failed: $caught" }
            foreach ($binary in $Binaries) {
                $expected = (Get-FileHash -LiteralPath (Join-Path $StageRoot $binary.Name) -Algorithm SHA256).Hash
                $actual = (Get-FileHash -LiteralPath (Join-Path $resolvedTarget $binary.Name) -Algorithm SHA256).Hash
                if ($actual -ne $expected) { throw "Installed hash differs: $($binary.Name)" }
                if ([IO.File]::ReadAllText((Join-Path $StageRoot ('previous-' + $binary.Name))) -ne ('previous ' + $binary.Name)) { throw 'Backup changed.' }
            }
            if ((Test-BinaryVersions $resolvedTarget) -ne $stagedVersion) { throw 'Installed version differs.' }
        } else {
            if (-not $caught) { throw 'Locked destination did not stop installation.' }
            foreach ($binary in $Binaries) {
                if ([IO.File]::ReadAllText((Join-Path $resolvedTarget $binary.Name)) -ne ('previous ' + $binary.Name)) {
                    throw "Partial replacement was not rolled back: $($binary.Name)"
                }
            }
        }
        if ([IO.File]::ReadAllText($marker) -ne 'unrelated file') { throw 'Unrelated file changed.' }
        foreach ($incoming in $incomingPaths) {
            if (Test-Path -LiteralPath $incoming) { throw "Temporary incoming file left behind: $incoming" }
        }
        Write-Host "PASS: $scenario; all three files verified; unrelated file preserved."
    }
} finally {
    if ($lock) { $lock.Dispose() }
    $resolvedFixture = [IO.Path]::GetFullPath($fixtureRoot)
    $prefix = $tempBase.TrimEnd([IO.Path]::DirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar
    if (-not $resolvedFixture.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase)) { throw 'Unsafe fixture cleanup path.' }
    if (Test-Path -LiteralPath $resolvedFixture) { Remove-Item -LiteralPath $resolvedFixture -Recurse -Force }
}
