#!/usr/bin/env bash
set -euo pipefail
B=http://localhost:8080
# 独立运行时也容忍服务尚未完全就绪：短轮询登陆页。
for _ in $(seq 1 30); do curl -fsS -o /dev/null "$B/" && break || sleep 1; done
jar=$(mktemp); jarb=$(mktemp); trap 'rm -f $jar $jarb' EXIT
curl -fsS -c "$jar" "$B/login?user=alice" -o /dev/null
curl -fsS -c "$jarb" "$B/login?user=bob" -o /dev/null
# allow：alice 看列表含北京
curl -fsS -b "$jar" "$B/orders" | grep -q "北京客户" || { echo "FAIL: alice 应见北京"; exit 1; }
# 数据过滤：bob 列表不含北京
curl -fsS -b "$jarb" "$B/orders" | grep -q "北京客户" && { echo "FAIL: bob 不应见北京"; exit 1; } || true
# deny：bob 删除得 403
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$jarb" -X POST "$B/orders/2/delete")
[ "$code" = "403" ] || { echo "FAIL: bob 删除应 403，实际 $code"; exit 1; }
echo "✅ smoke 通过：allow/deny/数据过滤 均符合预期"
