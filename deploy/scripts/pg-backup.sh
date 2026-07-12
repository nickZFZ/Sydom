#!/usr/bin/env sh
# 司域 PG 逻辑备份：pg_dump 自定义格式(-Fc,压缩,可选择性恢复)到 BACKUP_DIR，时间戳命名。
# 用法：DATABASE_URL=postgres://... BACKUP_DIR=/backups [RETENTION_DAYS=7] [POST_HOOK=cmd] pg-backup.sh
set -eu
: "${DATABASE_URL:?DATABASE_URL required}"
BACKUP_DIR="${BACKUP_DIR:-/backups}"
mkdir -p "$BACKUP_DIR"
TS=$(date -u +%Y%m%dT%H%M%SZ)
OUT="$BACKUP_DIR/sydom-$TS.dump"
pg_dump "$DATABASE_URL" -Fc --no-owner --no-privileges -f "$OUT"
echo "backup written: $OUT ($(wc -c < "$OUT") bytes)"
if [ -n "${RETENTION_DAYS:-}" ]; then
  find "$BACKUP_DIR" -name 'sydom-*.dump' -type f -mtime "+$RETENTION_DAYS" -delete
fi
[ -n "${POST_HOOK:-}" ] && sh -c "$POST_HOOK" "$OUT" || true
