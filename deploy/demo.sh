#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"
export $(grep -v '^#' .env.demo | xargs)
DC="docker compose --env-file .env.demo"

echo "[1/5] 起 PG + Redis ..."
$DC up -d postgres redis

echo "[2/5] 迁移数据库 ..."
$DC up migrate

echo "[3/5] 起控制面 ..."
$DC up -d controlplane

echo "[4/5] 供应 app（建角色/权限/授权/数据策略，捕获 app_secret）..."
APP_SECRET="$($DC run --rm -e CP_ADMIN_ADDR=controlplane:8081 -e SYDOM_ROOT_SECRET="$SYDOM_ROOT_SECRET" seeder)"
[ -n "$APP_SECRET" ] || { echo "供应失败：未取得 app_secret"; exit 1; }

echo "[5/5] 起 Sidecar + 订单服务 ..."
SYDOM_APP_SECRET="$APP_SECRET" $DC up -d sidecar orderservice

echo
echo "✅ demo 就绪：浏览器打开 http://localhost:8080"
echo "   用 alice（manager）/ bob（clerk）分别进入对比功能权限与数据权限。"
