param(
    [string]$Target = 'C:\Users\ZMII\bin'
)

$ErrorActionPreference = 'Stop'

$RepoRoot = Split-Path -Parent $PSScriptRoot
$StageRoot = Join-Path ([IO.Path]::GetTempPath()) ("mem-tool-windows-build-" + [Guid]::NewGuid().ToString('N'))
$PreserveStage = $false
$Binaries = @(
    @{ Name = 'mem.exe'; Package = './cmd/mem' },
    @{ Name = 'mem-index.exe'; Package = './cmd/mem-index' },
    @{ Name = 'mem-bot.exe'; Package = './cmd/mem-bot' }
)
$OriginalBuildEnvironment = @{}
foreach ($name in @('GOCACHE', 'GOOS', 'GOARCH', 'CGO_ENABLED')) {
    $OriginalBuildEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
}

function Assert-WritableTarget([string]$Directory) {
    $targetPath = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($Directory)
    $probe = $null
    $renamedProbe = $null
    try {
        if (Test-Path -LiteralPath $targetPath) {
            if (-not (Test-Path -LiteralPath $targetPath -PathType Container)) {
                throw 'The deployment target is a file, not a directory.'
            }
        } else {
            [IO.Directory]::CreateDirectory($targetPath) | Out-Null
        }
        $targetPath = (Resolve-Path -LiteralPath $targetPath).ProviderPath
        $probe = Join-Path $targetPath ('.mem-deploy-probe-' + [Guid]::NewGuid().ToString('N'))
        $renamedProbe = $probe + '.renamed'
        $stream = [IO.File]::Open($probe, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
        try { $stream.WriteByte(0) } finally { $stream.Dispose() }
        Move-Item -LiteralPath $probe -Destination $renamedProbe -ErrorAction Stop
        Remove-Item -LiteralPath $renamedProbe -Force -ErrorAction Stop
    } catch {
        throw "Deployment target '$targetPath' is not writable. Create, rename and delete access is required; tests and build have not started. $($_.Exception.Message)"
    } finally {
        foreach ($path in @($probe, $renamedProbe)) {
            if ($path) {
                try {
                    if (Test-Path -LiteralPath $path) { Remove-Item -LiteralPath $path -Force -ErrorAction Stop }
                } catch { Write-Warning "Cannot remove deployment probe '$path': $($_.Exception.Message)" }
            }
        }
    }
    return $targetPath
}

function Remove-Stage {
    if ($PreserveStage) {
        Write-Warning "Recovery artifacts preserved for manual restoration: $StageRoot"
        return
    }
    foreach ($binary in $Binaries) {
        foreach ($name in @($binary.Name, ('previous-' + $binary.Name))) {
            $path = Join-Path $StageRoot $name
            try {
                if (Test-Path -LiteralPath $path -PathType Leaf) { Remove-Item -LiteralPath $path -Force -ErrorAction Stop }
            } catch { Write-Warning "Cannot remove staged file '$path': $($_.Exception.Message)" }
        }
    }
    try {
        if (Test-Path -LiteralPath $StageRoot -PathType Container) {
            # Never prompt for recursive removal when cleanup left a file behind.
            [IO.Directory]::Delete($StageRoot, $false)
        }
    } catch { Write-Warning "Staging cleanup incomplete at '$StageRoot': $($_.Exception.Message)" }
}

function Test-BinaryVersions([string]$Directory) {
    $reportedVersions = @()
    foreach ($binary in $Binaries) {
        $output = @(& (Join-Path $Directory $binary.Name) version)
        if ($LASTEXITCODE -ne 0) { throw "$($binary.Name) version smoke test failed." }
        $output | ForEach-Object { Write-Host $_ }
        if ($output.Count -eq 0 -or $output[0] -notmatch ' v([0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?)$') {
            throw "$($binary.Name) returned an invalid semantic version."
        }
        $reportedVersions += $Matches[1]
    }
    $uniqueVersions = @($reportedVersions | Sort-Object -Unique)
    if ($uniqueVersions.Count -ne 1) { throw "Binaries report different versions: $($reportedVersions -join ', ')" }
    return $uniqueVersions[0]
}

Push-Location $RepoRoot
try {
    if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
        throw 'Go is not available in PATH.'
    }

    Write-Host '[deploy] Checking destination access...'
    $resolvedTarget = Assert-WritableTarget $Target

    $env:GOCACHE = Join-Path ([IO.Path]::GetTempPath()) 'mem-tool-gocache'
    $env:GOOS = 'windows'
    $env:GOARCH = 'amd64'
    $env:CGO_ENABLED = '0'

    Write-Host '[deploy] Running tests...'
    & go test ./... -count=1 -timeout=300s
    if ($LASTEXITCODE -ne 0) {
        throw "Tests failed with exit code $LASTEXITCODE."
    }

    New-Item -ItemType Directory -Path $StageRoot | Out-Null
    foreach ($binary in $Binaries) {
        $output = Join-Path $StageRoot $binary.Name
        Write-Host "[deploy] Building $($binary.Name)..."
        & go build -trimpath -o $output $binary.Package
        if ($LASTEXITCODE -ne 0) {
            throw "Build failed for $($binary.Name) with exit code $LASTEXITCODE."
        }
    }

    # Keep the complete release and previous binaries available after any
    # deployment failure, including failures while preparing the destination.
    $PreserveStage = $true
    Write-Host '[deploy] Staged smoke tests...'
    $stagedVersion = Test-BinaryVersions $StageRoot

    $running = Get-Process -Name 'mem', 'mem-index', 'mem-bot' -ErrorAction SilentlyContinue
    if ($running) {
        $description = ($running | ForEach-Object { "$($_.ProcessName):$($_.Id)" }) -join ', '
        throw "Close running mem-tool processes before deployment: $description. Verified release is staged in $StageRoot"
    }

    $prepared = @()
    $incomingPaths = @()
    try {
        foreach ($binary in $Binaries) {
            $source = Join-Path $StageRoot $binary.Name
            $destination = Join-Path $resolvedTarget $binary.Name
            $incoming = $destination + '.' + [Guid]::NewGuid().ToString('N') + '.new'
            $incomingPaths += $incoming
            $sourceHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $source).Hash

            Copy-Item -LiteralPath $source -Destination $incoming -Force
            $incomingHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $incoming).Hash
            if ($sourceHash -ne $incomingHash) {
                throw "Hash mismatch before replacing $($binary.Name)."
            }
            $prepared += [pscustomobject]@{
                Binary = $binary
                Source = $source
                SourceHash = $sourceHash
                Destination = $destination
                Incoming = $incoming
                Backup = Join-Path $StageRoot ('previous-' + $binary.Name)
                HadOriginal = $false
            }
        }

        $attempted = @()
        try {
            foreach ($item in $prepared) {
                if (Test-Path -LiteralPath $item.Destination -PathType Leaf) {
                    Copy-Item -LiteralPath $item.Destination -Destination $item.Backup -Force
                    $item.HadOriginal = $true
                }
                $attempted += $item
                Move-Item -LiteralPath $item.Incoming -Destination $item.Destination -Force
                $deployedHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $item.Destination).Hash
                if ($item.SourceHash -ne $deployedHash) {
                    throw "Hash mismatch after replacing $($item.Binary.Name)."
                }
                Write-Host "[deploy] $($item.Binary.Name) $deployedHash"
            }

            Write-Host '[deploy] Smoke tests...'
            $deployedVersion = Test-BinaryVersions $resolvedTarget
            if ($deployedVersion -ne $stagedVersion) { throw 'Deployed version differs from staged version.' }
            Write-Host "[deploy] Unified version: $deployedVersion"
        } catch {
            $deploymentError = $_
            Write-Warning "Deployment failed; restoring the previous binary set. $($deploymentError.Exception.Message)"
            $rollbackErrors = @()
            for ($i = $attempted.Count - 1; $i -ge 0; $i--) {
                $item = $attempted[$i]
                try {
                    if ($item.HadOriginal) {
                        Copy-Item -LiteralPath $item.Backup -Destination $item.Destination -Force
                    } elseif (Test-Path -LiteralPath $item.Destination -PathType Leaf) {
                        Remove-Item -LiteralPath $item.Destination -Force
                    }
                } catch {
                    $rollbackErrors += "$($item.Binary.Name): $($_.Exception.Message)"
                }
            }
            if ($rollbackErrors.Count -gt 0) {
                $PreserveStage = $true
                throw "Deployment failed and rollback was incomplete: $($rollbackErrors -join '; '). Recovery artifacts: $StageRoot. Original error: $($deploymentError.Exception.Message)"
            }
            throw $deploymentError
        }
    } finally {
        foreach ($incoming in $incomingPaths) {
            try {
                if (Test-Path -LiteralPath $incoming -PathType Leaf) { Remove-Item -LiteralPath $incoming -Force -ErrorAction Stop }
            } catch { Write-Warning "Cannot remove incoming file '$incoming': $($_.Exception.Message)" }
        }
    }

    Write-Host "[deploy] Complete: $resolvedTarget"
    $PreserveStage = $false
} finally {
    foreach ($name in $OriginalBuildEnvironment.Keys) {
        if ($null -eq $OriginalBuildEnvironment[$name]) {
            Remove-Item -LiteralPath ('Env:\' + $name) -ErrorAction SilentlyContinue
        } else {
            [Environment]::SetEnvironmentVariable($name, $OriginalBuildEnvironment[$name], 'Process')
        }
    }
    Pop-Location
    Remove-Stage
}
