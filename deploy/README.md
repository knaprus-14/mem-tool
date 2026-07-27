# Деплой mem-bot на VDS (knaprus)

## Одноразовая настройка (от root)

```bash
# 1. Создаём пользователя для бота (без shell, без home)
useradd --system --shell /usr/sbin/nologin --no-create-home membot

# 2. Создаём директории
mkdir -p /opt/mem-bot/data
mkdir -p /var/log/mem-bot
chown -R membot:membot /opt/mem-bot
chown -R membot:membot /var/log/mem-bot
chmod 750 /opt/mem-bot/data

# 3. Копируем бинарь (с локальной машины)
scp E:\Programming\claude\mem-tool\mem-bot.exe root@knaprus:/opt/mem-bot/

# 4. Создаём env-файл с токеном
cat > /opt/mem-bot/.env <<'EOF'
TELEGRAM_BOT_TOKEN=ТВОЙ_ТОКЕН_СЮДА
MEM_BOT_DATA_DIR=/opt/mem-bot/data
EOF
chown membot:membot /opt/mem-bot/.env
chmod 600 /opt/mem-bot/.env

# 5. Устанавливаем systemd-юнит
cp /tmp/mem-bot.service /etc/systemd/system/mem-bot.service
systemctl daemon-reload
systemctl enable mem-bot
systemctl start mem-bot

# 6. Проверяем
systemctl status mem-bot
journalctl -u mem-bot -f
tail -f /var/log/mem-bot/bot.log
```

## Управление

```bash
systemctl status mem-bot    # статус
systemctl stop mem-bot      # остановить
systemctl restart mem-bot   # перезапустить
systemctl logs mem-bot      # логи (journalctl)
```

## Обновление

```bash
# С локальной машины — собрать новую версию
cd E:\Programming\claude\mem-tool
go build -o mem-bot.exe ./cmd/mem-bot/

# Скопировать на VDS
scp mem-bot.exe root@knaprus:/opt/mem-bot/

# Перезапустить бота
ssh root@knaprus "systemctl restart mem-bot"
```

## Бэкапы

```bash
# Базы пользователей (per-user SQLite) — в /opt/mem-bot/data/
# Бэкап раз в день через cron:

# /etc/cron.daily/mem-bot-backup
#!/bin/bash
BACKUP_DIR=/var/backups/mem-bot
mkdir -p $BACKUP_DIR
tar czf $BACKUP_DIR/data-$(date +%F).tar.gz /opt/mem-bot/data
# Хранить 30 дней
find $BACKUP_DIR -name "data-*.tar.gz" -mtime +30 -delete
```

## Безопасность

- ✅ Токен в env-файле с правами 600, owner=membot
- ✅ Бот работает от отдельного пользователя без root
- ✅ ProtectSystem=strict — доступ только к /opt/mem-bot
- ✅ NoNewPrivileges — запрет эскалации привилегий
- ⚠️ Ollama на localhost:11434 — должен быть доступен для membot

## Проверка после деплоя

Открыть `@mem_knaprus_bot` в Telegram → `/start` → должно прийти приветствие.
