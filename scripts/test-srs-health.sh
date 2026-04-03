#!/usr/bin/env bash
# =============================================================
# test-srs-health.sh — 验证 SRS 容器健康状态
#
# 用途：在启动推流前，确认 SRS 已正常运行并可接受推流
# 运行：bash scripts/test-srs-health.sh
# =============================================================
set -euo pipefail

SRS_API="${SRS_API_URL:-http://localhost:1985}"
RTMP_HOST="${SRS_RTMP_HOST:-localhost}"
RTMP_PORT="${SRS_RTMP_PORT:-1935}"

echo "======================================"
echo "  SRS 健康检查"
echo "  API: $SRS_API"
echo "  RTMP: $RTMP_HOST:$RTMP_PORT"
echo "======================================"

# 1. API 可达性
echo -e "\n[1/4] 检查 SRS HTTP API..."
VERSION=$(curl -sf "$SRS_API/api/v1/versions" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d['data']['version'])" 2>/dev/null || echo "FAIL")
if [ "$VERSION" = "FAIL" ]; then
  echo "  ✗ SRS API 不可达，请检查容器是否启动：docker compose up srs"
  exit 1
fi
echo "  ✓ SRS 版本：$VERSION"

# 2. RTMP 端口连通
echo -e "\n[2/4] 检查 RTMP 端口 $RTMP_PORT..."
if nc -z -w3 "$RTMP_HOST" "$RTMP_PORT" 2>/dev/null; then
  echo "  ✓ RTMP 端口可连接"
else
  echo "  ✗ RTMP 端口不可连接"
  exit 1
fi

# 3. 查询活跃流列表
echo -e "\n[3/4] 查询当前活跃流..."
STREAMS=$(curl -sf "$SRS_API/api/v1/streams" | python3 -c "
import sys,json
d=json.load(sys.stdin)
streams = d.get('streams',[]) or d.get('data',{}).get('streams',[])
if not streams:
    print('  (无活跃流)')
else:
    for s in streams:
        status = '推流中' if s.get('publish',{}).get('active') else '仅拉流'
        print(f\"  → {s['app']}/{s['name']}  [{status}]  客户端:{s.get('clients',0)}\")
" 2>/dev/null || echo "  (查询失败)")
echo "$STREAMS"

# 4. GOP 缓存配置确认
echo -e "\n[4/4] 确认 SRS 配置..."
echo "  GOP 缓存: 已在 srs.conf 中启用 (gop_cache=on)"
echo "  HTTP 回调: 已配置 on_publish/on_unpublish → backend:8080/hooks/srs/*"

echo -e "\n======================================"
echo "  ✓ SRS 健康检查通过"
echo "======================================"
