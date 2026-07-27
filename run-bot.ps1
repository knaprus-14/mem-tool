# run-bot.ps1
# Local launcher for mem-bot (PowerShell 5.1 compatible, ASCII only).
# Token is read from .env.local - you paste it in yourself, AI never sees it.

$ErrorActionPreference = 'Stop'

$BotRoot   = Split-Path -Parent $MyInvocation.MyCommand.Path
$EnvFile   = Join-Path $BotRoot '.env.local'
$BotExe    = Join-Path $BotRoot 'mem-bot.exe'
$DefaultDataDir = Join-Path $env:USERPROFILE 'mem-bot-data'

# Load .env.local if present
if (Test-Path $EnvFile) {
    Write-Host "[run-bot] loading $EnvFile" -ForegroundColor DarkGray
    Get-Content $EnvFile | ForEach-Object {
        $line = $_.Trim()
        if ($line -and -not $line.StartsWith('#')) {
            $k, $v = $line.Split('=', 2)
            if ($k) { [Environment]::SetEnvironmentVariable($k.Trim(), $v.Trim(), 'Process') }
        }
    }
} else {
    Write-Host "[run-bot] $EnvFile not found" -ForegroundColor Yellow
    Write-Host "Create file .env.local next to the script with content:" -ForegroundColor Yellow
    Write-Host "  TELEGRAM_BOT_TOKEN=your_bot_token_from_BotFather" -ForegroundColor Yellow
    Write-Host "  MEM_BOT_DATA_DIR=$DefaultDataDir" -ForegroundColor Yellow
    exit 1
}

# Checks
if (-not $env:TELEGRAM_BOT_TOKEN) {
    Write-Host "[run-bot] TELEGRAM_BOT_TOKEN is not set" -ForegroundColor Red
    exit 1
}

if (-not (Test-Path $BotExe)) {
    Write-Host "[run-bot] binary $BotExe not found. Build first:" -ForegroundColor Red
    Write-Host "  go build -o mem-bot.exe ./cmd/mem-bot/" -ForegroundColor Yellow
    exit 1
}

if (-not $env:MEM_BOT_DATA_DIR) {
    $env:MEM_BOT_DATA_DIR = $DefaultDataDir
}

# Check Ollama
try {
    $resp = Invoke-WebRequest -Uri 'http://localhost:11434/api/tags' -TimeoutSec 3 -UseBasicParsing
    Write-Host "[run-bot] Ollama: HTTP $($resp.StatusCode)" -ForegroundColor Green
} catch {
    Write-Host "[run-bot] Ollama is not available at localhost:11434" -ForegroundColor Red
    Write-Host "  Run: ollama serve" -ForegroundColor Yellow
    exit 1
}

New-Item -ItemType Directory -Force -Path $env:MEM_BOT_DATA_DIR | Out-Null

Write-Host ""
Write-Host "[run-bot] === mem-bot starting ===" -ForegroundColor Cyan
Write-Host "[run-bot]   Telegram token: $($env:TELEGRAM_BOT_TOKEN.Substring(0,10))..." -ForegroundColor DarkGray
Write-Host "[run-bot]   Data dir:       $env:MEM_BOT_DATA_DIR" -ForegroundColor DarkGray
Write-Host "[run-bot] Press Ctrl+C to stop the bot" -ForegroundColor DarkGray
Write-Host ""

& $BotExe
