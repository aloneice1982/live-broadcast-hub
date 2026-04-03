#!/usr/bin/env bash
# =============================================================
# gen-ffmpeg-commands.sh — 打印所有场景的 FFmpeg 命令（仅输出，不执行）
#
# 用途：运维人员手动验证、复制命令、排查问题时使用
#       所有参数与后端 pkg/ffscript/generator.go 保持一致
# =============================================================

SRS_HOST="${SRS_HOST:-localhost}"
SRS_PORT="${SRS_RTMP_PORT:-1935}"
CITY_CODE="${CITY_CODE:-tz}"
PROMO_FILE="${PROMO_FILE:-/app/data/transcoded/1/promo_std.mp4}"
LIVE_URL="${LIVE_URL:-rtmp://jstv.example.com/live/streamA}"
WX_PUSH_URL="${WX_PUSH_URL:-rtmp://push.weixin.qq.com/push}"
WX_PUSH_KEY="${WX_PUSH_KEY:-your_stream_key_here}"
VOLUME_GAIN="${VOLUME_GAIN:-1.5}"

SRS_INGEST="rtmp://$SRS_HOST:$SRS_PORT/live/$CITY_CODE"
PUSH_DEST="$WX_PUSH_URL/$WX_PUSH_KEY"

line() { printf '─%.0s' {1..60}; echo; }

echo "苏超联赛直播分发中台 — FFmpeg 命令速查"
line

echo ""
echo "【1】宣传片循环注入 SRS（inject-ffmpeg 宣传片模式）"
echo ""
echo "  # 目的：将离线标准化后的 MP4 循环推入 SRS"
echo "  # -re            锁定实时速率，防止推流速率过快"
echo "  # -stream_loop   无限循环（-1 = 永久）"
echo "  # -fflags +genpts 重置时间戳，避免循环时花屏"
echo "  # -c copy        直通，不转码（格式已标准化）"
echo ""
echo "  ffmpeg \\"
echo "    -re \\"
echo "    -stream_loop -1 \\"
echo "    -fflags +genpts \\"
echo "    -i \"$PROMO_FILE\" \\"
echo "    -c copy \\"
echo "    -flvflags no_duration_filesize \\"
echo "    -f flv \\"
echo "    \"$SRS_INGEST\""

line

echo ""
echo "【2】上游直播流注入 SRS（inject-ffmpeg 直播模式）"
echo ""
echo "  # 目的：拉取 JSTV 直播源，叠加音量增益，推入 SRS"
echo "  # -c:v copy      视频绝对禁止转码（核心约束，节省 CPU）"
echo "  # -c:a aac       音频重编码（仅此步骤消耗少量 CPU）"
echo "  # volume=X       音量增益（1.0 ~ 2.0）"
echo "  # -reconnect     网络抖动时自动重连"
echo ""
echo "  ffmpeg \\"
echo "    -reconnect 1 \\"
echo "    -reconnect_streamed 1 \\"
echo "    -reconnect_delay_max 3 \\"
echo "    -timeout 10000000 \\"
echo "    -i \"$LIVE_URL\" \\"
echo "    -c:v copy \\"
echo "    -c:a aac -b:a 128k -ar 44100 -ac 2 \\"
echo "    -af \"volume=$VOLUME_GAIN\" \\"
echo "    -async 1 \\"
echo "    -f flv \\"
echo "    \"$SRS_INGEST\""

line

echo ""
echo "【3】SRS → 视频号推流（push-ffmpeg，全程不停）"
echo ""
echo "  # 目的：将 SRS 中继的流推到微信视频号"
echo "  # -c copy        直接转发，不处理任何内容"
echo "  # -reconnect     切流时 push 端短暂断流后自动恢复"
echo ""
echo "  ffmpeg \\"
echo "    -reconnect 1 \\"
echo "    -reconnect_streamed 1 \\"
echo "    -reconnect_delay_max 2 \\"
echo "    -i \"$SRS_INGEST\" \\"
echo "    -c copy \\"
echo "    -f flv \\"
echo "    -flvflags no_duration_filesize \\"
echo "    \"$PUSH_DEST\""

line

echo ""
echo "【4】宣传片离线标准化转码（上传后一次性执行）"
echo ""
echo "  # 目的：将任意 MP4 转为与直播流格式一致的标准文件"
echo "  # -g 50 -keyint_min 50 -sc_threshold 0  固定 2s GOP"
echo "  # -movflags +faststart                   头部前置"
echo ""
echo "  ffmpeg \\"
echo "    -i /app/data/uploads/1/original.mp4 \\"
echo "    -c:v libx264 -preset veryfast -profile:v main -level:v 4.0 \\"
echo "    -vf \"scale=1920:1080:force_original_aspect_ratio=decrease,pad=1920:1080:(ow-iw)/2:(oh-ih)/2,fps=25\" \\"
echo "    -g 50 -keyint_min 50 -sc_threshold 0 \\"
echo "    -b:v 2500k -maxrate 3000k -bufsize 6000k \\"
echo "    -c:a aac -b:a 128k -ar 44100 -ac 2 \\"
echo "    -movflags +faststart \\"
echo "    -y /app/data/transcoded/1/original_std.mp4"

line

echo ""
echo "【5】验证 SRS 流状态（运维排查用）"
echo ""
echo "  # 查询所有活跃流："
echo "  curl http://$SRS_HOST:1985/api/v1/streams | python3 -m json.tool"
echo ""
echo "  # 用 ffplay 拉流验证画面："
echo "  ffplay \"$SRS_INGEST\""
echo ""
echo "  # 检查流参数（帧率/分辨率/编码）："
echo "  ffprobe -v error -show_streams -of json \"$SRS_INGEST\" 2>/dev/null | python3 -m json.tool"

line
echo ""
echo "提示：设置环境变量可自定义上方命令中的参数"
echo "  CITY_CODE=nj VOLUME_GAIN=1.8 bash scripts/gen-ffmpeg-commands.sh"
echo ""
