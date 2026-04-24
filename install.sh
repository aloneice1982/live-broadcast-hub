#!/usr/bin/env bash
# =============================================================
# 苏超联赛直播分发中台 — 一键安装脚本
# 支持：Ubuntu 20.04+ / Debian 11+ / CentOS 8+ / Rocky 8+
# 依赖：Docker（已安装）、openssl、curl
# =============================================================

set -euo pipefail

REPO="aloneice1982/live-broadcast-hub"
RAW_BASE="https://raw.githubusercontent.com/${REPO}/main"
INSTALL_DIR="${INSTALL_DIR:-/opt/susuper}"
COMPOSE_FILE="docker-compose.prod.yml"

# ---------- 颜色输出 ----------
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
CYAN='\033[0;36m'; BOLD='\033[1m'; RESET='\033[0m'

info()    { echo -e "${CYAN}[INFO]${RESET}  $*"; }
success() { echo -e "${GREEN}[✓]${RESET}    $*"; }
warn()    { echo -e "${YELLOW}[WARN]${RESET}  $*"; }
error()   { echo -e "${RED}[✗]${RESET}    $*" >&2; }
die()     { error "$*"; exit 1; }

# ---------- 检测 docker compose 命令 ----------
detect_compose() {
  if docker compose version &>/dev/null 2>&1; then
    COMPOSE_CMD="docker compose"
  elif command -v docker-compose &>/dev/null; then
    COMPOSE_CMD="docker-compose"
  else
    die "未找到 docker compose 或 docker-compose，请先安装 Docker（含 Compose 插件）"
  fi
}

# ---------- 端口冲突检测 ----------
port_in_use() {
  local port="$1"
  if command -v ss &>/dev/null; then
    ss -tlnp 2>/dev/null | grep -q ":${port}\b"
  elif command -v netstat &>/dev/null; then
    netstat -tlnp 2>/dev/null | grep -q ":${port}\b"
  else
    return 1  # 无法检测则跳过
  fi
}

# ---------- 带端口冲突检测的交互输入 ----------
read_port() {
  local prompt="$1" default="$2" varname="$3"
  local val
  while true; do
    printf "%s [%s]: " "$prompt" "$default"
    read -r val </dev/tty
    val="${val:-$default}"
    if ! [[ "$val" =~ ^[0-9]+$ ]] || [ "$val" -lt 1 ] || [ "$val" -gt 65535 ]; then
      warn "端口号无效，请输入 1-65535 之间的数字"
      continue
    fi
    if port_in_use "$val"; then
      warn "端口 ${val} 已被占用，请换一个端口"
      continue
    fi
    eval "$varname=\"$val\""
    break
  done
}

