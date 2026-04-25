package handler

import (
	"bytes"
	"database/sql"
	"encoding/csv"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"

	"susuper/internal/config"
	"susuper/internal/middleware"
	"susuper/internal/model"
	"susuper/internal/service"
)

// Handler 聚合所有路由所需的依赖
type Handler struct {
	db        *sql.DB
	cfg       *config.Config
	transcode *service.TranscodeService
	ffmpeg    *service.FFmpegService
	scheduler *service.SchedulerService
}

func New(
	db *sql.DB,
	cfg *config.Config,
	transcode *service.TranscodeService,
	ffmpeg *service.FFmpegService,
	scheduler *service.SchedulerService,
) *Handler {
	return &Handler{db: db, cfg: cfg, transcode: transcode, ffmpeg: ffmpeg, scheduler: scheduler}
}

// RegisterRoutes 注册所有路由
func (h *Handler) RegisterRoutes(r *gin.Engine) {
	r.GET("/health", h.health)

	// SRS 回调（无鉴权，内网调用）
	hooks := r.Group("/hooks/srs")
	hooks.POST("/publish", h.srsPublish)
	hooks.POST("/unpublish", h.srsUnpublish)

	api := r.Group("/api")

	// 公开接口
	api.POST("/auth/login", h.login)
	api.GET("/system/metrics", h.getSystemMetrics)

	// 需要登录
	authed := api.Group("", middleware.Auth(h.cfg.JWTSecret))
	authed.GET("/cities", h.listCities)
	authed.GET("/cities/:cityId/status", h.getCityStatus)
	authed.GET("/stream-sources", h.listStreamSources)

	// 超管专属
	superOnly := authed.Group("", middleware.RequireSuperAdmin())
	superOnly.GET("/users", h.listUsers)
	superOnly.POST("/users", h.createUser)
	superOnly.DELETE("/users/:userId", h.deleteUser)
	superOnly.PUT("/users/:userId/password", h.changePassword)
	superOnly.GET("/stream-sources/template", h.downloadSourceTemplate)
	superOnly.POST("/stream-sources/import", h.importStreamSources)
	superOnly.POST("/stream-sources", h.createStreamSource)
	superOnly.PUT("/stream-sources/:id", h.updateStreamSource)
	superOnly.DELETE("/stream-sources/:id", h.deleteStreamSource)
	superOnly.GET("/audit-logs", h.listAuditLogs)

	// 地市管理员（city_admin 只能操作自己城市，super_admin 可操作所有）
	cityRoutes := authed.Group("/cities/:cityId")
	cityRoutes.GET("/videos", h.listVideos)
	cityRoutes.POST("/videos/upload", h.uploadVideo)
	cityRoutes.GET("/videos/:videoId/thumbnail", h.thumbnailVideo)
	cityRoutes.PUT("/videos/:videoId", h.updateVideo)
	cityRoutes.DELETE("/videos/:videoId", h.deleteVideo)
	cityRoutes.POST("/videos/:videoId/retranscode", h.retranscodeVideo)
	cityRoutes.GET("/stream-config", h.getStreamConfig)
	cityRoutes.PUT("/stream-config", h.updateStreamConfig)
	cityRoutes.GET("/schedules", h.listSchedules)
	cityRoutes.POST("/schedules", h.createSchedule)
	cityRoutes.PUT("/schedules/:scheduleId/items", h.updateScheduleItems)
	cityRoutes.POST("/schedules/:scheduleId/start", h.startSchedule)
	cityRoutes.POST("/schedules/:scheduleId/stop", h.stopSchedule)
	cityRoutes.POST("/schedules/:scheduleId/advance", h.advanceSchedule)
	cityRoutes.DELETE("/schedules/:scheduleId", h.deleteSchedule)
	cityRoutes.GET("/alerts", h.listAlerts)
	cityRoutes.POST("/ffmpeg/reset", h.resetFFmpeg)
	cityRoutes.POST("/ffmpeg/direct-push", h.directPush)
	cityRoutes.POST("/ffmpeg/mute", h.setMute)
	cityRoutes.POST("/ffmpeg/insert-promo", h.insertPromo)
	cityRoutes.POST("/ffmpeg/stop-promo", h.stopPromo)
	cityRoutes.POST("/alerts/clear", h.clearAlerts)
}

// ── 通用辅助 ────────────────────────────────────────────────────

func ok(c *gin.Context, data interface{}) {
	c.JSON(http.StatusOK, gin.H{"data": data})
}

func fail(c *gin.Context, code int, msg string) {
	c.JSON(code, gin.H{"error": msg})
}

func cityIDFromParam(c *gin.Context) (int64, error) {
	return strconv.ParseInt(c.Param("cityId"), 10, 64)
}

func jwtCityID(c *gin.Context) *int64 {
	v, _ := c.Get("cityID")
	if v == nil {
		return nil
	}
	id, _ := v.(*int64)
	return id
}

func isAdmin(c *gin.Context) bool {
	role, _ := c.Get("role")
	return role == "super_admin"
}

// assertCityAccess 确保请求者有权访问 cityID（超管全通，地市管理员只能访问自己）
func assertCityAccess(c *gin.Context, cityID int64) bool {
	if isAdmin(c) {
		return true
	}
	myCity := jwtCityID(c)
	if myCity == nil || *myCity != cityID {
		fail(c, http.StatusForbidden, "access denied for this city")
		return false
	}
	return true
}

// writeAuditLog 写入操作审计日志（fire-and-forget，失败不影响主流程）
func (h *Handler) writeAuditLog(userID *int64, username, role, action, detail, ip string) {
	h.db.Exec( //nolint:errcheck
		`INSERT INTO audit_logs (user_id, username, role, action, detail, ip) VALUES (?,?,?,?,?,?)`,
		userID, username, role, action, detail, ip,
	)
}

