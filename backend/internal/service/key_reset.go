package service

import (
	"database/sql"
	"log"
	"time"
)

// StartDailyKeyReset 在每天北京时间 05:00 清空所有城市推流密钥
// 同时将 config_locked 重置为 0，强制管理员当日重新填报并锁定配置
func StartDailyKeyReset(db *sql.DB, logger *log.Logger) {
	go func() {
		loc := time.FixedZone("CST", 8*3600)
		for {
			now := time.Now().In(loc)
			next := time.Date(now.Year(), now.Month(), now.Day(), 5, 0, 0, 0, loc)
			if !next.After(now) {
				next = next.Add(24 * time.Hour)
			}
			logger.Printf("[key-reset] next reset scheduled at %s", next.Format("2006-01-02 15:04:05 MST"))
			time.Sleep(time.Until(next))

			_, err := db.Exec(`UPDATE stream_configs SET push_key = NULL, config_locked = 0`)
			if err != nil {
				logger.Printf("[key-reset] ERROR clearing push keys: %v", err)
			} else {
				logger.Printf("[key-reset] push_key cleared for all cities at %s", time.Now().In(loc).Format("2006-01-02 15:04:05"))
			}
		}
	}()
}