# ---------- 主流程 ----------
main() {
  clear
  echo -e "${BOLD}${CYAN}"
  echo "================================================"
  echo "  苏超联赛直播分发中台 — 一键安装向导 v1.0.0"
  echo "================================================"
  echo -e "${RESET}"
  echo "  本脚本将引导你完成安装配置，全程约 3-5 分钟"
  echo "  （镜像拉取时间取决于网络速度）"
  echo ""

  # ---- [1/5] 检查依赖 ----
  echo -e "${BOLD}[1/5] 检查运行环境${RESET}"
  command -v docker &>/dev/null || die "Docker 未安装，请先参考 https://docs.docker.com/engine/install/ 安装"
  detect_compose
  success "Docker 已就绪，compose 命令：${COMPOSE_CMD}"
  command -v openssl &>/dev/null || die "openssl 未安装（sudo apt install openssl）"
  command -v curl &>/dev/null    || die "curl 未安装（sudo apt install curl）"
  success "系统依赖检查通过"
  echo ""

  # ---- [2/5] 基础配置 ----
  echo -e "${BOLD}[2/5] 基础配置${RESET}"

  # 管理员密码（必填，不能为空）
  local admin_password=""
  while [ -z "$admin_password" ]; do
    printf "  超管初始密码（登录账号 admin 的密码，必填）: "
    read -rs admin_password </dev/tty
    echo ""
    if [ -z "$admin_password" ]; then
      warn "密码不能为空，请重新输入"
    fi
  done

  # JWT 密钥（自动生成）
  local jwt_secret
  jwt_secret="$(openssl rand -hex 32)"
  success "JWT 密钥已自动生成（64位随机串）"
  echo ""

  # ---- [3/5] 端口配置 ----
  echo -e "${BOLD}[3/5] 端口配置${RESET}"
  echo "  （直接回车使用默认值，如有冲突请修改）"
  echo ""
  local frontend_port backend_port rtmp_port
  read_port "  前端访问端口  " "3080" frontend_port
  read_port "  后端 API 端口 " "8080" backend_port
  read_port "  RTMP 推流端口 " "1935" rtmp_port
  echo ""

  # ---- [4/5] 短信告警（可选） ----
  echo -e "${BOLD}[4/5] 短信告警配置（可选，直接回车跳过）${RESET}"
  echo "  告警信息仍会记录在系统日志中，短信仅用于即时通知"
  echo ""
  printf "  短信 API 地址 (SMS_API_URL): "
  local sms_url; read -r sms_url </dev/tty; sms_url="${sms_url:-}"
  printf "  短信 API Key  (SMS_API_KEY): "
  local sms_key; read -r sms_key </dev/tty; sms_key="${sms_key:-}"
  printf "  告警手机号（多个用英文逗号分隔）: "
  local sms_phones; read -r sms_phones </dev/tty; sms_phones="${sms_phones:-}"
  echo ""

  # ---- [5/5] 安装 ----
  echo -e "${BOLD}[5/5] 正在安装...${RESET}"

  # 创建安装目录
  mkdir -p "${INSTALL_DIR}"
  cd "${INSTALL_DIR}"

  # 生成 .env
  info "生成 .env 配置文件..."
  cat > .env <<EOF
# 苏超联赛直播分发中台 - 自动生成的配置文件
# 生成时间：$(date '+%Y-%m-%d %H:%M:%S')

APP_ENV=production
APP_PORT=${backend_port}
JWT_SECRET=${jwt_secret}

FRONTEND_PORT=${frontend_port}
RTMP_PORT=${rtmp_port}
SRS_API_PORT=1985
SRS_HTTP_PORT=8088

DB_PATH=/app/data/db/susuper.db
UPLOAD_DIR=/app/data/uploads
TRANSCODE_DIR=/app/data/transcoded
MAX_UPLOAD_SIZE_MB=2048

SRS_HOST=srs
SRS_RTMP_PORT=1935

FFMPEG_BIN=ffmpeg
TRANSCODE_CONCURRENCY=2
TRANSCODE_RESOLUTION=1920x1080
TRANSCODE_FPS=25
TRANSCODE_KEYFRAME_INTERVAL=50
TRANSCODE_AUDIO_BITRATE=128k
TRANSCODE_AUDIO_SAMPLERATE=44100

FFMPEG_MAX_RETRIES=3
FFMPEG_RETRY_DELAY_SECONDS=3

SMS_API_URL=${sms_url}
SMS_API_KEY=${sms_key}
SMS_SUPER_ADMIN_PHONES=${sms_phones}

ADMIN_INITIAL_PASSWORD=${admin_password}
EOF
  success ".env 文件已生成 → ${INSTALL_DIR}/.env"

  # 下载 docker-compose.prod.yml
  info "下载 docker-compose.prod.yml..."
  curl -fsSL "${RAW_BASE}/${COMPOSE_FILE}" -o "${COMPOSE_FILE}" \
    || die "下载 docker-compose.prod.yml 失败，请检查网络"
  success "docker-compose.prod.yml 已下载"

  # 拉取镜像
  info "拉取镜像（首次可能需要几分钟）..."
  ${COMPOSE_CMD} -f "${COMPOSE_FILE}" pull \
    || die "镜像拉取失败，请检查网络或 GitHub Container Registry 访问权限"
  success "镜像拉取完成"

  # 启动服务
  info "启动容器..."
  ${COMPOSE_CMD} -f "${COMPOSE_FILE}" up -d \
    || die "容器启动失败，请运行 '${COMPOSE_CMD} -f ${INSTALL_DIR}/${COMPOSE_FILE} logs' 查看日志"

  # 等待后端就绪
  info "等待服务启动（最多 30 秒）..."
  local attempts=0
  until curl -sf "http://127.0.0.1:${backend_port}/api/cities" -o /dev/null 2>/dev/null; do
    attempts=$((attempts + 1))
    [ "$attempts" -ge 30 ] && break
    sleep 1
  done

  # ---------- 完成提示 ----------
  echo ""
  echo -e "${BOLD}${GREEN}"
  echo "================================================"
  echo "  安装完成！"
  echo "================================================"
  echo -e "${RESET}"

  # 获取本机 IP
  local server_ip
  server_ip=$(curl -sf --max-time 3 https://ipinfo.io/ip 2>/dev/null || hostname -I 2>/dev/null | awk '{print $1}' || echo "YOUR_SERVER_IP")

  echo "  🌐 前端管理界面：  http://${server_ip}:${frontend_port}"
  echo "  🔧 后端 API：      http://${server_ip}:${backend_port}"
  echo "  📡 RTMP 推流地址： rtmp://${server_ip}:${rtmp_port}/live/{城市代码}"
  echo ""
  echo "  👤 登录账号：admin"
  echo "  🔑 初始密码：（你刚才设置的密码）"
  echo ""
  echo -e "${YELLOW}  ⚠️  提示：${RESET}"
  echo "     • 首次登录后请立即修改超管密码"
  echo "     • 各城市推流密钥需在城市控制台手动填写"
  echo "     • 每天 05:00 系统会自动清空推流密钥，请每日填写"
  echo ""
  echo "  📂 安装目录：${INSTALL_DIR}"
  echo "  📋 查看日志：cd ${INSTALL_DIR} && ${COMPOSE_CMD} -f ${COMPOSE_FILE} logs -f"
  echo "  ⏹  停止服务：cd ${INSTALL_DIR} && ${COMPOSE_CMD} -f ${COMPOSE_FILE} down"
  echo ""
}

main "$@"
