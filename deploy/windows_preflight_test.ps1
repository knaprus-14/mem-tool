$ErrorActionPreference = 'Stop'
$scriptPath = Join-Path $PSScriptRoot 'windows.ps1'
$parseErrors = $null
$tokens = $null
$ast = [Management.Automation.Language.Parser]::ParseFile($scriptPath, [ref]$tokens, [ref]$parseErrors)
if ($parseErrors.Count) { throw ($parseErrors -join [Environment]::NewLine) }
foreach ($function in $ast.FindAll({param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst]}, $true)) {
    . ([scriptblock]::Create($function.Extent.Text))
}
$tempBase = [IO.Path]::GetFullPath([IO.Path]::GetTempPath())
$fixtureRoot = Join-Path $tempBase ('mem-preflight-test-' + [Guid]::NewGuid().ToString('N'))
$callerEnvironment = @{}
$nativeExitVariable = Get-Variable -Name LASTEXITCODE -Scope Global -ErrorAction SilentlyContinue
$hadNativeExit = $null -ne $nativeExitVariable
$callerNativeExit = if ($hadNativeExit) { $nativeExitVariable.Value } else { $null }
foreach ($name in @('GOCACHE','GOOS','GOARCH','CGO_ENABLED')) {
    $callerEnvironment[$name] = [Environment]::GetEnvironmentVariable($name,'Process')
}
$global:MemDeployGoCalls = 0
# Failure injection stops before compilation, while the full entry script
# must still execute its environment restoration and cleanup.
function go { $global:MemDeployGoCalls++; $global:LASTEXITCODE = 23 }
$lock = $null
try {
    $target = Join-Path $fixtureRoot 'target with spaces [literal]'
    $actual = Assert-WritableTarget $target
    if ($actual -ne $target) { throw "Target path changed: $actual" }
    if (@(Get-ChildItem -LiteralPath $target -Force).Count -ne 0) { throw 'Preflight left temporary files.' }
    Write-Host 'PASS: preflight creates, renames and deletes only its unique probe; literal path preserved.'

    $notDirectory = Join-Path $fixtureRoot 'target is a file'
    [IO.File]::WriteAllText($notDirectory, 'keep')
    $caught = $null
    try { & $scriptPath -Target $notDirectory } catch { $caught = $_.Exception.Message }
    if (-not $caught -or $caught -notlike '*tests and build have not started*' -or $global:MemDeployGoCalls -ne 0) {
        throw "Invalid target did not fail before Go: error=$caught calls=$global:MemDeployGoCalls"
    }
    if ([IO.File]::ReadAllText($notDirectory) -ne 'keep') { throw 'Preflight changed the existing file.' }
    Write-Host 'PASS: invalid target rejected before tests/build; existing file unchanged.'

    foreach ($arch in @('arm64', $null)) {
        $wanted = @{GOCACHE=(Join-Path $fixtureRoot 'caller cache'); GOOS='linux'; GOARCH=$arch; CGO_ENABLED='1'}
        foreach ($name in $wanted.Keys) {
            if ($null -eq $wanted[$name]) { Remove-Item -LiteralPath ('Env:\' + $name) -ErrorAction SilentlyContinue }
            else { [Environment]::SetEnvironmentVariable($name,$wanted[$name],'Process') }
        }
        $callsBefore = $global:MemDeployGoCalls
        $caught = $null
        try { & $scriptPath -Target $target } catch { $caught = $_.Exception.Message }
        if ($caught -notlike '*Tests failed with exit code 23*' -or $global:MemDeployGoCalls -ne $callsBefore + 1) { throw "Injected Go failure was lost: $caught" }
        foreach ($name in $wanted.Keys) {
            if ([Environment]::GetEnvironmentVariable($name,'Process') -cne $wanted[$name]) { throw "Environment variable $name was not restored." }
        }
    }
    Write-Host 'PASS: failed build workflow restores existing and unset Go environment variables.'

    $StageRoot = Join-Path $fixtureRoot 'locked cleanup'
    [IO.Directory]::CreateDirectory($StageRoot) | Out-Null
    $Binaries = @(@{Name='mem.exe'})
    $PreserveStage = $false
    $lockedPath = Join-Path $StageRoot 'mem.exe'
    [IO.File]::WriteAllText($lockedPath, 'test')
    $lock = [IO.File]::Open($lockedPath,[IO.FileMode]::Open,[IO.FileAccess]::Read,[IO.FileShare]::None)
    $caught = $null
    try {
        try { throw 'original deployment failure' } finally { Remove-Stage }
    } catch { $caught = $_.Exception.Message }
    if ($caught -ne 'original deployment failure') { throw "Cleanup masked original failure: $caught" }
    $lock.Dispose(); $lock = $null
    Remove-Stage
    if (Test-Path -LiteralPath $StageRoot) { throw 'Unlocked staging directory was not removed.' }
    Write-Host 'PASS: locked staging cleanup preserves original failure and never requests recursive deletion.'
} finally {
    if ($lock) { $lock.Dispose() }
    foreach ($name in $callerEnvironment.Keys) {
        if ($null -eq $callerEnvironment[$name]) { Remove-Item -LiteralPath ('Env:\' + $name) -ErrorAction SilentlyContinue }
        else { [Environment]::SetEnvironmentVariable($name,$callerEnvironment[$name],'Process') }
    }
    Remove-Item -LiteralPath Function:\go
    Remove-Variable -Name MemDeployGoCalls -Scope Global
    if ($hadNativeExit) { Set-Variable -Name LASTEXITCODE -Scope Global -Value $callerNativeExit }
    else { Remove-Variable -Name LASTEXITCODE -Scope Global -ErrorAction SilentlyContinue }
    $resolvedFixture = [IO.Path]::GetFullPath($fixtureRoot)
    $prefix = $tempBase.TrimEnd([IO.Path]::DirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar
    if (-not $resolvedFixture.StartsWith($prefix,[StringComparison]::OrdinalIgnoreCase)) { throw 'Unsafe fixture cleanup path.' }
    if (Test-Path -LiteralPath $resolvedFixture) { Remove-Item -LiteralPath $resolvedFixture -Recurse -Force }
}
