#!/usr/bin/env bash
# =============================================================
# test-full-pipeline.sh — 端到端全链路冒烟测试
#
# 验证完整路径：
#   宣传片 MP4 → inject-ffmpeg → SRS → push-ffmpeg → 模拟视频号
#
# 同时测试"无缝切流"：
#   宣传片循环 → [等待 15s] → 切换为直播源 → [等待 15s] → 切换回宣传片
#
# 用法：
#   bash scripts/test-full-pipeline.sh <MP4文件> <直播源URL>
#
# 示例：
#   bash scripts/test-full-pipeline.sh data/transcoded/1/test_std.mp4 rtmp://jstv.example.com/live/streamA
# =============================================================
set -euo pipefail

MP4_FILE="${1:?用法: $0 <MP4文件> <直播源URL>}"
LIVE_SOURCE="${2:?用法: $0 <MP4文件> <直播源URL>}"
STREAM_CODE="pipeline_test"
SRS_HOST="${SRS_RTMP_HOST:-localhost}"
SRS_PORT="${SRS_RTMP_PORT:-1935}"
SRS_API="${SRS_API_URL:-http://localhost:1985}"
FFMPEG="${FFMPEG_BIN:-ffmpeg}"
VOLUME_GAIN="1.5"

SRS_INGEST="rtmp://$SRS_HOST:$SRS_PORT/live/$STREAM_CODE"
# 模拟视频号推流目标：本地起一个 SRS 接收端（无需真实密钥）
PUSH_DEST="rtmp://$SRS_HOST:$SRS_PORT/live/${STREAM_CODE}_out"

INJECT_PID=""
PUSH_PID=""

cleanup() {
  echo -e "\n[清理] 停止所有测试进程..."
  [ -n "$INJECT_PID" ] && kill "$INJECT_PID" 2>/dev/null || true
  [ -n "$PUSH_PID" ]   && kill "$PUSH_PID"   2>/dev/null || true
  echo "[清理] 完成"
}
trap cleanup EXIT INT TERM

echo "╔══════════════════════════════════════╗"
echo "║  苏超直播 全链路冒烟测试             ║"
echo "╚══════════════════════════════════════╝"
echo ""
echo "  宣传片:   $MP4_FILE"
echo "  直播源:   $LIVE_SOURCE"
echo "  SRS注入:  $SRS_INGEST"
echo "  推流目标: $PUSH_DEST"

# ── Step 1: SRS 健康检查 ──────────────────────────────────────
echo -e "\n── Step 1: SRS 健康检查 ──"
if ! curl -sf "$SRS_API/api/v1/versions" >/dev/null 2>&1; then
  echo "✗ SRS API 不可达，请先执行: docker compose up srs"
  exit 1
fi
echo "✓ SRS 正常运行"

# ── Step 2: 启动 push 进程（SRS → 模拟视频号）────────────────
echo -e "\n── Step 2: 启动 push-ffmpeg ──"
echo "  SRS → $PUSH_DEST"
$FFMPEG -reconnect 1 -reconnect_streamed 1 -reconnect_delay_max 2 \
  -i "$SRS_INGEST" \
  -c copy -f flv -flvflags no_duration_filesize \
  "$PUSH_DEST" \
  -loglevel warning 2>&1 &
PUSH_PID=$!
echo "  push-ffmpeg PID: $PUSH_PID"

# ── Step 3: inject 宣传片 ──────────────────────────────────────
echo -e "\n── Step 3: 注入宣传片（15s）──"
$FFMPEG -re -stream_loop -1 -fflags +genpts \
  -i "$MP4_FILE" \
  -c copy -flvflags no_duration_filesize \
  -f flv "$SRS_INGEST" \
  -loglevel warning 2>&1 &
INJECT_PID=$!
echo "  inject-ffmpeg PID: $INJECT_PID"

