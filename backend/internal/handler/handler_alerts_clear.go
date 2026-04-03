package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// clearAlerts POST /cities/:cityId/alerts/clear
//
// 停止 ffmpeg 进程（含 cancel 上下文，彻底杀死 watchdog goroutine）
// → 重置 DB 状态为 idle
// → 删除该城市所有告警日志（UI 清零）
//
// 并发安全：StopCity 先 cancel context，watchdog 在 ctx.Done() 处立即退出，
// killProc 等待进程结束（最多 3s），返回后再写 DB，不存在竞争。
func (h *Handler) clearAlerts(c *gin.Context) {
	cityID, err := cityIDFromParam(c)
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid city id")
		return
	}
	if !assertCityAccess(c, cityID) {
		return
	}

	// 1. 彻底停止进程（cancel ctx + kill + wait）
	h.ffmpeg.StopCity(cityID)

	// 2. 重置 DB 状态（此时 watchdog 已退出，无竞争）
	h.db.Exec(
		`UPDATE ffmpeg_processes
		    SET status='idle', retry_count=0, current_item_id=NULL,
		        push_pid=NULL, inject_pid=NULL, last_started_at=NULL,
		        direct_source_name=NULL
		  WHERE city_id=?`, cityID)

	// 3. 清除告警日志（UI 清零，历史仍可在 DB 中恢复）
	h.db.Exec(`DELETE FROM alert_logs WHERE city_id=?`, cityID)

	ok(c, gin.H{"cleared": true})
}
