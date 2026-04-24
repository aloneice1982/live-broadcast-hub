# 苏超联赛直播分发中台

> 苏超联赛全省 13 城市直播信号统一分发管理系统  
> Go + Gin · Vue 3 · SRS · FFmpeg · SQLite · Docker Compose

---

## 目录

- [项目简介](#项目简介)
- [系统架构](#系统架构)
- [快速安装（生产环境）](#快速安装生产环境)
- [本地开发启动](#本地开发启动)
- [环境变量说明](#环境变量说明)
- [功能一览](#功能一览)
- [目录结构](#目录结构)
- [版本管理](#版本管理)
- [更新日志](#更新日志)

---

## 项目简介

系统为苏超联赛提供全省各地市直播信号的统一转推与分发管理能力：

- 超管在一个大盘中监控全省所有地市推流状态
- 地市管理员在专属控制台完成推流密钥配置、一键开播/停播
- 系统自动完成 **直播源 → SRS 中转 → 微信视频号** 两级转推
- 进程异常时自动重试、熔断，并通过短信发送告警

---

## 系统架构

```
[直播信号源 RTMP/HLS]
        ↓  FFmpeg inject 进程
  [SRS 流媒体中转服务器]
        ↓  FFmpeg push 进程
  [微信视频号 RTMP 推流地址]
```

每个地市维护两个独立的 FFmpeg 子进程：

| 进程 | 作用 |
|------|------|
| `inject` | 拉取直播源，推入内部 SRS |
| `push` | 从 SRS 拉流，推送到视频号公网地址 |

切换信号源时只重启 inject 进程，push 进程保持稳定，视频号端几乎无感中断。

### 技术选型

| 层级 | 技术 |
|------|------|
| 后端 | Go 1.22 + Gin |
| 前端 | Vue 3 + Vite + Tailwind CSS |
| 流媒体 | SRS 5.x |
| 数据库 | SQLite（WAL 模式） |
| 部署 | Docker Compose |
| 鉴权 | JWT（24h） |

### 容器端口

| 容器 | 宿主机端口 | 说明 |
|------|-----------|------|
| frontend | 3080 | Vue 前端（Nginx） |
| backend | 8080 | Go REST API |
| srs | 1935 | RTMP 推流/拉流 |
| srs | 1985 | SRS HTTP API（内网） |

---

## 快速安装（生产环境）

### 方式一：在线安装（服务器可访问 GitHub）

```bash
curl -fsSL https://raw.githubusercontent.com/aloneice1982/live-broadcast-hub/main/install.sh | bash
```

### 方式二：离线安装（内网环境）

1. 在有网机器上打包离线镜像包（约 120MB）：

```bash
docker save ossrs/srs:5 susuper-backend:latest susuper-frontend:latest \
  | gzip > susuper-offline.tar.gz
```

2. 将以下文件拷贝到目标服务器：
   - `susuper-offline.tar.gz`
   - `docker-compose.yml`（本仓库根目录）
   - `srs/conf/srs.conf`
   - `install.sh`

3. 执行安装：

```bash
sudo bash install.sh
```

### 安装向导说明

安装脚本分 5 步交互引导：

```
[1/5] 检查依赖      → Docker / docker compose
[2/5] 基础配置      → 超管初始密码（必填）、JWT 密钥（自动生成）
[3/5] 端口配置      → 前端/后端/RTMP（有默认值，回车跳过；自动检测端口冲突）
[4/5] 短信告警配置  → SMS_API_URL / KEY / 手机号（可选，回车跳过）
[5/5] 启动服务      → 生成 .env → 加载镜像 → docker compose up -d
```

安装完成后访问 `http://服务器IP:3080`，使用设置的密码登录 `admin` 账号。

---

## 本地开发启动

```bash
# 1. 克隆仓库
git clone https://github.com/aloneice1982/live-broadcast-hub.git
cd live-broadcast-hub

# 2. 复制并填写环境变量
cp .env.example .env
# 至少填写：ADMIN_INITIAL_PASSWORD、JWT_SECRET

# 3. 构建并启动（首次约 3-5 分钟）
docker compose up -d --build

# 4. 查看日志
docker compose logs -f backend
```

访问地址：
- 前端：http://localhost:3080
- 后端 API：http://localhost:8080
- SRS API：http://localhost:1985/api/v1/versions

---

## 环境变量说明

完整模板见 `.env.example`，关键变量：

| 变量 | 必填 | 说明 |
|------|------|------|
| `ADMIN_INITIAL_PASSWORD` | ✅ | 首次启动自动创建 admin 账号的密码 |
| `JWT_SECRET` | ✅ | JWT 签名密钥，建议 `openssl rand -hex 32` 生成 |
| `FRONTEND_PORT` | — | 前端端口，默认 `3080` |
| `APP_PORT` | — | 后端端口，默认 `8080` |
| `RTMP_PORT` | — | RTMP 推流端口，默认 `1935` |
| `SMS_API_URL` | — | 短信告警 HTTP 网关地址（留空禁用） |
| `SMS_API_KEY` | — | 短信网关 API Key |
| `SMS_SUPER_ADMIN_PHONES` | — | 告警接收手机号，逗号分隔 |
| `TRANSCODE_CONCURRENCY` | — | 转码并发数，默认 `2` |

---

## 功能一览

### 用户角色

| 角色 | 权限 |
|------|------|
| `super_admin` | 全省大盘 + 所有地市操作 + 用户管理 |
| `city_admin` | 仅限本城市推流操作 |
| `observer` | 只读全省大盘 |

### 核心功能

- **全省大盘**：实时展示所有地市推流状态、在线时长、告警信息
- **地市控制台**：推流密钥配置、一键开播/停播、静音切换、推流密钥每日 05:00 自动清除
- **宣传片管理**：上传 MP4 → 后台自动转码（标准化 1080p/H.264/25fps）→ 在线插播
- **宣传片插播**：单次播放（播完自动切回直播）或循环播放（手动停止），倒计时显示
- **进程守护**：FFmpeg 异常退出自动重启，连续失败 N 次触发熔断 + 短信告警
- **插播熔断兜底**：插播进程异常退出时最多重试 3 次恢复直播，全部失败触发 critical 告警
- **OOM 保护**：FFmpeg 进程 `oom_score_adj = -500`，降低被系统 OOM Kill 的概率

---

## 目录结构

```
live-broadcast-hub/
├── docker-compose.yml          # 本地开发构建用
├── docker-compose.prod.yml     # 生产环境（使用预构建镜像）
├── install.sh                  # 一键安装脚本
├── .env.example                # 环境变量模板
├── srs/conf/srs.conf           # SRS 流媒体配置
│
├── backend/                    # Go 后端
│   ├── Dockerfile
│   ├── main.go                 # 入口 + DB 迁移 + 城市初始化
│   ├── internal/
│   │   ├── config/             # 环境变量读取
│   │   ├── db/                 # SQLite 连接
│   │   ├── handler/            # HTTP 路由处理器
│   │   ├── model/              # 数据库实体
│   │   └── service/
│   │       ├── ffmpeg.go       # FFmpeg 进程守护（核心）
│   │       ├── transcode.go    # 宣传片离线转码
│   │       ├── scheduler.go    # 时间轴调度引擎
│   │       └── sms.go          # 短信告警
│   └── pkg/
│       ├── ffscript/           # FFmpeg 命令生成器
│       └── srsapi/             # SRS HTTP API 客户端
│
├── frontend/                   # Vue 3 前端
│   ├── Dockerfile
│   ├── package.json
│   └── src/
│       ├── views/
│       │   ├── Dashboard.vue   # 超管全省大盘
│       │   ├── CityConsole.vue # 地市控制台
│       │   └── Login.vue
│       └── api/index.ts        # API 请求封装
│
└── scripts/                    # 运维测试脚本
    ├── test-srs-health.sh
    ├── test-inject-promo.sh
    ├── test-inject-live.sh
    └── test-full-pipeline.sh
```

---

## 版本管理

版本号格式：`vMAJOR.MINOR.PATCH`（遵循 [Semantic Versioning](https://semver.org/lang/zh-CN/)）

| 变更类型 | 升级位 | 示例 |
|----------|--------|------|
| bug fix / 小修复 | PATCH +1 | `v1.1.0` → `v1.1.1` |
| 新功能 / 功能升级 | MINOR +1，PATCH 归零 | `v1.1.1` → `v1.2.0` |
| 大版本重构 / 破坏性变更 | MAJOR +1，其余归零 | `v1.2.3` → `v2.0.0` |

### 发版步骤

```bash
# 1. 修改 frontend/package.json 中的 version 字段
# 2. 提交
git commit -am "chore: bump version to x.x.x"
# 3. 打 tag
git tag vx.x.x
# 4. 推送
git push origin main && git push origin vx.x.x
```

推送 tag 后，GitHub Actions 自动构建 `linux/amd64` + `linux/arm64` 镜像并发布 Release。

---

## 更新日志

### v1.1.1 · 2026-04-24
- **fix** `watchdogPromo` 双重 watchdog bug（`startProc` 内部已启动 watchdog，之前重复调用导致 goroutine 竞争）
- **fix** 插播失败后恢复直播无重试问题：新增 3 次重试（间隔 2s），全部失败触发 critical 告警 + 短信

### v1.1.0 · 2026-04-20
- **feat** 一键安装分发方案：`install.sh` 交互向导 + `docker-compose.prod.yml` + GitHub Actions 自动构建发布

### v1.0.x 以前
- 宣传片插播（单次 / 循环）+ 转码进度 + 自动切回
- 宣传片上传管理 + 每城市 1GB 配额校验
- 直播中手动插播宣传片
- 推流带宽保护 + OOM 保护
- 全省大盘 + 地市控制台 + 用户管理
- FFmpeg 进程守护 + 熔断告警 + 短信通知
