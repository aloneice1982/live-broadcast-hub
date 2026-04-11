# 直播分发中台 — 功能需求与技术实现说明

> 本文档用于外包开发交接，描述系统功能需求、技术架构与界面设计要求。
> 版本：1.1  日期：2026-04-09

---

## 一、项目背景

系统为苏超联赛（江苏省内城市足球联赛）提供全省 13 个参赛地市的直播信号统一分发管理能力。核心目标：

- 超级管理员在一个大盘中监控全省所有地市的直播推流状态
- 各地市管理员在自己的操控台完成推流密钥配置、一键开播、停播等操作
- 系统自动完成直播源→中转服务器→视频号的两级推流，并在异常时自动重试和告警

---

## 二、系统架构

### 2.1 整体结构

```
[直播信号源 RTMP/HLS]
        ↓
  [中转流媒体服务器 SRS]    ← FFmpeg inject 进程注入
        ↓
  [FFmpeg push 进程]
        ↓
  [微信视频号 RTMP 推流端点]
```

每个地市维护**两个独立的 FFmpeg 子进程**：
- **inject 进程**：将直播信号源拉取后推入内部 SRS 服务器
- **push 进程**：将 SRS 内的流转推到视频号的公网 RTMP 地址

这种双进程设计的优势：切换信号源时只重启 inject 进程，push 进程保持稳定连接，视频号端几乎无感知中断。

### 2.2 技术选型

| 层级 | 技术选型 | 说明 |
|------|---------|------|
| 后端 | Go + Gin | REST API 服务，单二进制部署 |
| 前端 | Vue 3 + Vite + Tailwind CSS | SPA，Nginx 静态托管 |
| 流媒体 | SRS 5.x | 开源 RTMP/HLS 服务器，官方 Docker 镜像 |
| 数据库 | SQLite（WAL 模式） | 轻量，无需独立数据库服务 |
| 部署 | Docker Compose | 三容器：frontend / backend / srs |
| 鉴权 | JWT（24h 有效期） | Bearer Token，前端 Pinia 存储登录态 |

### 2.3 容器与端口

```yaml
frontend:  宿主机 3080 → 容器 80   （Nginx 托管 Vue 构建产物）
backend:   宿主机 8080 → 容器 8080  （Go HTTP API）
srs:       宿主机 1935 → 容器 1935  （RTMP）
           宿主机 1985 → 容器 1985  （SRS HTTP API，内网调用）
```

---

## 三、用户角色

| 角色 | 说明 | 权限范围 |
|------|------|---------|
| `super_admin` | 超级管理员 | 全部功能：所有地市操作 + 直播源管理 + 用户管理 |
| `city_admin` | 地市管理员 | 仅限自己所属地市的推流操作 |
| `observer` | 观察员 | 只读全省大盘，无法操作任何城市 |

---

## 四、功能模块详述

### 4.1 登录页

**功能**：
- 用户名 + 密码登录
- 登录成功后按角色跳转：`super_admin` / `observer` → 全省大盘；`city_admin` → 自己城市的操控台
- JWT Token 存入本地，页面刷新保持登录态
- Token 过期自动跳转登录页

**接口**：`POST /api/auth/login`

---

### 4.2 全省大盘（超管 / 观察员）

超管和观察员登录后进入此页面，分为三个 Tab（观察员只能看"全省大盘"Tab）。

#### Tab 1：全省大盘

**页面结构**：
1. 顶部统计栏：4 个数字卡片，分别显示当前「推流中」「预热中」「异常/熔断」「空闲」的地市数量
2. 服务器带宽实时展示：上行 Mbps / 下行 Mbps，每 5 秒轮询更新
3. 地市卡片网格：13 个城市卡片，2-5 列自适应布局

**地市卡片内容**：
- 城市名称 + 城市代码
- 推流状态圆点（带动效）：
  - 空闲：灰色静态
  - 等待开播：蓝色脉冲
  - 预热中：黄色脉冲
  - 推流中：绿色静态
  - 异常：红色脉冲
  - 熔断：红色扩散动效
- 推流中时：显示当前信号源名称 + 已推流时长（HH:MM:SS，实时递增）
- 熔断时：显示「清除熔断」按钮（仅超管可见）

**交互**：
- 超管点击卡片进入该城市操控台
- 观察员卡片不可点击
- 页面每 5 秒自动轮询所有城市状态

#### Tab 2：直播源管理（仅超管）

**功能说明**：
直播源是系统从外部拉取的原始直播信号（如电视台转播流），全省共享，由超管统一维护。

**子功能**：

1. **CSV 批量导入**
   - 提供模板下载（CSV 格式，含字段说明行）
   - 上传 CSV 文件后批量导入，返回「成功 N 条，跳过 N 条」
   - 模板字段：`name, url, match_datetime, round, channel, remark`

