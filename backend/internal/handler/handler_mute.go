package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// setMute POST /cities/:cityId/ffmpeg/mute
// Body: {"muted": true/false}
//
// 切换静音：以 gain=0（静音）或原始 gain 重启 inject 进程，push 进程不断流。
func (h *Handler) setMute(c *gin.Context) {
	cityID, err := cityIDFromParam(c)
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid city id")
		return
	}
	if !assertCityAccess(c, cityID) {
		return
	}

	var req struct {
		Muted bool `json:"muted"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid body")
		return
	}

	if err := h.ffmpeg.SetMute(cityID, req.Muted); err != nil {
		fail(c, http.StatusInternalServerError, err.Error())
		return
	}

	ok(c, gin.H{"muted": req.Muted})
}