// callerInfo 从 JWT context 取出操作者信息（用于非 login 路由）
func (h *Handler) callerInfo(c *gin.Context) (int64, string, string) {
	uid, _ := c.Get("userID")
	roleVal, _ := c.Get("role")
	userID := uid.(int64)
	var username string
	h.db.QueryRow(`SELECT username FROM users WHERE id=?`, userID).Scan(&username) //nolint:errcheck
	return userID, username, roleVal.(string)
}

// ── Health ──────────────────────────────────────────────────────

func (h *Handler) health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok", "ts": time.Now().Unix()})
}

// ── Auth ────────────────────────────────────────────────────────

type loginReq struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

func (h *Handler) login(c *gin.Context) {
	var req loginReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}

	var user model.User
	row := h.db.QueryRow(
		`SELECT id, username, password_hash, role, city_id FROM users WHERE username=?`, req.Username)
	if err := row.Scan(&user.ID, &user.Username, &user.PasswordHash, &user.Role, &user.CityID); err != nil {
		h.writeAuditLog(nil, req.Username, "", "LOGIN_FAIL", "用户不存在", c.ClientIP())
		fail(c, http.StatusUnauthorized, "invalid credentials")
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		h.writeAuditLog(&user.ID, user.Username, user.Role, "LOGIN_FAIL", "密码错误", c.ClientIP())
		fail(c, http.StatusUnauthorized, "invalid credentials")
		return
	}

	claims := &middleware.Claims{
		UserID: user.ID,
		Role:   user.Role,
		CityID: user.CityID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(24 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(h.cfg.JWTSecret))
	if err != nil {
		fail(c, http.StatusInternalServerError, "sign token failed")
		return
	}

	h.writeAuditLog(&user.ID, user.Username, user.Role, "LOGIN", "", c.ClientIP())
	ok(c, gin.H{"token": signed, "user": user})
}

// ── Cities ──────────────────────────────────────────────────────

