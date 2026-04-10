package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"susuper/internal/model"
)

// directPush POST /cities/:cityId/ffmpeg/direct-push
// Body: { "streamSourceId": 123 }
//
// 无需排期，直接将指定直播源推流到视频号。
// 停止旧进程 → 立��以该直播源启动新进程。
func (h *Handler) directPush(c *gin.Context) {
	cityID, err := cityIDFromParam(c)
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid city id")
		return
	}
	if !assertCityAccess(c, cityID) {
		return
	}

	var req struct {
		StreamSourceID int64 `json:"streamSourceId"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.StreamSourceID == 0 {
		fail(c, http.StatusBadRequest, "streamSourceId required")
		return
	}

	// 加载直播源
	var sourceURL, sourceName string
	if err := h.db.QueryRow(
		`SELECT url, name FROM stream_sources WHERE id=? AND is_active=1`, req.StreamSourceID,
	).Scan(&sourceURL, &sourceName); err != nil {
		fail(c, http.StatusNotFound, "stream source not found or inactive")
		return
	}

	// 检查配置是否已锁定
	var locked int
	h.db.QueryRow(`SELECT config_locked FROM stream_configs WHERE city_id=?`, cityID).Scan(&locked)
	if locked == 0 {
		fail(c, http.StatusBadRequest, "推流配置未锁定，请先保存推流密钥")
		return
	}

	// 带宽保护：上行带宽 ≥ 80 Mbps 时拒绝新推流（泰州特权豁免）
	var cityCode string
	h.db.QueryRow(`SELECT code FROM cities WHERE id=?`, cityID).Scan(&cityCode)
	if cityCode != "tz" {
		globalNetStats.mu.RLock()
		uploadMbps := globalNetStats.UploadMbps
		globalNetStats.mu.RUnlock()
		if uploadMbps >= 80.0 {
			fail(c, http.StatusServiceUnavailable, "SERVER_BANDWIDTH_FULL")
			return
		}
	}

	// 停止可能存在的旧进程
	h.ffmpeg.StopCity(cityID)

	// 构造合成 ScheduleItem（无排期 ID）
	item := &model.ScheduleItem{
		ItemType:     "live_stream",
		StreamSource: &model.StreamSource{URL: sourceURL},
	}

	if err := h.ffmpeg.StartCity(cityID, 0, item); err != nil {
		fail(c, http.StatusInternalServerError, err.Error())
		return
	}

	// 将直播源名称写入 DB，供 getCityStatus 的 currentItemName 展示
	h.db.Exec(`UPDATE ffmpeg_processes SET direct_source_name=? WHERE city_id=?`, sourceName, cityID)

	ok(c, gin.H{"started": true, "sourceName": sourceName})
}