2. **手动添加**
   - 表单字段：名称（必填）、RTMP/HLS 地址（必填）、比赛日期、比赛时间、轮次、所属频道、备注

3. **直播源列表**
   - 支持按日期过滤（默认今日）
   - 显示：名称、地址、比赛时间、轮次、频道、备注
   - 已过期的直播源（比赛时间早于今天）置灰显示，标注「已过期」
   - 每条可「上线/下线」切换、删除

**接口**：
- `GET /api/stream-sources` — 列表
- `POST /api/stream-sources` — 手动新建
- `PUT /api/stream-sources/:id` — 更新（含上下线）
- `DELETE /api/stream-sources/:id` — 删除
- `GET /api/stream-sources/template` — 下载 CSV 模板
- `POST /api/stream-sources/import` — CSV 批量导入

#### Tab 3：用户管理（仅超管）

**功能**：
- 创建账号：用户名、密码（≥8位）、角色（地市管理员/超管/观察员）、所属城市（地市管理员必填）、手机号（用于告警短信）
- 用户列表：展示所有账号的用户名、角色、城市、手机号
- 修改密码：行内展开修改密码表单
- 删除账号：不能删除自己

---

### 4.3 地市操控台

地市管理员登录后直接进入本城市操控台；超管点击大盘卡片也进入此页。

页面为单页式纵向布局，包含三个卡片：推流配置、推流控制、告警记录。

#### 卡片 1：推流配置

配置该城市向视频号推流所需的地址和密钥。

**字段**：
- **微信视频号推流地址**（RTMP，有默认值）
- **推流密钥**（文本输入，每次比赛前从视频号后台获取）
- **音量增益**（滑块，1.0x～2.0x，对音量较小的信号源进行放大）
- **SRS 内网挂载点**（只读展示，供技术排查）

**锁定机制**：
- 保存配置后进入「已锁定」状态，卡片收起显示「🔒 推流密钥已就绪」
- 锁定后才允许开始推流
- 可点「修改」重新编辑

**接口**：
- `GET /api/cities/:cityId/stream-config`
- `PUT /api/cities/:cityId/stream-config`

#### 卡片 2：推流控制

**空闲状态**：
- 下拉框选择今日直播源（自动过滤：仅显示今日及常驻直播源）
- 选中直播源后显示预览卡（比赛时间、轮次、频道）
- 「⚡ 开始推流」按钮（配置未锁定时禁用并提示）

**推流中状态**：
- 显示当前信号源名称
- 显示实时推流时长（HH:MM:SS）
- 「🔇 静音 / 🔊 恢复」切换按钮（注：切换时画面可能短暂卡顿 1-2 秒）
- 「⏹ 停止推流」按钮

**熔断状态**：
- 显示红色告警横幅，说明熔断原因
- 「清除熔断」按钮：停止进程、重置状态、清除告警

**状态轮询**：每 3 秒自动刷新一次推流状态

**接口**：
- `GET /api/cities/:cityId/status` — 获取推流状态（含进度、当前信号源名、推流时长起始时间）
- `POST /api/cities/:cityId/ffmpeg/direct-push` — 开始推流（Body: `{"streamSourceId": 123}`）
- `POST /api/cities/:cityId/ffmpeg/reset` — 停止推流
- `POST /api/cities/:cityId/ffmpeg/mute` — 静音切换（Body: `{"muted": true}`）

#### 卡片 3：告警记录

- 展示该城市的历史告警日志列表（时间、级别、内容）
- 告警级别：`warn` / `error` / `critical`，不同颜色区分
- 「清除告警」按钮：同时停止进程、重置 DB 状态、清除所有告警日志

**接口**：
- `GET /api/cities/:cityId/alerts`
- `POST /api/cities/:cityId/alerts/clear`

---

## 五、后端核心实现要点

### 5.1 双进程推流模型

```
StartCity(cityID, firstItem):
  1. 从 DB 读取 stream_configs 获取 push_url, push_key, volume_gain
  2. 启动 push-ffmpeg: SRS内网流 → rtmp://视频号地址/密钥
  3. 等待 500ms（push 进程预热）
  4. 启动 inject-ffmpeg: 直播源URL → SRS内网流
  5. 写 DB: ffmpeg_processes.status = 'streaming'

StopCity(cityID):
  1. cancel context（通知所有 goroutine 退出）
  2. 向进程发送 SIGTERM，3 秒后如未退出强制 SIGKILL
  3. 写 DB: status = 'idle'
```

### 5.2 进程守护与熔断

每个 FFmpeg 进程启动时，同时启动一个 watchdog goroutine 监控进程退出事件：

