#!/usr/bin/env bash
# =============================================================
# test-inject-live.sh — 单独测试上游直播流拉取 + 音量增益注入
#
# 用途：验证"JSTV 直播源 → 音量增益 → SRS"路径
#       并测量 CPU 占用（确认 -c:v copy 生效）
#
# 用法：
#   bash scripts/test-inject-live.sh [RTMP源地址] [音量增益] [SRS流名称]
#
# 示例：
#   bash scripts/test-inject-live.sh rtmp://jstv.example.com/live/streamA 1.5 tz
# =============================================================
set -euo pipefail

SOURCE_URL="${1:?请提供源地址，例如 rtmp://jstv.example.com/live/streamA}"
VOLUME_GAIN="${2:-1.5}"
STREAM_CODE="${3:-test}"
SRS_HOST="${SRS_RTMP_HOST:-localhost}"
SRS_PORT="${SRS_RTMP_PORT:-1935}"
SRS_API="${SRS_API_URL:-http://localhost:1985}"
FFMPEG="${FFMPEG_BIN:-ffmpeg}"
TEST_DURATION=30

SRS_TARGET="rtmp://$SRS_HOST:$SRS_PORT/live/$STREAM_CODE"

echo "======================================"
echo "  直播流注入测试（含音量增益）"
echo "  源:    $SOURCE_URL"
echo "  增益:  ${VOLUME_GAIN}x"
echo "  目标:  $SRS_TARGET"
echo "  时长:  ${TEST_DURATION}s"
echo "======================================"

command -v "$FFMPEG" >/dev/null || { echo "✗ ffmpeg 未找到"; exit 1; }

# 构造完整命令（与 ffscript.InjectLiveArgs 输出一致）
FFMPEG_CMD=(
  "$FFMPEG"
  # 输入端重连
  -reconnect 1
  -reconnect_streamed 1
  -reconnect_delay_max 3
  -timeout 10000000
  -i "$SOURCE_URL"
  # 视频直通（核心约束：禁止转码）
  -c:v copy
  # 音频重编码 + 增益
  -c:a aac
  -b:a 128k
  -ar 44100
  -ac 2
  -af "volume=${VOLUME_GAIN}"
  -async 1
  # 输出
  -f flv
  "$SRS_TARGET"
  # 测试模式：限制时长
  -t "$TEST_DURATION"
)

echo -e "\n生成的 FFmpeg 命令："
echo "  ${FFMPEG_CMD[*]}"

echo -e "\n[1/3] 启动注入进程..."
"${FFMPEG_CMD[@]}" 2>&1 &
FFMPEG_PID=$!
echo "  PID: $FFMPEG_PID"

sleep 5
echo -e "\n[2/3] 监控 CPU 占用（应 < 20% 表明 -c:v copy 生效）..."
if command -v ps >/dev/null 2>&1; then
  CPU=$(ps -p $FFMPEG_PID -o %cpu --no-headers 2>/dev/null || echo "N/A")
  echo "  FFmpeg CPU: ${CPU}%"
  if [ "$CPU" != "N/A" ]; then
    CPU_INT=${CPU%.*}
    if [ "$CPU_INT" -gt 60 ] 2>/dev/null; then
      echo "  ⚠ CPU 占用过高！请确认 -c:v copy 是否生效"
      echo "    可能原因：源流格式不兼容，FFmpeg 自动触发了转码"
    else
      echo "  ✓ CPU 占用正常，视频直通确认"
    fi
  fi
fi

echo -e "\n[3/3] 验证 SRS 流状态..."
sleep 3
curl -sf "$SRS_API/api/v1/streams" | python3 -c "
import sys,json
d=json.load(sys.stdin)
streams = d.get('streams',[]) or d.get('data',{}).get('streams',[])
for s in streams:
    if s['name'] == '$STREAM_CODE':
        v = s.get('video') or {}
        a = s.get('audio') or {}
        print(f\"  ✓ 流已就绪: live/$STREAM_CODE\")
        print(f\"  视频: {v.get('codec','?')} {v.get('width','?')}x{v.get('height','?')}\")
        print(f\"  音频: {a.get('codec','?')} {a.get('sample_rate','?')}Hz\")
        print(f\"  客户端数: {s.get('clients',0)}\")
        sys.exit(0)
print('  ✗ 未找到流 live/$STREAM_CODE')
" 2>/dev/null || echo "  (查询失败)"

echo -e "\n等待测试结束（${TEST_DURATION}s 后自动退出）..."
wait $FFMPEG_PID 2>/dev/null || true
echo "✓ 测试完成"
