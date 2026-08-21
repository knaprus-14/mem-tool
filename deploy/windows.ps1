param(
    [string]$Target = 'C:\Users\ZMII\bin'
)

$ErrorActionPreference = 'Stop'

$RepoRoot = Split-Path -Parent $PSScriptRoot
$StageRoot = Join-Path ([IO.Path]::GetTempPath()) ("mem-tool-windows-build-" + [Guid]::NewGuid().ToString('N'))
$Binaries = @(
    @{ Name = 'mem.exe'; Package = './cmd/mem' },
    @{ Name = 'mem-index.exe'; Package = './cmd/mem-index' },
    @{ Name = 'mem-bot.exe'; Package = './cmd/mem-bot' }
)

function Remove-Stage {
    foreach ($binary in $Binaries) {
        foreach ($name in @($binary.Name, ('previous-' + $binary.Name))) {
            $path = Join-Path $StageRoot $name
            if (Test-Path -LiteralPath $path -PathType Leaf) {
                Remove-Item -LiteralPath $path -Force
            }
        }
    }
    if (Test-Path -LiteralPath $StageRoot -PathType Container) {
        Remove-Item -LiteralPath $StageRoot -Force
    }
}

Push-Location $RepoRoot
try {
    $running = Get-Process -Name 'mem', 'mem-index', 'mem-bot' -ErrorAction SilentlyContinue
    if ($running) {
        $description = ($running | ForEach-Object { "$($_.ProcessName):$($_.Id)" }) -join ', '
        throw "Close running mem-tool processes before deployment: $description"
    }

    if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
        throw 'Go is not available in PATH.'
    }

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

    if (-not (Test-Path -LiteralPath $Target)) {
        New-Item -ItemType Directory -Path $Target | Out-Null
    }
    $resolvedTarget = (Resolve-Path -LiteralPath $Target).Path

    $prepared = @()
    try {
        foreach ($binary in $Binaries) {
            $source = Join-Path $StageRoot $binary.Name
            $destination = Join-Path $resolvedTarget $binary.Name
            $incoming = $destination + '.new'
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
            $reportedVersions = @()
            foreach ($binary in $Binaries) {
                $output = @(& (Join-Path $resolvedTarget $binary.Name) version)
                if ($LASTEXITCODE -ne 0) {
                    throw "$($binary.Name) version smoke test failed."
                }
                $output | ForEach-Object { Write-Host $_ }
                if ($output.Count -eq 0 -or $output[0] -notmatch ' v([0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?)$') {
                    throw "$($binary.Name) returned an invalid semantic version."
                }
                $reportedVersions += $Matches[1]
            }
            $uniqueVersions = @($reportedVersions | Sort-Object -Unique)
            if ($uniqueVersions.Count -ne 1) {
                throw "Deployed binaries report different versions: $($reportedVersions -join ', ')"
            }
            Write-Host "[deploy] Unified version: $($uniqueVersions[0])"
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
                throw "Deployment failed and rollback was incomplete: $($rollbackErrors -join '; '). Original error: $($deploymentError.Exception.Message)"
            }
            throw $deploymentError
        }
    } finally {
        foreach ($binary in $Binaries) {
            $incoming = (Join-Path $resolvedTarget $binary.Name) + '.new'
            if (Test-Path -LiteralPath $incoming -PathType Leaf) {
                Remove-Item -LiteralPath $incoming -Force
            }
        }
    }

    Write-Host "[deploy] Complete: $resolvedTarget"
} finally {
    Pop-Location
    Remove-Stage
}