```
watchdog(process):
  等待进程退出
  if 正常停止(context已取消) → 不处理
  retries++
  读取 FFmpeg stderr 最后一行 → 分析故障原因（中文化）
  if retries < 最大重试次数(3):
    等待重试延迟(3秒)
    重新启动进程
  else:
    触发熔断:
      写 DB status = 'breaker_open'
      发送短信告警
      停止双进程
```

故障原因中文化分类（从 FFmpeg stderr 关键词判断）：推流密钥被拒绝、服务器拒绝连接、连接超时、连接被重置、文件不存在、流格式错误、RTMP 推流失败等。

### 5.3 推流状态 API 设计

`GET /api/cities/:cityId/status` 返回的数据结构：

```json
{
  "status": "streaming | warming | failed | breaker_open | idle",
  "currentItemName": "苏超流A-泰州vs南京",
  "retryCount": 0,
  "lastStartedAt": "2026-04-08 19:30:00",
  "schedulerActive": false,
  "scheduleStatus": "",
  "currentItemIndex": 0,
  "todayItemCount": 3
}
```

重要逻辑：若 DB 记录状态为 `streaming` 但内存中无活跃 session（服务重启后遗留脏数据），接口自动将状态修正为 `idle` 并更新 DB。

### 5.4 数据库表结构概要

| 表名 | 说明 |
|------|------|
| `cities` | 13 个参赛城市（name, code），系统初始化时写入，不允许用户修改 |
| `users` | 账号表（username, password_hash, role, city_id, phone） |
| `stream_sources` | 直播信号源（name, url, match_datetime, round, channel, is_active） |
| `stream_configs` | 各城市推流配置（push_url, push_key, volume_gain, config_locked, srs_app, srs_stream） |
| `ffmpeg_processes` | 各城市 FFmpeg 进程状态追踪（status, retry_count, current_item_id, last_started_at, direct_source_name） |
| `alert_logs` | 告警日志（city_id, level, message, sms_sent） |

数据库使用 SQLite WAL 模式，开启外键约束。所有表在服务启动时通过 schema.sql 自动建表（IF NOT EXISTS）。

### 5.5 鉴权中间件

- 所有 `/api/` 路由除登录外均需 Bearer JWT
- JWT Claims 包含：user_id, role, city_id
- `city_admin` 调用城市相关接口时，中间件校验 JWT 中的 city_id 与 URL 参数是否一致
- `super_admin` 可访问所有城市
- `observer` 仅可访问只读接口（cities 列表、各城市状态）

---

## 六、界面设计规范

### 6.1 整体风格

- **配色**：深色主题，主背景 `#030712`（gray-950），卡片背景 `#111827`（gray-900）
- **字体**：系统默认 sans-serif；数字/代码使用等宽字体（monospace）
- **圆角**：卡片 `rounded-xl`（12px）；按钮 `rounded-lg`（8px）
- **边框**：卡片默认 `border-gray-800`；状态色边框根据推流状态动态变化

### 6.2 状态色规范

| 状态 | 颜色 | 动效 |
|------|------|------|
| 空闲 idle | 灰色 gray-500 | 无 |
| 等待开播 | 蓝色 blue-400 | pulse（脉冲） |
| 预热中 | 黄色 yellow-400 | pulse |
| 推流中 | 绿色 green-400 | 无 |
| 异常 | 红色 red-400 | pulse |
| 熔断 | 红色 red-500 | ping（扩散） |

### 6.3 页面布局

**登录页**：居中卡片，最大宽度 400px，白色文字/表单，深色背景。

**全省大盘**：
```
┌─────────────────────────────────────────────────────┐
│  顶部导航栏（sticky）：Logo + 大盘标题 + 退出按钮      │
├─────────────────────────────────────────────────────┤
│  Tab 切换：全省大盘 | 直播源管理 | 用户管理            │
├─────────────────────────────────────────────────────┤
│  带宽监控行：↑ X.X Mbps  ↓ X.X Mbps                 │
├─────────────────────────────────────────────────────┤
│  统计卡片（4列）：推流中 | 预热中 | 异常/熔断 | 空闲    │
├─────────────────────────────────────────────────────┤
│  地市卡片网格（响应式 2-5列）                         │
│  ┌──────────┐ ┌──────────┐ ┌──────────┐ ...        │
│  │ 南京     │ │ 苏州  🟢 │ │ 无锡     │            │
│  │ ● 空闲   │ │ 推流中   │ │ ● 空闲   │            │
│  │          │ │ 00:45:23 │ │          │            │
│  └──────────┘ └──────────┘ └──────────┘            │
└─────────────────────────────────────────────────────┘
```

