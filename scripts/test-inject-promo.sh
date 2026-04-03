#!/usr/bin/env bash
# =============================================================
# test-inject-promo.sh — 单独测试宣传片循环注入 SRS
#
# 用途：验证"宣传片 MP4 → SRS → 可被拉取"这条路径
# 前置条件：
#   1. docker compose up srs 已运行
#   2. 存在一个已转码的测试 MP4 文件
#
# 用法：
#   bash scripts/test-inject-promo.sh [MP4文件路径] [SRS流名称]
#
# 示例：
#   bash scripts/test-inject-promo.sh data/transcoded/1/test_std.mp4 tz
# =============================================================
set -euo pipefail

MP4_FILE="${1:-data/transcoded/1/test_std.mp4}"
STREAM_CODE="${2:-test}"
SRS_HOST="${SRS_RTMP_HOST:-localhost}"
SRS_PORT="${SRS_RTMP_PORT:-1935}"
SRS_API="${SRS_API_URL:-http://localhost:1985}"
FFMPEG="${FFMPEG_BIN:-ffmpeg}"

SRS_TARGET="rtmp://$SRS_HOST:$SRS_PORT/live/$STREAM_CODE"

echo "======================================"
echo "  宣传片循环注入测试"
echo "  文件：$MP4_FILE"
echo "  目标：$SRS_TARGET"
echo "======================================"

# 前置检查
if [ ! -f "$MP4_FILE" ]; then
  echo "✗ 文件不存在：$MP4_FILE"
  echo "  请先上传并等待宣传片转码完成"
  exit 1
fi

command -v "$FFMPEG" >/dev/null || { echo "✗ ffmpeg 未找到"; exit 1; }

echo -e "\n[1/3] 探测文件信息..."
ffprobe -v error -show_entries format=duration,bit_rate -show_entries \
  stream=codec_name,codec_type,width,height,r_frame_rate,sample_rate \
  -of default=noprint_wrappers=1 "$MP4_FILE" 2>/dev/null | grep -E "(codec|width|height|frame_rate|duration|bit_rate|sample_rate)" || true

echo -e "\n[2/3] 启动注入进程（Ctrl+C 停止，持续 30 秒后自动退出）..."
echo "命令："
echo "  $FFMPEG -re -stream_loop -1 -fflags +genpts \\"
echo "    -i \"$MP4_FILE\" \\"
echo "    -c copy -flvflags no_duration_filesize \\"
echo "    -f flv \"$SRS_TARGET\""

# 后台运行 30 秒后自动停止
$FFMPEG -re -stream_loop -1 -fflags +genpts \
  -i "$MP4_FILE" \
  -c copy -flvflags no_duration_filesize \
  -f flv "$SRS_TARGET" \
  -t 30 2>&1 | tail -5 &
FFMPEG_PID=$!

sleep 5
echo -e "\n[3/3] 验证 SRS 是否收到推流..."
IS_PUB=$(curl -sf "$SRS_API/api/v1/streams" | \
  python3 -c "
import sys,json
d=json.load(sys.stdin)
streams = d.get('streams',[]) or d.get('data',{}).get('streams',[])
found = any(s['name']=='$STREAM_CODE' and s.get('publish',{}).get('active') for s in streams)
print('YES' if found else 'NO')
" 2>/dev/null || echo "QUERY_FAIL")

if [ "$IS_PUB" = "YES" ]; then
  echo "  ✓ SRS 已收到推流 live/$STREAM_CODE"
  echo "  可用以下命令拉流验证画面："
  echo "  ffplay rtmp://$SRS_HOST:$SRS_PORT/live/$STREAM_CODE"
else
  echo "  ✗ SRS 未检测到推流（$IS_PUB），请检查网络或 SRS 日志"
fi

wait $FFMPEG_PID 2>/dev/null || true
echo -e "\n✓ 测试完成"
