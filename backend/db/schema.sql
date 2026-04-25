-- =============================================================
-- 苏超联赛全省直播分发中台 - SQLite Schema
-- =============================================================

PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;

-- -------------------------------------------------------------
-- 城市表（13 个地市）
-- -------------------------------------------------------------
CREATE TABLE IF NOT EXISTS cities (
    id   INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT    NOT NULL UNIQUE,   -- e.g. "泰州"
    code TEXT    NOT NULL UNIQUE    -- e.g. "tz"
);

-- 预置 13 个苏超参赛城市
INSERT OR IGNORE INTO cities (name, code) VALUES
    ('南京', 'nj'), ('苏州', 'sz'), ('无锡', 'wx'), ('常州', 'cz'),
    ('南通', 'nt'), ('扬州', 'yz'), ('镇江', 'zj'), ('泰州', 'tz'),
    ('盐城', 'yc'), ('连云港', 'lyg'), ('淮安', 'ha'),
    ('宿迁', 'sq'), ('徐州', 'xz');

-- -------------------------------------------------------------
-- 用户表（超管 + 地市管理员）
-- -------------------------------------------------------------
CREATE TABLE IF NOT EXISTS users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT    NOT NULL UNIQUE,
    password_hash TEXT    NOT NULL,
    role          TEXT    NOT NULL CHECK (role IN ('super_admin', 'city_admin')),
    city_id       INTEGER REFERENCES cities (id) ON DELETE SET NULL,  -- city_admin 专属
    phone         TEXT,           -- 接收短信告警的手机号
    created_at    DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- 默认超管账号（密码 "admin123" 的 bcrypt 占位，启动时由后端覆盖）
INSERT OR IGNORE INTO users (username, password_hash, role)
VALUES ('admin', '$2a$12$placeholder', 'super_admin');

