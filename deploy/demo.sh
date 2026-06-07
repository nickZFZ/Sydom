#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"
# 健壮加载 .env.demo：set -a 让 source 的变量自动导出，避免 xargs 对含空格/特殊字符的值误拆分。
set -a
# shellcheck disable=SC1091
source .env.demo
set +a
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

echo "等待鉴权链就绪（订单服务 + Sidecar 引导 + 数据策略）..."
# 只探 / 不够：落地页恒 200，不代表 Sidecar 已从控制面引导完策略。
# 改为探「alice 能真正看到北京订单」——证明鉴权链端到端就绪，规避 smoke 竞态假失败。
ready=
jar="$(mktemp)"
for _ in $(seq 1 60); do
	if curl -fsS -c "$jar" "http://localhost:8080/login?user=alice" -o /dev/null 2>/dev/null &&
		curl -fsS -b "$jar" "http://localhost:8080/orders" 2>/dev/null | grep -q "北京客户"; then
		ready=1
		break
	fi
	sleep 1
done
rm -f "$jar"
[ -n "$ready" ] || { echo "鉴权链在 60s 内未就绪"; $DC logs --tail 50 orderservice sidecar; exit 1; }

echo
echo "✅ demo 就绪：浏览器打开 http://localhost:8080"
echo "   用 alice（manager）/ bob（clerk）分别进入对比功能权限与数据权限。"
