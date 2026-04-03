# 苏超联赛直播分发中台 — 快速启动

## 一键启动

```bash
# 1. 复制并填写环境变量
cp .env.example .env
# 编辑 .env，至少填写：JWT_SECRET / SMS_API_URL（可选）/ ADMIN_INITIAL_PASSWORD

# 2. 启动全部容器
docker compose up -d

# 3. 查看日志
docker compose logs -f backend
docker compose logs -f srs
```

访问地址：
- 前端：http://localhost:3000
- 后端 API：http://localhost:8080
- SRS API：http://localhost:1985/api/v1/versions

默认超管账号：`admin` / `.env` 中 `ADMIN_INITIAL_PASSWORD` 的值

---

## 验证流程

### Step 1：验证 SRS
```bash
bash scripts/test-srs-health.sh
```

### Step 2：测试宣传片注入
```bash
# 先上传一个宣传片并等待转码完成，然后：
bash scripts/test-inject-promo.sh data/transcoded/1/xxxx_std.mp4 tz
```

### Step 3：测试直播流注入
```bash
bash scripts/test-inject-live.sh rtmp://jstv.example.com/live/streamA 1.5 tz
```

### Step 4：全链路冒烟测试（含切流）
```bash
bash scripts/test-full-pipeline.sh \
  data/transcoded/1/promo_std.mp4 \
  rtmp://jstv.example.com/live/streamA
```

### 查看所有 FFmpeg 命令
```bash
bash scripts/gen-ffmpeg-commands.sh
# 自定义参数：
CITY_CODE=nj VOLUME_GAIN=1.8 bash scripts/gen-ffmpeg-commands.sh
```

---

## 目录结构

```
susuper/
├── docker-compose.yml     一键启动
├── .env.example           环境变量模板
├── srs/conf/srs.conf      SRS 流媒体配置
├── backend/               Go 后端
│   ├── pkg/ffscript/      FFmpeg 命令生成器（参数权威来源）
│   ├── pkg/srsapi/        SRS HTTP API 客户端
│   └── internal/service/  核心业务逻辑
├── frontend/              Vue 3 前端
└── scripts/               运维测试脚本
    ├── test-srs-health.sh
    ├── test-inject-promo.sh
    ├── test-inject-live.sh
    ├── test-full-pipeline.sh
    └── gen-ffmpeg-commands.sh
```