-- -------------------------------------------------------------
-- 上游直播源（全省共享，超管维护）
-- -------------------------------------------------------------
CREATE TABLE IF NOT EXISTS stream_sources (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    name           TEXT    NOT NULL,          -- e.g. "泰州 vs 南京"
    url            TEXT    NOT NULL,          -- e.g. rtmp://jstv.example.com/live/streamA
    match_datetime TEXT,                      -- e.g. "2025-06-01 19:30"
    round          TEXT,                      -- e.g. "第3轮"
    channel        TEXT,                      -- e.g. "JSTV-2"
    remark         TEXT,
    is_active      INTEGER NOT NULL DEFAULT 1,
    created_at     DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- -------------------------------------------------------------
-- 宣传片资产（各地市上传，离线转码后才可用于排期）
-- -------------------------------------------------------------
CREATE TABLE IF NOT EXISTS promotional_videos (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    city_id            INTEGER NOT NULL REFERENCES cities (id) ON DELETE CASCADE,
    original_filename  TEXT    NOT NULL,
    stored_filename    TEXT    NOT NULL UNIQUE, -- UUID 文件名，防冲突
    upload_path        TEXT    NOT NULL,        -- 原始上传路径
    transcoded_path    TEXT,                   -- 标准化转码后路径（NULL = 未完成）
    thumbnail_path     TEXT,                   -- 封面截图路径（转码后第1秒截图）
    display_name       TEXT,                   -- 管理员自定义显示名（NULL 时用 original_filename）
    transcode_status   TEXT    NOT NULL DEFAULT 'pending'
                           CHECK (transcode_status IN ('pending', 'processing', 'done', 'failed')),
    transcode_error    TEXT,                   -- 失败原因
    duration_seconds   INTEGER,                -- 转码完成后填入
    created_at         DATETIME DEFAULT CURRENT_TIMESTAMP,
    created_by         INTEGER  REFERENCES users (id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS idx_promo_city ON promotional_videos (city_id);
CREATE INDEX IF NOT EXISTS idx_promo_status ON promotional_videos (transcode_status);

-- -------------------------------------------------------------
-- 地市推流配置（每个地市一条记录，记忆上次填写的配置）
-- -------------------------------------------------------------
CREATE TABLE IF NOT EXISTS stream_configs (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    city_id      INTEGER NOT NULL UNIQUE REFERENCES cities (id) ON DELETE CASCADE,
    push_url     TEXT,                   -- 视频号推流地址
    push_key     TEXT,                   -- 视频号推流密钥
    volume_gain  REAL    NOT NULL DEFAULT 1.0
                     CHECK (volume_gain >= 1.0 AND volume_gain <= 2.0),
    srs_app      TEXT    NOT NULL DEFAULT 'live',
    srs_stream   TEXT    NOT NULL DEFAULT 'stream', -- SRS 挂载点，每城市唯一
    updated_at   DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- 自动为每个地市建立默认推流配置（srs_stream 用 code 区分）
INSERT OR IGNORE INTO stream_configs (city_id, srs_stream)
SELECT id, code FROM cities;

-- -------------------------------------------------------------
-- 排期主表（每个地市每天一条）
-- -------------------------------------------------------------
CREATE TABLE IF NOT EXISTS schedules (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    city_id    INTEGER NOT NULL REFERENCES cities (id) ON DELETE CASCADE,
    date       DATE    NOT NULL,
    status     TEXT    NOT NULL DEFAULT 'draft'
                   CHECK (status IN ('draft', 'running', 'stopped', 'finished')),
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (city_id, date)
);

CREATE INDEX IF NOT EXISTS idx_schedule_city_date ON schedules (city_id, date);

-- -------------------------------------------------------------
-- 排期条目（时间轴上的每一段）
-- -------------------------------------------------------------
CREATE TABLE IF NOT EXISTS schedule_items (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    schedule_id      INTEGER NOT NULL REFERENCES schedules (id) ON DELETE CASCADE,
    order_index      INTEGER NOT NULL,             -- 执行顺序（从 0 开始）
    item_type        TEXT    NOT NULL
                         CHECK (item_type IN ('promo_video', 'live_stream')),
    -- promo_video 专属
    promo_video_id   INTEGER REFERENCES promotional_videos (id) ON DELETE SET NULL,
    loop_count       INTEGER NOT NULL DEFAULT -1,  -- -1 = 无限循环，直到下一项时间到
    -- live_stream 专属
    stream_source_id INTEGER REFERENCES stream_sources (id) ON DELETE SET NULL,
    -- 共同字段
    start_time       TEXT    NOT NULL,             -- "HH:MM"，当天的计划开始时间
    UNIQUE (schedule_id, order_index)
);

CREATE INDEX IF NOT EXISTS idx_item_schedule ON schedule_items (schedule_id);

-- -------------------------------------------------------------
-- FFmpeg 进程追踪（每个地市最多一条活跃记录）
-- -------------------------------------------------------------
CREATE TABLE IF NOT EXISTS ffmpeg_processes (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    city_id         INTEGER NOT NULL UNIQUE REFERENCES cities (id) ON DELETE CASCADE,
    schedule_id     INTEGER REFERENCES schedules (id) ON DELETE SET NULL,
    -- 推流进程（SRS → 视频号）
    push_pid        INTEGER,
    -- 注入进程（source → SRS）
    inject_pid      INTEGER,
    status          TEXT    NOT NULL DEFAULT 'idle'
                        CHECK (status IN ('idle', 'warming', 'streaming', 'failed', 'breaker_open')),
    current_item_id INTEGER REFERENCES schedule_items (id) ON DELETE SET NULL,
    retry_count     INTEGER NOT NULL DEFAULT 0,
    last_started_at DATETIME,
    last_failed_at  DATETIME,
    updated_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
    promo_inserting INTEGER NOT NULL DEFAULT 0,  -- 0/1，是否正在插播宣传片
    direct_source_name TEXT                      -- 直推模式下的直播源名称
);

-- 每个地市预置一条 idle 记录
INSERT OR IGNORE INTO ffmpeg_processes (city_id, status)
SELECT id, 'idle' FROM cities;

-- -------------------------------------------------------------
-- 告警日志
-- -------------------------------------------------------------
CREATE TABLE IF NOT EXISTS alert_logs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    city_id     INTEGER NOT NULL REFERENCES cities (id) ON DELETE CASCADE,
    level       TEXT    NOT NULL CHECK (level IN ('warn', 'error', 'critical')),
    message     TEXT    NOT NULL,
    sms_sent    INTEGER NOT NULL DEFAULT 0,  -- 0/1
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_alert_city ON alert_logs (city_id, created_at DESC);

-- -------------------------------------------------------------
-- 操作审计日志（登录、流控、用户管理等关键操作）
-- -------------------------------------------------------------
CREATE TABLE IF NOT EXISTS audit_logs (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id    INTEGER,                        -- 操作者 ID（登录失败时可能为 NULL）
    username   TEXT    NOT NULL,               -- 操作者用户名
    role       TEXT    NOT NULL DEFAULT '',    -- 操作者角色
    action     TEXT    NOT NULL,               -- 操作类型，如 LOGIN / LOGIN_FAIL / START_STREAM
    detail     TEXT    NOT NULL DEFAULT '',    -- 附加说明
    ip         TEXT    NOT NULL DEFAULT '',    -- 客户端 IP
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_audit_created ON audit_logs (created_at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_user ON audit_logs (username);
