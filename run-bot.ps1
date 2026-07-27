# run-bot.ps1
# Запуск mem-bot на локальной машине (PowerShell)
# Токен читается из .env.local — туда ты вписываешь TELEGRAM_BOT_TOKEN сам.

$ErrorActionPreference = 'Stop'

$BotRoot   = Split-Path -Parent $MyInvocation.MyCommand.Path
$EnvFile   = Join-Path $BotRoot '.env.local'
$BotExe    = Join-Path $BotRoot 'mem-bot.exe'
$DefaultDataDir = Join-Path $env:USERPROFILE 'mem-bot-data'

# === Загружаем .env.local если есть ===
if (Test-Path $EnvFile) {
    Write-Host "[run-bot] загружаю $EnvFile" -ForegroundColor DarkGray
    Get-Content $EnvFile | ForEach-Object {
        $line = $_.Trim()
        if ($line -and -not $line.StartsWith('#')) {
            $k, $v = $line.Split('=', 2)
            [Environment]::SetEnvironmentVariable($k.Trim(), $v.Trim(), 'Process')
        }
    }
} else {
    Write-Host "[run-bot] $EnvFile не найден" -ForegroundColor Yellow
    Write-Host "Создай файл .env.local рядом со скриптом с содержимым:" -ForegroundColor Yellow
    Write-Host "  TELEGRAM_BOT_TOKEN=твой_токен_от_BotFather" -ForegroundColor Yellow
    Write-Host "  MEM_BOT_DATA_DIR=$DefaultDataDir" -ForegroundColor Yellow
    exit 1
}

# === Проверки ===
if (-not $env:TELEGRAM_BOT_TOKEN) {
    Write-Host "[run-bot] переменная TELEGRAM_BOT_TOKEN не задана" -ForegroundColor Red
    exit 1
}

if (-not (Test-Path $BotExe)) {
    Write-Host "[run-bot] бинарь $BotExe не найден. Сначала выполни: go build -o mem-bot.exe ./cmd/mem-bot/" -ForegroundColor Red
    exit 1
}

# Куда складывать базы пользователей
if (-not $env:MEM_BOT_DATA_DIR) {
    $env:MEM_BOT_DATA_DIR = $DefaultDataDir
}

# Проверим что Ollama жива
try {
    $resp = Invoke-WebRequest -Uri 'http://localhost:11434/api/tags' -TimeoutSec 3 -UseBasicParsing
    Write-Host "[run-bot] Ollama: HTTP $($resp.StatusCode)" -ForegroundColor Green
} catch {
    Write-Host "[run-bot] Ollama недоступна на localhost:11434. Запусти её:" -ForegroundColor Red
    Write-Host "  ollama serve" -ForegroundColor Yellow
    exit 1
}

# Создаём data dir
New-Item -ItemType Directory -Force -Path $env:MEM_BOT_DATA_DIR | Out-Null

Write-Host ""
Write-Host "[run-bot] === mem-bot запускается ===" -ForegroundColor Cyan
Write-Host "[run-bot]   Telegram token: $($env:TELEGRAM_BOT_TOKEN.Substring(0,10))..." -ForegroundColor DarkGray
Write-Host "[run-bot]   Data dir:       $env:MEM_BOT_DATA_DIR" -ForegroundColor DarkGray
Write-Host "[run-bot] Нажми Ctrl+C чтобы остановить бота" -ForegroundColor DarkGray
Write-Host ""

# Прямой запуск — Ctrl+C корректно останавливает
& $BotExe
