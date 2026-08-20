# Деплой mem-bot на Linux VDS (knaprus)

Целевой systemd-юнит запускает Linux-бинарь `/opt/mem-bot/mem-bot`. Обычный
`go build` на Windows создаёт PE/Windows `.exe` и для VDS не подходит.

## Сборка и загрузка с Windows

Проверить архитектуру VDS: `ssh root@knaprus "uname -m"`. Для `x86_64`
используется `GOARCH=amd64`; для `aarch64` — `GOARCH=arm64`.

Из корня репозитория в PowerShell:

```powershell
$oldGOOS = $env:GOOS
$oldGOARCH = $env:GOARCH
$oldCGO = $env:CGO_ENABLED
try {
    $env:GOOS = "linux"
    $env:GOARCH = "amd64"
    $env:CGO_ENABLED = "0"
    go build -trimpath -o mem-bot-linux ./cmd/mem-bot
} finally {
    $env:GOOS = $oldGOOS
    $env:GOARCH = $oldGOARCH
    $env:CGO_ENABLED = $oldCGO
}

scp mem-bot-linux root@knaprus:/tmp/mem-bot
scp deploy/mem-bot.service root@knaprus:/tmp/mem-bot.service
```

## Одноразовая настройка на VDS

Команды выполняются от `root`:

```bash
useradd --system --shell /usr/sbin/nologin --no-create-home membot

install -d -o root -g root -m 0755 /opt/mem-bot
install -d -o membot -g membot -m 0750 /opt/mem-bot/data
install -d -o membot -g membot -m 0750 /var/log/mem-bot
install -o root -g root -m 0755 /tmp/mem-bot /opt/mem-bot/mem-bot

cat > /opt/mem-bot/.env <<'EOF'
TELEGRAM_BOT_TOKEN=ТВОЙ_ТОКЕН_СЮДА
MEM_BOT_DATA_DIR=/opt/mem-bot/data
EOF
chown root:membot /opt/mem-bot/.env
chmod 0640 /opt/mem-bot/.env

install -o root -g root -m 0644 /tmp/mem-bot.service /etc/systemd/system/mem-bot.service
rm -f /tmp/mem-bot /tmp/mem-bot.service

systemctl daemon-reload
systemctl enable --now mem-bot
```

Проверить формат бинаря, состояние и логи:

```bash
file /opt/mem-bot/mem-bot
# Ожидается ELF 64-bit для архитектуры VDS, не PE32/Windows.

systemctl --no-pager --full status mem-bot
journalctl -u mem-bot -n 50 --no-pager
tail -n 50 /var/log/mem-bot/bot.log
```

## Управление

```bash
systemctl status mem-bot
systemctl stop mem-bot
systemctl restart mem-bot
journalctl -u mem-bot -f
```

## Обновление

Сначала повторить PowerShell-сборку выше, затем:

```powershell
scp mem-bot-linux root@knaprus:/tmp/mem-bot.new
ssh root@knaprus "install -o root -g root -m 0755 /tmp/mem-bot.new /opt/mem-bot/mem-bot.new && mv -f /opt/mem-bot/mem-bot.new /opt/mem-bot/mem-bot && rm -f /tmp/mem-bot.new && systemctl restart mem-bot"
ssh root@knaprus "file /opt/mem-bot/mem-bot && systemctl --no-pager --full status mem-bot && journalctl -u mem-bot -n 50 --no-pager"
```

Сначала создаётся новый файл, затем `mv` атомарно заменяет старый бинарь в той
же файловой системе. При ошибке загрузки работающий бинарь не изменяется.

## Консистентный бэкап SQLite

Нельзя архивировать открытые `store.db` обычным `tar`: снимок может попасть на
середину транзакции. Простой вариант с короткой остановкой сервиса:

```bash
# /etc/cron.daily/mem-bot-backup
#!/bin/bash
set -euo pipefail
umask 077

BACKUP_DIR=/var/backups/mem-bot
ARCHIVE="$BACKUP_DIR/data-$(date +%F).tar.gz"
mkdir -p "$BACKUP_DIR"
chmod 0700 "$BACKUP_DIR"

systemctl stop mem-bot
trap 'systemctl start mem-bot' EXIT
tar -C /opt/mem-bot -czf "$ARCHIVE" data
systemctl start mem-bot
trap - EXIT

find "$BACKUP_DIR" -type f -name "data-*.tar.gz" -mtime +30 -delete
```

После создания скрипта: `chmod 0700 /etc/cron.daily/mem-bot-backup`.
Восстановление нужно отдельно проверять на временной директории, открывая
каждый `store.db` через `PRAGMA integrity_check`.

## Безопасность

- Токен хранится в root-owned env-файле `0640`, доступном группе `membot`.
- Бинарь и systemd-юнит принадлежат root; пользователь бота не может их заменить.
- Бот работает без root, с `NoNewPrivileges` и `ProtectSystem=strict`.
- Запись разрешена только в `/opt/mem-bot/data`; Ollama доступна на localhost.

## Проверка после деплоя

Открыть `@mem_knaprus_bot` в Telegram и выполнить `/start`, `/add`, `/search`.
После этого перезапустить сервис и повторить `/recent`, чтобы проверить
сохранность SQLite и корректное закрытие/повторное открытие базы.