func (h *Handler) listCities(c *gin.Context) {
	rows, err := h.db.Query(`SELECT id, name, code FROM cities ORDER BY id`)
	if err != nil {
		fail(c, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	var cities []model.City
	for rows.Next() {
		var city model.City
		if err := rows.Scan(&city.ID, &city.Name, &city.Code); err == nil {
			cities = append(cities, city)
		}
	}
	ok(c, cities)
}

func (h *Handler) getCityStatus(c *gin.Context) {
	cityID, err := cityIDFromParam(c)
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid cityId")
		return
	}
	if !assertCityAccess(c, cityID) {
		return
	}

	var fp model.FFmpegProcess
	row := h.db.QueryRow(
		`SELECT id, city_id, schedule_id, push_pid, inject_pid, status, current_item_id, retry_count,
		       strftime('%Y-%m-%d %H:%M:%S', last_started_at) as last_started_at, direct_source_name
		   FROM ffmpeg_processes WHERE city_id=?`, cityID)
	var lastStartedAt, directSourceName sql.NullString
	_ = row.Scan(&fp.ID, &fp.CityID, &fp.ScheduleID, &fp.PushPID, &fp.InjectPID,
		&fp.Status, &fp.CurrentItemID, &fp.RetryCount, &lastStartedAt, &directSourceName)
	// 若 DB 显示推流中但内存无 session（服务重启后遗留脏数据），自动修正为 idle
	if (fp.Status == "streaming" || fp.Status == "warming") && !h.ffmpeg.HasSession(cityID) {
		fp.Status = "idle"
		fp.CurrentItemID = nil
		h.db.Exec(`UPDATE ffmpeg_processes SET status='idle', current_item_id=NULL WHERE city_id=?`, cityID)
	}

	type enrichedStatus struct {
		model.FFmpegProcess
		CurrentItemName  *string `json:"currentItemName,omitempty"`
		TodayItemCount   int     `json:"todayItemCount"`
		SchedulerActive  bool    `json:"schedulerActive"`
		ScheduleStatus   string  `json:"scheduleStatus,omitempty"`
		CurrentItemIndex int     `json:"currentItemIndex"`
		NextItemName     *string `json:"nextItemName,omitempty"`
		NextItemTime     *string `json:"nextItemTime,omitempty"`
		LastStartedAt    *string `json:"lastStartedAt,omitempty"`
		PromoInserting      bool    `json:"promoInserting"`
		PromoLoop           bool    `json:"promoLoop"`
		PromoRemainingSecs  int     `json:"promoRemainingSecs"`
		PromoStartedAt      string  `json:"promoStartedAt,omitempty"`
		PromoVideoDuration  int     `json:"promoVideoDuration,omitempty"`
	}
	result := enrichedStatus{FFmpegProcess: fp}
	if lastStartedAt.Valid {
		result.LastStartedAt = &lastStartedAt.String
	}
	// 直推模式：优先使用 direct_source_name 作为 currentItemName
	if directSourceName.Valid && directSourceName.String != "" {
		result.CurrentItemName = &directSourceName.String
	}

	// 调度器 goroutine 是否在运行（含等待 start_time 阶段）
	result.SchedulerActive = h.scheduler.IsRunning(cityID)
	// 插播宣传片状态（倒计时）
	var promoStartedAt time.Time
	result.PromoInserting, result.PromoLoop, result.PromoRemainingSecs, promoStartedAt, result.PromoVideoDuration = h.ffmpeg.PromoStatus(cityID)
	if !promoStartedAt.IsZero() {
		result.PromoStartedAt = promoStartedAt.UTC().Format(time.RFC3339)
	}

	// 当前活动排期的状态
	if fp.ScheduleID != nil {
		var schStatus string
		h.db.QueryRow(`SELECT status FROM schedules WHERE id=?`, *fp.ScheduleID).Scan(&schStatus)
		result.ScheduleStatus = schStatus
	}

	// 查当前条目名称 + 排期进度 + 下一条目
	if fp.CurrentItemID != nil {
		var itemType string
		var promoVideoID, streamSourceID sql.NullInt64
		var orderIdx int
		h.db.QueryRow(
			`SELECT item_type, promo_video_id, stream_source_id, order_index FROM schedule_items WHERE id=?`,
			*fp.CurrentItemID,
		).Scan(&itemType, &promoVideoID, &streamSourceID, &orderIdx)

		var name string
		if itemType == "live_stream" && streamSourceID.Valid {
			h.db.QueryRow(`SELECT name FROM stream_sources WHERE id=?`, streamSourceID.Int64).Scan(&name)
		} else if itemType == "promo_video" && promoVideoID.Valid {
			h.db.QueryRow(
				`SELECT COALESCE(display_name, original_filename) FROM promotional_videos WHERE id=?`,
				promoVideoID.Int64,
			).Scan(&name)
		}
		if name != "" {
			result.CurrentItemName = &name
		}
		result.CurrentItemIndex = orderIdx + 1 // 1-based

		// 查下一条目（order_index + 1）
		if fp.ScheduleID != nil {
			var nextType, nextTime string
			var nextSrcID, nextVideoID sql.NullInt64
			h.db.QueryRow(
				`SELECT item_type, start_time, stream_source_id, promo_video_id
				   FROM schedule_items WHERE schedule_id=? AND order_index=?`,
				*fp.ScheduleID, orderIdx+1,
			).Scan(&nextType, &nextTime, &nextSrcID, &nextVideoID)
			if nextTime != "" {
				var nextName string
				if nextType == "live_stream" && nextSrcID.Valid {
					h.db.QueryRow(`SELECT name FROM stream_sources WHERE id=?`, nextSrcID.Int64).Scan(&nextName)
				} else if nextType == "promo_video" && nextVideoID.Valid {
					h.db.QueryRow(
						`SELECT COALESCE(display_name, original_filename) FROM promotional_videos WHERE id=?`,
						nextVideoID.Int64,
					).Scan(&nextName)
				}
				result.NextItemTime = &nextTime
				if nextName != "" {
					result.NextItemName = &nextName
				}
			}
		}
	}

	// 查今日排期条目数
	today := time.Now().Format("2006-01-02")
	h.db.QueryRow(
		`SELECT COUNT(*) FROM schedule_items si
		   JOIN schedules s ON si.schedule_id = s.id
		  WHERE s.city_id=? AND s.date=?`,
		cityID, today,
	).Scan(&result.TodayItemCount)

		ok(c, result)
}

// ── Users (super admin) ─────────────────────────────────────────

func (h *Handler) listUsers(c *gin.Context) {
	rows, err := h.db.Query(
		`SELECT id, username, role, city_id, phone, created_at FROM users ORDER BY id`)
	if err != nil {
		fail(c, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	var users []model.User
	for rows.Next() {
		var u model.User
		if err := rows.Scan(&u.ID, &u.Username, &u.Role, &u.CityID, &u.Phone, &u.CreatedAt); err == nil {
			users = append(users, u)
		}
	}
	ok(c, users)
}

type createUserReq struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required,min=8"`
	Role     string `json:"role" binding:"required,oneof=super_admin city_admin observer"`
	CityID   *int64 `json:"cityId"`
	Phone    string `json:"phone"`
}

func (h *Handler) createUser(c *gin.Context) {
	var req createUserReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	if req.Role == "city_admin" && req.CityID == nil {
		fail(c, http.StatusBadRequest, "cityId required for city_admin")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		fail(c, http.StatusInternalServerError, "hash password failed")
		return
	}

	var phone *string
	if req.Phone != "" {
		phone = &req.Phone
	}

	res, err := h.db.Exec(
		`INSERT INTO users (username, password_hash, role, city_id, phone) VALUES (?,?,?,?,?)`,
		req.Username, string(hash), req.Role, req.CityID, phone)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			fail(c, http.StatusConflict, "username already exists")
			return
		}
		fail(c, http.StatusInternalServerError, err.Error())
		return
	}

	id, _ := res.LastInsertId()
	callerUID, callerName, callerRole := h.callerInfo(c)
	h.writeAuditLog(&callerUID, callerName, callerRole, "CREATE_USER", "新用户: "+req.Username+"("+req.Role+")", c.ClientIP())
	ok(c, gin.H{"id": id})
}

// ── Stream Sources ──────────────────────────────────────────────

