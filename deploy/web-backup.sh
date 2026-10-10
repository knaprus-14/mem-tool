#!/usr/bin/env bash
set -euo pipefail
umask 077
# Install alongside web_backup.py in /opt/mem-tool/tools; run as administrator.
# The service account must be dedicated: no concurrent CLI, bot or other worker
# may write into /var/lib/mem-tool during this maintenance window.
tools_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
backup_dir=/var/backups/mem-tool
mkdir -p -- "$backup_dir"
chmod 0700 -- "$backup_dir"
exec 9>"$backup_dir/backup.lock"
flock -n 9
was_active=0
if systemctl is-active --quiet mem-web.service; then was_active=1; fi
restart_service() {
    if [[ "$was_active" == 1 ]]; then systemctl start mem-web.service; fi
}
trap restart_service EXIT
systemctl stop mem-web.service
if systemctl is-active --quiet mem-web.service; then
    echo 'mem-web.service is still active; backup cancelled' >&2
    exit 1
fi
archive="$backup_dir/mem-web-$(date -u +%Y%m%dT%H%M%SZ)-$$.zip"
python3 "$tools_dir/web_backup.py" backup --data /var/lib/mem-tool --output "$archive" --writers-stopped
# The Python command verifies every hash and SQLite database in an isolated restore.
# No automatic pruning: retention must be chosen with the operator's storage plan.