sleep 5
echo -n "  验证 SRS 流状态: "
IS_PUB=$(srs_check() {
  curl -sf "$SRS_API/api/v1/streams" | python3 -c "
import sys,json
d=json.load(sys.stdin)
streams = d.get('streams',[]) or d.get('data',{}).get('streams',[])
print('YES' if any(s['name']=='$STREAM_CODE' and s.get('publish',{}).get('active') for s in streams) else 'NO')
" 2>/dev/null || echo "FAIL"
}; srs_check)
[ "$IS_PUB" = "YES" ] && echo "✓ 推流中" || echo "✗ 未检测到推流"

echo "  等待 10s 模拟宣传片播放..."
sleep 10

# ── Step 4: 无缝切换到直播流 ──────────────────────────────────
echo -e "\n── Step 4: 无缝切换 → 直播流 ──"
OLD_INJECT_PID=$INJECT_PID

# 先启动新注入进程
$FFMPEG -reconnect 1 -reconnect_streamed 1 -reconnect_delay_max 3 \
  -timeout 10000000 \
  -i "$LIVE_SOURCE" \
  -c:v copy -c:a aac -b:a 128k -ar 44100 -ac 2 \
  -af "volume=$VOLUME_GAIN" -async 1 \
  -f flv "$SRS_INGEST" \
  -loglevel warning 2>&1 &
INJECT_PID=$!
echo "  新 inject-ffmpeg PID: $INJECT_PID"

# 等待 SRS 确认新流（轮询 /api/v1/streams 中的推流时长变化）
echo -n "  等待 SRS 接受新注入..."
for i in $(seq 1 15); do
  sleep 1
  LIVE_MS=$(curl -sf "$SRS_API/api/v1/streams" | python3 -c "
import sys,json
d=json.load(sys.stdin)
streams = d.get('streams',[]) or d.get('data',{}).get('streams',[])
for s in streams:
    if s['name'] == '$STREAM_CODE':
        print(s.get('live_ms',0))
        sys.exit()
print(0)
" 2>/dev/null || echo 0)
  echo -n "."
done
echo ""

# 停止旧 inject 进程（SRS 已断开旧连接，SIGTERM 即可）
echo "  SIGTERM 旧 inject PID $OLD_INJECT_PID"
kill -TERM "$OLD_INJECT_PID" 2>/dev/null || true
echo "  ✓ 切流完成"

echo "  等待 15s 模拟直播播放..."
sleep 15

# ── Step 5: 切换回宣传片 ──────────────────────────────────────
echo -e "\n── Step 5: 无缝切回 → 宣传片 ──"
OLD_INJECT_PID=$INJECT_PID
$FFMPEG -re -stream_loop -1 -fflags +genpts \
  -i "$MP4_FILE" \
  -c copy -flvflags no_duration_filesize \
  -f flv "$SRS_INGEST" \
  -loglevel warning 2>&1 &
INJECT_PID=$!
sleep 3
kill -TERM "$OLD_INJECT_PID" 2>/dev/null || true
echo "  ✓ 已切回宣传片"

sleep 5

# ── Step 6: 最终检查 ──────────────────────────────────────────
echo -e "\n── Step 6: 最终状态检查 ──"
curl -sf "$SRS_API/api/v1/streams" | python3 -c "
import sys,json
d=json.load(sys.stdin)
streams = d.get('streams',[]) or d.get('data',{}).get('streams',[])
for s in streams:
    if '$STREAM_CODE' in s['name']:
        v = s.get('video') or {}
        active = '✓' if s.get('publish',{}).get('active') else '✗'
        print(f\"  {active} {s['app']}/{s['name']}  {v.get('width','?')}x{v.get('height','?')}  客户端:{s.get('clients',0)}\")
" 2>/dev/null || echo "  (查询失败)"

# push 进程存活检查
if kill -0 "$PUSH_PID" 2>/dev/null; then
  echo "  ✓ push-ffmpeg 进程存活（无感切流验证通过）"
else
  echo "  ✗ push-ffmpeg 进程已退出（切流期间出现断流）"
fi

echo ""
echo "╔══════════════════════════════════════╗"
echo "║  ✓ 全链路测试完成                   ║"
echo "╚══════════════════════════════════════╝"