func (h *Handler) listStreamSources(c *gin.Context) {
	rows, err := h.db.Query(`SELECT id, name, url, match_datetime, round, channel, remark, is_active, created_at FROM stream_sources ORDER BY id`)
	if err != nil {
		fail(c, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	var sources []model.StreamSource
	for rows.Next() {
		var s model.StreamSource
		if err := rows.Scan(&s.ID, &s.Name, &s.URL, &s.MatchDatetime, &s.Round, &s.Channel, &s.Remark, &s.IsActive, &s.CreatedAt); err == nil {
			sources = append(sources, s)
		}
	}
	ok(c, sources)
}

type sourceReq struct {
	Name          string  `json:"name" binding:"required"`
	URL           string  `json:"url" binding:"required"`
	IsActive      *bool   `json:"isActive"`
	MatchDatetime *string `json:"matchDatetime"`
	Round         *string `json:"round"`
	Channel       *string `json:"channel"`
	Remark        *string `json:"remark"`
}

func (h *Handler) createStreamSource(c *gin.Context) {
	var req sourceReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	isActive := true
	if req.IsActive != nil {
		isActive = *req.IsActive
	}
	res, err := h.db.Exec(
		`INSERT INTO stream_sources (name, url, match_datetime, round, channel, remark, is_active) VALUES (?,?,?,?,?,?,?)`,
		req.Name, req.URL, req.MatchDatetime, req.Round, req.Channel, req.Remark, isActive)
	if err != nil {
		fail(c, http.StatusInternalServerError, err.Error())
		return
	}
	id, _ := res.LastInsertId()
	ok(c, gin.H{"id": id})
}

func (h *Handler) updateStreamSource(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	var req sourceReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	isActive := true
	if req.IsActive != nil {
		isActive = *req.IsActive
	}
	_, err := h.db.Exec(
		`UPDATE stream_sources SET name=?,url=?,match_datetime=?,round=?,channel=?,remark=?,is_active=? WHERE id=?`,
		req.Name, req.URL, req.MatchDatetime, req.Round, req.Channel, req.Remark, isActive, id)
	if err != nil {
		fail(c, http.StatusInternalServerError, err.Error())
		return
	}
	ok(c, gin.H{"updated": true})
}

func (h *Handler) deleteStreamSource(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	_, err := h.db.Exec(`DELETE FROM stream_sources WHERE id=?`, id)
	if err != nil {
		fail(c, http.StatusInternalServerError, err.Error())
		return
	}
	ok(c, gin.H{"deleted": true})
}

// CSV 列顺序（与模板一致）
var csvHeaders = []string{"名称", "比赛时间", "轮次", "所属频道", "直播流地址", "备注"}

func (h *Handler) downloadSourceTemplate(c *gin.Context) {
	var buf bytes.Buffer
	// UTF-8 BOM，让 Excel 正确识别中文
	buf.WriteString("\xef\xbb\xbf")
	w := csv.NewWriter(&buf)
	_ = w.Write(csvHeaders)
	_ = w.Write([]string{"泰州 vs 南京", "2025-06-01 19:30", "第3轮", "JSTV-2", "rtmp://example.com/live/streamA", "示例行请删除"})
	w.Flush()

	c.Header("Content-Disposition", `attachment; filename="stream_sources_template.csv"`)
	c.Data(http.StatusOK, "text/csv; charset=utf-8", buf.Bytes())
}

func (h *Handler) importStreamSources(c *gin.Context) {
	fh, err := c.FormFile("file")
	if err != nil {
		fail(c, http.StatusBadRequest, "missing file field")
		return
	}
	f, err := fh.Open()
	if err != nil {
		fail(c, http.StatusInternalServerError, err.Error())
		return
	}
	defer f.Close()

	r := csv.NewReader(f)
	records, err := r.ReadAll()
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid CSV: "+err.Error())
		return
	}
	if len(records) == 0 {
		fail(c, http.StatusBadRequest, "empty CSV")
		return
	}

	// 验证列头（忽略 BOM）
	hdr := records[0]
	if len(hdr) > 0 {
		hdr[0] = strings.TrimPrefix(hdr[0], "\xef\xbb\xbf")
	}
	if len(hdr) < 5 {
		fail(c, http.StatusBadRequest, "CSV 列数不足，期望列头：名称,比赛时间,轮次,所属频道,直播流地址[,备注]")
		return
	}

	imported, skipped := 0, 0
	for _, row := range records[1:] {
		for len(row) < 6 {
			row = append(row, "")
		}
		name := strings.TrimSpace(row[0])
		matchDatetime := strings.TrimSpace(row[1])
		round := strings.TrimSpace(row[2])
		channel := strings.TrimSpace(row[3])
		url := strings.TrimSpace(row[4])
		remark := strings.TrimSpace(row[5])

		if url == "" {
			skipped++
			continue
		}
		if name == "" {
			name = url // fallback
		}
		toNullable := func(s string) interface{} {
			if s == "" {
				return nil
			}
			return s
		}
		// 标准化 matchDatetime 格式为 YYYY-MM-DD HH:MM
		normalizedDatetime := toNullable(normalizeMatchDatetime(matchDatetime))
		_, err := h.db.Exec(
			`INSERT INTO stream_sources (name, url, match_datetime, round, channel, remark, is_active) VALUES (?,?,?,?,?,?,1)`,
			name, url, normalizedDatetime, toNullable(round), toNullable(channel), toNullable(remark),
		)
		if err != nil {
			skipped++
			continue
		}
		imported++
	}

	ok(c, gin.H{"imported": imported, "skipped": skipped})
}

// ── Videos ──────────────────────────────────────────────────────

func (h *Handler) listVideos(c *gin.Context) {
	cityID, err := cityIDFromParam(c)
	if err != nil || !assertCityAccess(c, cityID) {
		return
	}

	rows, err := h.db.Query(
		`SELECT id, city_id, original_filename, stored_filename,
		        transcode_status, transcode_error, duration_seconds,
		        display_name, thumbnail_path, created_at, progress_pct
		   FROM promotional_videos WHERE city_id=? ORDER BY created_at DESC`, cityID)
	if err != nil {
		fail(c, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	var videos []model.PromotionalVideo
	for rows.Next() {
		var v model.PromotionalVideo
		var thumbPath *string
		if err := rows.Scan(&v.ID, &v.CityID, &v.OriginalFilename, &v.StoredFilename,
			&v.TranscodeStatus, &v.TranscodeError, &v.DurationSeconds,
			&v.DisplayName, &thumbPath, &v.CreatedAt, &v.ProgressPct); err == nil {
			v.ThumbnailPath = thumbPath
			v.HasThumbnail = thumbPath != nil && *thumbPath != ""
			videos = append(videos, v)
		}
	}
	ok(c, videos)
}

func (h *Handler) updateVideo(c *gin.Context) {
	cityID, err := cityIDFromParam(c)
	if err != nil || !assertCityAccess(c, cityID) {
		return
	}
	videoID, _ := strconv.ParseInt(c.Param("videoId"), 10, 64)

	var req struct {
		DisplayName string `json:"displayName" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	_, err = h.db.Exec(
		`UPDATE promotional_videos SET display_name=? WHERE id=? AND city_id=?`,
		req.DisplayName, videoID, cityID)
	if err != nil {
		fail(c, http.StatusInternalServerError, err.Error())
		return
	}
	ok(c, gin.H{"updated": true})
}

func (h *Handler) thumbnailVideo(c *gin.Context) {
	cityID, err := cityIDFromParam(c)
	if err != nil || !assertCityAccess(c, cityID) {
		return
	}
	videoID, _ := strconv.ParseInt(c.Param("videoId"), 10, 64)

	var thumbPath string
	err = h.db.QueryRow(
		`SELECT COALESCE(thumbnail_path,'') FROM promotional_videos WHERE id=? AND city_id=?`,
		videoID, cityID).Scan(&thumbPath)
	if err != nil || thumbPath == "" {
		c.Status(http.StatusNotFound)
		return
	}
	c.File(thumbPath)
}

func (h *Handler) uploadVideo(c *gin.Context) {
	cityID, err := cityIDFromParam(c)
	if err != nil || !assertCityAccess(c, cityID) {
		return
	}

	file, header, err := c.Request.FormFile("file")
	if err != nil {
		fail(c, http.StatusBadRequest, "file required")
		return
	}
	defer file.Close()

	if !strings.HasSuffix(strings.ToLower(header.Filename), ".mp4") {
		fail(c, http.StatusBadRequest, "only .mp4 files allowed")
		return
	}

	// 1GB 配额校验：统计该城市上传目录已用空间
	cityUploadDir := filepath.Join(h.cfg.UploadDir, fmt.Sprintf("%d", cityID))
	usedBytes, _ := dirSize(cityUploadDir)
	const maxBytes = 1 << 30 // 1 GiB
	if usedBytes+header.Size > maxBytes {
		fail(c, http.StatusRequestEntityTooLarge, "存储配额已满（上限 1 GB），请删除旧文件后重试")
		return
	}

	// 生成唯一存储文件名（时间戳 + 原始名）
	storedName := fmt.Sprintf("%d_%s", time.Now().UnixNano(), header.Filename)
	uploadPath := filepath.Join(h.cfg.UploadDir, fmt.Sprintf("%d", cityID), storedName)
	if err := os.MkdirAll(filepath.Dir(uploadPath), 0755); err != nil {
		fail(c, http.StatusInternalServerError, "create upload dir failed")
		return
	}

	dst, err := os.Create(uploadPath)
	if err != nil {
		fail(c, http.StatusInternalServerError, "save file failed")
		return
	}
	defer dst.Close()
	if _, err := io.CopyBuffer(dst, file, make([]byte, 1024*1024)); err != nil {
		fail(c, http.StatusInternalServerError, "write file failed")
		return
	}

	userID, _ := c.Get("userID")
	res, err := h.db.Exec(
		`INSERT INTO promotional_videos
		    (city_id, original_filename, stored_filename, upload_path, created_by)
		 VALUES (?,?,?,?,?)`,
		cityID, header.Filename, storedName, uploadPath, userID)
	if err != nil {
		fail(c, http.StatusInternalServerError, err.Error())
		return
	}
	videoID, _ := res.LastInsertId()

	// 投入转码队列
	if err := h.transcode.EnqueueVideo(videoID); err != nil {
		// 非致命错误，文件已保存，转码可以后台重试
		c.JSON(http.StatusAccepted, gin.H{
			"data": gin.H{"id": videoID, "warning": "enqueue transcode failed: " + err.Error()},
		})
		return
	}

	ok(c, gin.H{"id": videoID, "transcodeStatus": "pending"})
}

func (h *Handler) deleteVideo(c *gin.Context) {
	cityID, err := cityIDFromParam(c)
	if err != nil || !assertCityAccess(c, cityID) {
		return
	}
	videoID, _ := strconv.ParseInt(c.Param("videoId"), 10, 64)

	var uploadPath string
	_ = h.db.QueryRow(`SELECT upload_path FROM promotional_videos WHERE id=? AND city_id=?`,
		videoID, cityID).Scan(&uploadPath)

	_, err = h.db.Exec(`DELETE FROM promotional_videos WHERE id=? AND city_id=?`, videoID, cityID)
	if err != nil {
		fail(c, http.StatusInternalServerError, err.Error())
		return
	}

	// 清理文件（忽略错误）
	_ = os.Remove(uploadPath)
	ok(c, gin.H{"deleted": true})
}

func (h *Handler) retranscodeVideo(c *gin.Context) {
	cityID, err := cityIDFromParam(c)
	if err != nil || !assertCityAccess(c, cityID) {
		return
	}
	videoID, _ := strconv.ParseInt(c.Param("videoId"), 10, 64)

	var uploadPath string
	var status string
	err = h.db.QueryRow(`SELECT upload_path, transcode_status FROM promotional_videos WHERE id=? AND city_id=?`,
		videoID, cityID).Scan(&uploadPath, &status)
	if err != nil {
		fail(c, http.StatusNotFound, "video not found")
		return
	}
	if status == "processing" {
		fail(c, http.StatusConflict, "transcode already in progress")
		return
	}
	if _, err := os.Stat(uploadPath); err != nil {
		fail(c, http.StatusGone, "source file not found on disk")
		return
	}

	_, _ = h.db.Exec(`UPDATE promotional_videos SET transcode_status='pending', transcode_error=NULL WHERE id=?`, videoID)
	if err := h.transcode.EnqueueVideo(videoID); err != nil {
		fail(c, http.StatusServiceUnavailable, err.Error())
		return
	}
	ok(c, gin.H{"queued": true})
}



func (h *Handler) getStreamConfig(c *gin.Context) {
	cityID, err := cityIDFromParam(c)
	if err != nil || !assertCityAccess(c, cityID) {
		return
	}

	var sc model.StreamConfig
	row := h.db.QueryRow(
		`SELECT id, city_id, push_url, push_key, volume_gain, srs_app, srs_stream, config_locked
		   FROM stream_configs WHERE city_id=?`, cityID)
	if err := row.Scan(&sc.ID, &sc.CityID, &sc.PushURL, &sc.PushKey,
		&sc.VolumeGain, &sc.SRSApp, &sc.SRSStream, &sc.ConfigLocked); err != nil {
		fail(c, http.StatusNotFound, "stream config not found")
		return
	}
	ok(c, sc)
}

type updateStreamConfigReq struct {
	PushURL    *string  `json:"pushUrl"`
	PushKey    *string  `json:"pushKey"`
	VolumeGain *float64 `json:"volumeGain"`
}

func (h *Handler) updateStreamConfig(c *gin.Context) {
	cityID, err := cityIDFromParam(c)
	if err != nil || !assertCityAccess(c, cityID) {
		return
	}

	var req updateStreamConfigReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	if req.VolumeGain != nil && (*req.VolumeGain < 1.0 || *req.VolumeGain > 2.0) {
		fail(c, http.StatusBadRequest, "volumeGain must be between 1.0 and 2.0")
		return
	}

	// 只更新提供的字段
	setParts := []string{"updated_at=?"}
	args := []interface{}{time.Now()}

	if req.PushURL != nil {
		setParts = append(setParts, "push_url=?")
		args = append(args, *req.PushURL)
	}
	if req.PushKey != nil {
		setParts = append(setParts, "push_key=?")
		args = append(args, *req.PushKey)
		if *req.PushKey == "" {
			setParts = append(setParts, "config_locked=0")
		} else {
			setParts = append(setParts, "config_locked=1")
		}
	}
	if req.VolumeGain != nil {
		setParts = append(setParts, "volume_gain=?")
		args = append(args, *req.VolumeGain)
	}
	args = append(args, cityID)

	query := `UPDATE stream_configs SET ` + strings.Join(setParts, ",") + ` WHERE city_id=?`
	if _, err := h.db.Exec(query, args...); err != nil {
		fail(c, http.StatusInternalServerError, err.Error())
		return
	}
	ok(c, gin.H{"updated": true})
}

// ── Schedules ───────────────────────────────────────────────────

func (h *Handler) listSchedules(c *gin.Context) {
	cityID, err := cityIDFromParam(c)
	if err != nil || !assertCityAccess(c, cityID) {
		return
	}

	date := c.Query("date") // YYYY-MM-DD，可选
	var rows *sql.Rows
	if date != "" {
		rows, err = h.db.Query(
			`SELECT id, city_id, date, status, created_at, updated_at
			   FROM schedules WHERE city_id=? AND date=? ORDER BY date DESC`, cityID, date)
	} else {
		rows, err = h.db.Query(
			`SELECT id, city_id, date, status, created_at, updated_at
			   FROM schedules WHERE city_id=? ORDER BY date DESC LIMIT 30`, cityID)
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	var schedules []model.Schedule
	for rows.Next() {
		var s model.Schedule
		if err := rows.Scan(&s.ID, &s.CityID, &s.Date, &s.Status,
			&s.CreatedAt, &s.UpdatedAt); err == nil {
			schedules = append(schedules, s)
		}
	}

	// 自动修复：排期状态是 running 但 ffmpeg 进程已不在运行，且调度器 goroutine 也不在等待中，则重置为 stopped
	for i := range schedules {
		if schedules[i].Status == "running" {
			// 若调度器 goroutine 正在运行（含等待 start_time 阶段），不重置
			if h.scheduler.IsRunning(cityID) {
				continue
			}
			var fpStatus string
			h.db.QueryRow(
				`SELECT status FROM ffmpeg_processes WHERE city_id=? AND schedule_id=?`,
				cityID, schedules[i].ID,
			).Scan(&fpStatus)
			if fpStatus != "streaming" && fpStatus != "warming" {
				schedules[i].Status = "stopped"
				h.db.Exec(`UPDATE schedules SET status='stopped' WHERE id=?`, schedules[i].ID)
			}
		}
	}

	// 加载每个排期的条目
	for i := range schedules {
		itemRows, err := h.db.Query(
			`SELECT id, schedule_id, order_index, item_type,
			        promo_video_id, loop_count, stream_source_id, start_time
			   FROM schedule_items WHERE schedule_id=? ORDER BY order_index ASC`,
			schedules[i].ID)
		if err != nil {
			continue
		}
		for itemRows.Next() {
			var it model.ScheduleItem
			if err := itemRows.Scan(&it.ID, &it.ScheduleID, &it.OrderIndex, &it.ItemType,
				&it.PromoVideoID, &it.LoopCount, &it.StreamSourceID, &it.StartTime); err == nil {
				schedules[i].Items = append(schedules[i].Items, it)
			}
		}
		itemRows.Close()
	}

	ok(c, schedules)
}

type createScheduleReq struct {
	Date string `json:"date" binding:"required"` // YYYY-MM-DD
}

func (h *Handler) createSchedule(c *gin.Context) {
	cityID, err := cityIDFromParam(c)
	if err != nil || !assertCityAccess(c, cityID) {
		return
	}

	var req createScheduleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}

	res, err := h.db.Exec(
		`INSERT INTO schedules (city_id, date) VALUES (?,?)`, cityID, req.Date)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			fail(c, http.StatusConflict, "schedule for this date already exists")
			return
		}
		fail(c, http.StatusInternalServerError, err.Error())
		return
	}
	id, _ := res.LastInsertId()
	ok(c, gin.H{"id": id})
}

type scheduleItemReq struct {
	OrderIndex     int    `json:"orderIndex"`
	ItemType       string `json:"itemType" binding:"required,oneof=promo_video live_stream"`
	PromoVideoID   *int64 `json:"promoVideoId"`
	LoopCount      int    `json:"loopCount"`
	StreamSourceID *int64 `json:"streamSourceId"`
	StartTime      string `json:"startTime" binding:"required"` // "HH:MM"
}

// updateScheduleItems 替换某排期的所有条目（前端每次全量提交）
func (h *Handler) updateScheduleItems(c *gin.Context) {
	cityID, err := cityIDFromParam(c)
	if err != nil || !assertCityAccess(c, cityID) {
		return
	}
	scheduleID, _ := strconv.ParseInt(c.Param("scheduleId"), 10, 64)

	// 验证排期属于该城市且处于可编辑状态（draft 或 stopped）
	var status string
	_ = h.db.QueryRow(`SELECT status FROM schedules WHERE id=? AND city_id=?`,
		scheduleID, cityID).Scan(&status)
	if status == "" {
		fail(c, http.StatusNotFound, "schedule not found")
		return
	}
	if status != "draft" && status != "stopped" {
		fail(c, http.StatusBadRequest, "can only edit draft or stopped schedules")
		return
	}

	var items []scheduleItemReq
	if err := c.ShouldBindJSON(&items); err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}

	// 时间重叠检查
	if err := checkTimeOverlap(items); err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}

	tx, err := h.db.Begin()
	if err != nil {
		fail(c, http.StatusInternalServerError, err.Error())
		return
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM schedule_items WHERE schedule_id=?`, scheduleID); err != nil {
		fail(c, http.StatusInternalServerError, err.Error())
		return
	}

	for i, item := range items {
		loopCount := item.LoopCount
		if loopCount == 0 {
			loopCount = -1
		}
		if _, err := tx.Exec(
			`INSERT INTO schedule_items
			    (schedule_id, order_index, item_type, promo_video_id, loop_count, stream_source_id, start_time)
			 VALUES (?,?,?,?,?,?,?)`,
			scheduleID, i, item.ItemType, item.PromoVideoID, loopCount, item.StreamSourceID, item.StartTime,
		); err != nil {
			fail(c, http.StatusInternalServerError, err.Error())
			return
		}
	}

	if err := tx.Commit(); err != nil {
		fail(c, http.StatusInternalServerError, err.Error())
		return
	}
	ok(c, gin.H{"updated": len(items)})
}

func (h *Handler) startSchedule(c *gin.Context) {
	cityID, err := cityIDFromParam(c)
	if err != nil || !assertCityAccess(c, cityID) {
		return
	}
	scheduleID, _ := strconv.ParseInt(c.Param("scheduleId"), 10, 64)

	var req struct {
		Manual   bool `json:"manual"`
		ForceNow bool `json:"forceNow"` // true=立即推流，跳过 start_time 等待
	}
	_ = c.ShouldBindJSON(&req) // 忽略 bind 错误，manual/forceNow 默认为 false

	var startErr error
	if req.Manual {
		startErr = h.scheduler.RunScheduleManual(cityID, scheduleID)
	} else {
		startErr = h.scheduler.RunSchedule(cityID, scheduleID, req.ForceNow)
	}
	if startErr != nil {
		fail(c, http.StatusBadRequest, startErr.Error())
		return
	}
	ok(c, gin.H{"started": true, "manual": req.Manual, "forceNow": req.ForceNow})
}

func (h *Handler) stopSchedule(c *gin.Context) {
	cityID, err := cityIDFromParam(c)
	if err != nil || !assertCityAccess(c, cityID) {
		return
	}
	scheduleID, _ := strconv.ParseInt(c.Param("scheduleId"), 10, 64)
	h.scheduler.StopSchedule(cityID, scheduleID)
	ok(c, gin.H{"stopped": true})
}

func (h *Handler) advanceSchedule(c *gin.Context) {
	cityID, err := cityIDFromParam(c)
	if err != nil || !assertCityAccess(c, cityID) {
		return
	}
	if err := h.scheduler.AdvanceSchedule(cityID); err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	ok(c, gin.H{"advanced": true})
}

func (h *Handler) deleteSchedule(c *gin.Context) {
	cityID, err := cityIDFromParam(c)
	if err != nil || !assertCityAccess(c, cityID) {
		return
	}
	scheduleID, _ := strconv.ParseInt(c.Param("scheduleId"), 10, 64)
	// 先停止（如正在运行）
	h.scheduler.StopSchedule(cityID, scheduleID)
	// 删除排期（级联删除条目）
	if _, err := h.db.Exec(`DELETE FROM schedules WHERE id=? AND city_id=?`, scheduleID, cityID); err != nil {
		fail(c, http.StatusInternalServerError, err.Error())
		return
	}
	ok(c, gin.H{"deleted": true})
}

// ── Alerts ──────────────────────────────────────────────────────

func (h *Handler) listAlerts(c *gin.Context) {
	cityID, err := cityIDFromParam(c)
	if err != nil || !assertCityAccess(c, cityID) {
		return
	}

	rows, err := h.db.Query(
		`SELECT id, city_id, level, message, sms_sent, created_at
		   FROM alert_logs WHERE city_id=? ORDER BY created_at DESC LIMIT 50`, cityID)
	if err != nil {
		fail(c, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	var alerts []model.AlertLog
	for rows.Next() {
		var a model.AlertLog
		if err := rows.Scan(&a.ID, &a.CityID, &a.Level, &a.Message, &a.SMSSent, &a.CreatedAt); err == nil {
			alerts = append(alerts, a)
		}
	}
	ok(c, alerts)
}

// ── SRS Hooks ───────────────────────────────────────────────────

type srsHookBody struct {
	Stream string `json:"stream"` // city code
	App    string `json:"app"`
}

func (h *Handler) srsPublish(c *gin.Context) {
	// SRS 推流上线时回调，可用于更新状态
	// SRS 要求 HTTP 200 + code=0 才认为授权成功
	c.JSON(http.StatusOK, gin.H{"code": 0})
}

func (h *Handler) srsUnpublish(c *gin.Context) {
	// SRS 推流下线回调
	c.JSON(http.StatusOK, gin.H{"code": 0})
}

// ── 辅助函数 ────────────────────────────────────────────────────

// checkTimeOverlap 检查排期条目的时间是否重叠
func checkTimeOverlap(items []scheduleItemReq) error {
	seen := make(map[string]bool)
	for _, item := range items {
		if seen[item.StartTime] {
			return fmt.Errorf("duplicate start_time %s detected", item.StartTime)
		}
		seen[item.StartTime] = true
	}
	return nil
}

// resetFFmpeg 清除熔断状态，将 ffmpeg_processes 重置为 idle
// 用于熔断后不删除排期直接重试推流
func (h *Handler) resetFFmpeg(c *gin.Context) {
	cityID, err := cityIDFromParam(c)
	if err != nil || !assertCityAccess(c, cityID) {
		return
	}
	// 停止调度器 goroutine（若仍在等待 start_time 或运行中）
	var scheduleID int64
	h.db.QueryRow(`SELECT id FROM schedules WHERE city_id=? AND status='running' ORDER BY created_at DESC LIMIT 1`, cityID).Scan(&scheduleID)
	if scheduleID > 0 {
		h.scheduler.StopSchedule(cityID, scheduleID)
	}
	// 停止可能残存的 ffmpeg 进程
	h.ffmpeg.StopCity(cityID)
	// 直接重置 DB 状态
	h.db.Exec(
		`UPDATE ffmpeg_processes SET status='idle', retry_count=0, current_item_id=NULL, push_pid=NULL, inject_pid=NULL, direct_source_name=NULL WHERE city_id=?`,
		cityID)
	ok(c, gin.H{"reset": true})
}

// maskKey 将密钥脱敏（保留前 4 位）
func maskKey(key string) string {
	if len(key) <= 4 {
		return "****"
	}
	return key[:4] + strings.Repeat("*", len(key)-4)
}

// normalizeMatchDatetime 将各种日期格式统一为 "YYYY-MM-DD HH:MM"
// 支持 "2026/4/20 19:00"、"2026/04/20 19:00"、"2026-4-20 19:00" 等
func normalizeMatchDatetime(s string) string {
	if s == "" {
		return ""
	}
	formats := []string{
		"2006/1/2 15:04",
		"2006/01/02 15:04",
		"2006-1-2 15:04",
		"2006-01-02 15:04",
		"2006/1/2",
		"2006/01/02",
		"2006-1-2",
		"2006-01-02",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			if len(f) > 8 { // 含时间
				return t.Format("2006-01-02 15:04")
			}
			return t.Format("2006-01-02")
		}
	}
	return s // 无法解析时原样返回
}

// dirSize 递归统计目录下所有文件的总字节数
func dirSize(path string) (int64, error) {
	var total int64
	filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, e := d.Info()
		if e == nil {
			total += info.Size()
		}
		return nil
	})
	return total, nil
}

// ── Audit Logs ──────────────────────────────────────────────────

func (h *Handler) listAuditLogs(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "50"))
	action := c.Query("action")
	username := c.Query("username")
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 200 {
		pageSize = 50
	}
	offset := (page - 1) * pageSize

	where := "WHERE 1=1"
	args := []interface{}{}
	if action != "" {
		where += " AND action=?"
		args = append(args, action)
	}
	if username != "" {
		where += " AND username LIKE ?"
		args = append(args, "%"+username+"%")
	}

	var total int
	h.db.QueryRow("SELECT COUNT(*) FROM audit_logs "+where, args...).Scan(&total) //nolint:errcheck

	queryArgs := append(args, pageSize, offset)
	rows, err := h.db.Query(
		"SELECT id, user_id, username, role, action, detail, ip, "+
			"strftime('%Y-%m-%d %H:%M:%S', created_at) FROM audit_logs "+
			where+" ORDER BY created_at DESC LIMIT ? OFFSET ?",
		queryArgs...)
	if err != nil {
		fail(c, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	logs := make([]model.AuditLog, 0)
	for rows.Next() {
		var l model.AuditLog
		rows.Scan(&l.ID, &l.UserID, &l.Username, &l.Role, &l.Action, &l.Detail, &l.IP, &l.CreatedAt) //nolint:errcheck
		logs = append(logs, l)
	}
	ok(c, gin.H{"total": total, "logs": logs})
}
