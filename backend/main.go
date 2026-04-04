package main

import (
	"database/sql"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"

	"susuper/internal/config"
	"susuper/internal/db"
	"susuper/internal/handler"
	"susuper/internal/service"
)

func main() {
	// 1. 加载配置
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	// 2. 打开 SQLite 数据库（含 schema 初始化）
	database, err := db.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer database.Close()

	// 3. 确保超管密码已初始化
	if err := ensureAdminPassword(database, cfg.AdminInitialPassword); err != nil {
		log.Fatalf("init admin password: %v", err)
	}

	// 3a. 迁移：补充各列（已存在时忽略报错）
	database.Exec(`ALTER TABLE ffmpeg_processes ADD COLUMN direct_source_name TEXT`)
	database.Exec(`ALTER TABLE stream_configs ADD COLUMN config_locked INTEGER NOT NULL DEFAULT 0`)

	// 3b. 迁移：users 表支持 observer 角色（重建 CHECK 约束）
	var usersSchema string
	database.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='users'`).Scan(&usersSchema)
	if !strings.Contains(usersSchema, "'observer'") {
		database.Exec(`ALTER TABLE users RENAME TO users_pre_observer`)
		database.Exec(`CREATE TABLE users (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			username      TEXT    NOT NULL UNIQUE,
			password_hash TEXT    NOT NULL,
			role          TEXT    NOT NULL CHECK (role IN ('super_admin','city_admin','observer')),
			city_id       INTEGER REFERENCES cities(id) ON DELETE SET NULL,
			phone         TEXT,
			created_at    DATETIME DEFAULT CURRENT_TIMESTAMP
		)`)
		database.Exec(`INSERT INTO users SELECT * FROM users_pre_observer`)
		database.Exec(`DROP TABLE users_pre_observer`)
		log.Printf("INFO: migrated users table to support observer role")
	}

	// 3b. 初始化固定频道（幂等）
	if err := initFixedChannels(database, cfg.AdminInitialPassword); err != nil {
		log.Printf("WARN: init fixed channels: %v", err)
	}

	// 3c. 清理残留的推流状态（进程重启后 ffmpeg 已终止，DB 仍显示 streaming）
	if _, err := database.Exec(
		`UPDATE ffmpeg_processes SET status='idle', retry_count=0,
		        current_item_id=NULL, push_pid=NULL, inject_pid=NULL
		  WHERE status IN ('streaming','warming','failed')`,
	); err != nil {
		log.Printf("WARN: cleanup stale ffmpeg status: %v", err)
	}
	// 同步将对应的排期状态改回 stopped，允许用户重新启动
	if _, err := database.Exec(
		`UPDATE schedules SET status='stopped'
		  WHERE status='running'
		    AND id IN (SELECT schedule_id FROM ffmpeg_processes WHERE schedule_id IS NOT NULL)`,
	); err != nil {
		log.Printf("WARN: cleanup stale schedule status: %v", err)
	}

	// 4. 创建 Service 层（注意依赖顺序）
	smsSvc := service.NewSMSService(database, cfg)
	ffmpegSvc := service.NewFFmpegService(database, cfg, smsSvc)
	transcodeSvc := service.NewTranscodeService(database, cfg)
	schedulerSvc := service.NewSchedulerService(database, ffmpegSvc)

	// 4a. 每日 05:00 自动清空推流密钥（强制管理员每天重新填报）
	service.StartDailyKeyReset(database, log.Default())

	// 5. 创建 HTTP 路由
	if cfg.AppEnv == "production" {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())

	// 允许前端跨域（开发时前端 vite 在 :5173）
	r.Use(corsMiddleware())

	h := handler.New(database, cfg, transcodeSvc, ffmpegSvc, schedulerSvc)
	h.RegisterRoutes(r)

	addr := ":" + cfg.AppPort
	log.Printf("🚀 susuper backend starting on %s (env=%s)", addr, cfg.AppEnv)
	if err := r.Run(addr); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
}

// ensureAdminPassword 若超管仍使用占位密码，则用初始密码覆盖
func ensureAdminPassword(database *sql.DB, initialPassword string) error {
	// schema.sql 中已用 INSERT OR IGNORE 插入占位超管
	// 此处检查是否仍是占位 hash，若是则用初始密码覆盖
	var hash string
	row := database.QueryRow(`SELECT password_hash FROM users WHERE username='admin'`)
	if err := row.Scan(&hash); err != nil {
		return nil // 超管不存在，schema 会处理
	}
	if hash == "$2a$12$placeholder" {
		hashed, err := bcrypt.GenerateFromPassword([]byte(initialPassword), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		_, err = database.Exec(`UPDATE users SET password_hash=? WHERE username='admin'`, string(hashed))
		return err
	}
	return nil
}

// initFixedChannels 幂等地插入江苏移动、会员任我选两个固定频道及其管理员账号
func initFixedChannels(database *sql.DB, password string) error {
	// 1. 城市记录
	database.Exec(`INSERT OR IGNORE INTO cities (name, code) VALUES ('江苏移动','jsyd'),('会员任我选','vips')`)
	// 2. 推流配置（srs_stream 用 code 区分）
	database.Exec(`INSERT OR IGNORE INTO stream_configs (city_id, srs_stream) SELECT id, code FROM cities WHERE code IN ('jsyd','vips')`)
	// 3. ffmpeg 进程状态记录
	database.Exec(`INSERT OR IGNORE INTO ffmpeg_processes (city_id, status) SELECT id, 'idle' FROM cities WHERE code IN ('jsyd','vips')`)
	// 4. 管理员账号（使用与超管相同的初始密码）
	hashed, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	database.Exec(
		`INSERT OR IGNORE INTO users (username, password_hash, role, city_id)
		 SELECT 'admin_jsyd', ?, 'city_admin', id FROM cities WHERE code='jsyd'`,
		string(hashed))
	database.Exec(
		`INSERT OR IGNORE INTO users (username, password_hash, role, city_id)
		 SELECT 'admin_vips', ?, 'city_admin', id FROM cities WHERE code='vips'`,
		string(hashed))
	return nil
}

func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Authorization,Content-Type")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
