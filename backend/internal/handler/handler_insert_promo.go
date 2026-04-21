package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// insertPromo POST /cities/:cityId/ffmpeg/insert-promo
// Body: { "promoVideoId": 123, "loop": false }
//
// 在正在推流的城市中临时插播宣传片。
// loop=false：播放一次后自动切回直播流；loop=true：循环播放直到手动停止。
func (h *Handler) insertPromo(c *gin.Context) {
	cityID, err := cityIDFromParam(c)
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid city id")
		return
	}
	if !assertCityAccess(c, cityID) {
		return
	}

	var req struct {
		PromoVideoID int64 `json:"promoVideoId"`
		Loop         bool  `json:"loop"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.PromoVideoID == 0 {
		fail(c, http.StatusBadRequest, "promoVideoId required")
		return
	}

	if err := h.ffmpeg.StartPromoInsert(cityID, req.PromoVideoID, req.Loop); err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}

	ok(c, gin.H{"inserting": true, "loop": req.Loop})
}

// stopPromo POST /cities/:cityId/ffmpeg/stop-promo
// 手动停止循环插播，恢复直播流
func (h *Handler) stopPromo(c *gin.Context) {
	cityID, err := cityIDFromParam(c)
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid city id")
		return
	}
	if !assertCityAccess(c, cityID) {
		return
	}

	if err := h.ffmpeg.StopPromoInsert(cityID); err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}

	ok(c, gin.H{"stopped": true})
}
