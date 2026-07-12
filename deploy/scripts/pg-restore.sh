#!/usr/bin/env sh
# 司域 PG 恢复：pg_restore --clean --if-exists（破坏性，先删既有对象）。须 --yes 确认。
# 用法：DATABASE_URL=postgres://... pg-restore.sh <dumpfile> --yes
set -eu
: "${DATABASE_URL:?DATABASE_URL required}"
DUMP="${1:?usage: pg-restore.sh <dumpfile> --yes}"
[ -f "$DUMP" ] || { echo "dump not found: $DUMP" >&2; exit 1; }
case "${2:-}" in
  --yes) ;;
  *) echo "危险操作：--clean 会先删除既有对象。确认请加 --yes" >&2; exit 2 ;;
esac
pg_restore --clean --if-exists --no-owner --no-privileges -d "$DATABASE_URL" "$DUMP"
echo "restore complete from $DUMP"