**地市操控台**：
```
┌────────────────────────────────────┐
│  顶栏：← 返回大盘 | 苏超直播中台 泰州管理台 │
├────────────────────────────────────┤
│  推流配置卡片（可折叠）              │
│  ┌──────────────────────────────┐  │
│  │ 🔒 推流密钥已就绪  [修改]     │  │
│  └──────────────────────────────┘  │
├────────────────────────────────────┤
│  推流控制卡片                       │
│  ┌──────────────────────────────┐  │
│  │  状态：● 推流中               │  │
│  │  苏超流A-泰州vs南京   01:23:45│  │
│  │  [🔊 静音]  [⏹ 停止推流]    │  │
│  └──────────────────────────────┘  │
├────────────────────────────────────┤
│  告警记录卡片                       │
│  [刷新] [清除告警]                  │
│  19:32 warn  注入进程退出(重试1/3)  │
└────────────────────────────────────┘
```

### 6.4 组件规范

**主要 CSS 类**（基于 Tailwind，需自定义）：
- `.card`：`bg-gray-900 border border-gray-800 rounded-xl p-4`
- `.btn-primary`：`bg-blue-600 hover:bg-blue-500 text-white px-4 py-2 rounded-lg text-sm font-medium`
- `.btn-ghost`：`text-gray-400 hover:text-white px-3 py-1.5 rounded-lg text-sm transition-colors`
- `.input`：`w-full bg-gray-800 border border-gray-700 rounded-lg px-3 py-2 text-sm text-white focus:border-blue-500 focus:outline-none`
- `.label`：`block text-xs text-gray-400 mb-1.5`
- `.badge`：`text-xs px-2 py-0.5 rounded-full font-medium`

---

## 七、接口汇总

### 公开接口（无需鉴权）
| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/api/auth/login` | 登录，返回 JWT |
| GET | `/health` | 健康检查 |

### 认证接口（需 Bearer JWT）
| 方法 | 路径 | 权限 | 说明 |
|------|------|------|------|
| GET | `/api/cities` | 全部 | 城市列表 |
| GET | `/api/cities/:cityId/status` | 全部 | 城市推流状态 |
| GET | `/api/stream-sources` | 全部 | 直播源列表 |
| POST | `/api/stream-sources` | 超管 | 新建直播源 |
| PUT | `/api/stream-sources/:id` | 超管 | 更新直播源 |
| DELETE | `/api/stream-sources/:id` | 超管 | 删除直播源 |
| GET | `/api/stream-sources/template` | 超管 | 下载 CSV 模板 |
| POST | `/api/stream-sources/import` | 超管 | CSV 批量导入 |
| GET | `/api/users` | 超管 | 用户列表 |
| POST | `/api/users` | 超管 | 创建用户 |
| DELETE | `/api/users/:userId` | 超管 | 删除用户 |
| PUT | `/api/users/:userId/password` | 超管 | 修改用户密码 |
| GET | `/api/cities/:cityId/stream-config` | 地市+ | 获取推流配置 |
| PUT | `/api/cities/:cityId/stream-config` | 地市+ | 保存推流配置 |
| POST | `/api/cities/:cityId/ffmpeg/direct-push` | 地市+ | 开始推流 |
| POST | `/api/cities/:cityId/ffmpeg/reset` | 地市+ | 停止推流 |
| POST | `/api/cities/:cityId/ffmpeg/mute` | 地市+ | 静音切换 |
| GET | `/api/cities/:cityId/alerts` | 地市+ | 告警列表 |
| POST | `/api/cities/:cityId/alerts/clear` | 地市+ | 清除告警 |

> "地市+" 表示地市管理员只能操作自己城市，超管可操作所有城市。

---

## 八、部署要求

- **运行环境**：任意支持 Docker Compose 的 Linux 系统（推荐 x86_64）
- **依赖**：Docker 20.10+，Docker Compose v2
- **FFmpeg**：需在 backend 容器内可执行（Dockerfile 中 `apt install ffmpeg`）
- **持久化目录**：
  - `./data/db/` — SQLite 数据库
  - `./data/uploads/` — 视频上传暂存（按需，当前版本暂未使用）
  - `./data/transcoded/` — 转码输出（按需）
- **环境变量**（`.env` 文件）：

```
APP_PORT=8080
APP_ENV=production
JWT_SECRET=<随机字符串>
ADMIN_INITIAL_PASSWORD=<初始超管密码>
DB_PATH=/app/data/db/susuper.db
SRS_HOST=srs
SRS_RTMP_PORT=1935
SRS_API_PORT=1985
FFMPEG_BIN=/usr/bin/ffmpeg
FFMPEG_MAX_RETRIES=3
FFMPEG_RETRY_DELAY_SECONDS=3
```

---

*文档结束*
