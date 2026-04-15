package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// insertPromo POST /cities/:cityId/ffmpeg/insert-promo
// Body: { "promoVideoId": 123 }
//
// 在正在推流的城市中临时插播宣传片（一次播放后自动切回直播流）。
// 要求：当前城市处于 streaming 状态，且宣传片已完成转码。
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
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.PromoVideoID == 0 {
		fail(c, http.StatusBadRequest, "promoVideoId required")
		return
	}

	if err := h.ffmpeg.StartPromoInsert(cityID, req.PromoVideoID); err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}

	ok(c, gin.H{"inserting": true})
}
