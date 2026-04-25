package model

import "time"

// ── City ──────────────────────────────────────────────────────
type City struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Code string `json:"code"`
}

// ── User ──────────────────────────────────────────────────────
type User struct {
	ID           int64      `json:"id"`
	Username     string     `json:"username"`
	PasswordHash string     `json:"-"`
	Role         string     `json:"role"` // super_admin | city_admin
	CityID       *int64     `json:"cityId,omitempty"`
	Phone        *string    `json:"phone,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
}

// ── StreamSource ──────────────────────────────────────────────
type StreamSource struct {
	ID            int64     `json:"id"`
	Name          string    `json:"name"`
	URL           string    `json:"url"`
	MatchDatetime *string   `json:"matchDatetime,omitempty"`
	Round         *string   `json:"round,omitempty"`
	Channel       *string   `json:"channel,omitempty"`
	Remark        *string   `json:"remark,omitempty"`
	IsActive      bool      `json:"isActive"`
	CreatedAt     time.Time `json:"createdAt"`
}

// ── PromotionalVideo ──────────────────────────────────────────
type PromotionalVideo struct {
	ID               int64     `json:"id"`
	CityID           int64     `json:"cityId"`
	OriginalFilename string    `json:"originalFilename"`
	StoredFilename   string    `json:"storedFilename"`
	UploadPath       string    `json:"-"`
	TranscodedPath   *string   `json:"-"`
	ThumbnailPath    *string   `json:"-"`
	DisplayName      *string   `json:"displayName,omitempty"`
	HasThumbnail     bool      `json:"hasThumbnail"`
	TranscodeStatus  string    `json:"transcodeStatus"` // pending|processing|done|failed
	TranscodeError   *string   `json:"transcodeError,omitempty"`
	DurationSeconds  *int      `json:"durationSeconds,omitempty"`
	ProgressPct      int       `json:"progressPct"` // 0-100，转码中实时更新
	CreatedAt        time.Time `json:"createdAt"`
	CreatedBy        *int64    `json:"createdBy,omitempty"`
}

// ── StreamConfig ──────────────────────────────────────────────
type StreamConfig struct {
	ID           int64   `json:"id"`
	CityID       int64   `json:"cityId"`
	PushURL      *string `json:"pushUrl"`
	PushKey      *string `json:"pushKey,omitempty"` // 脱敏，不直接返回
	VolumeGain   float64 `json:"volumeGain"`
	SRSApp       string  `json:"srsApp"`
	SRSStream    string  `json:"srsStream"`
	ConfigLocked bool    `json:"configLocked"` // true = 密钥已保存并锁定，可以开播
}

// ── Schedule ──────────────────────────────────────────────────
type Schedule struct {
	ID        int64          `json:"id"`
	CityID    int64          `json:"cityId"`
	Date      string         `json:"date"` // YYYY-MM-DD
	Status    string         `json:"status"` // draft|running|stopped|finished
	Items     []ScheduleItem `json:"items,omitempty"`
	CreatedAt time.Time      `json:"createdAt"`
	UpdatedAt time.Time      `json:"updatedAt"`
}

// ── ScheduleItem ──────────────────────────────────────────────
type ScheduleItem struct {
	ID             int64   `json:"id"`
	ScheduleID     int64   `json:"scheduleId"`
	OrderIndex     int     `json:"orderIndex"`
	ItemType       string  `json:"itemType"` // promo_video | live_stream
	PromoVideoID   *int64  `json:"promoVideoId,omitempty"`
	LoopCount      int     `json:"loopCount"` // -1 = infinite until next
	StreamSourceID *int64  `json:"streamSourceId,omitempty"`
	StartTime      string  `json:"startTime"` // "HH:MM"
	// 关联数据（查询时填充）
	PromoVideo   *PromotionalVideo `json:"promoVideo,omitempty"`
	StreamSource *StreamSource     `json:"streamSource,omitempty"`
}

// ── FFmpegProcess ─────────────────────────────────────────────
type FFmpegProcess struct {
	ID            int64   `json:"id"`
	CityID        int64   `json:"cityId"`
	ScheduleID    *int64  `json:"scheduleId,omitempty"`
	PushPID       *int    `json:"pushPid,omitempty"`
	InjectPID     *int    `json:"injectPid,omitempty"`
	Status        string  `json:"status"` // idle|warming|streaming|failed|breaker_open
	CurrentItemID *int64  `json:"currentItemId,omitempty"`
	RetryCount    int     `json:"retryCount"`
}

// ── AlertLog ─────────────────────────────────────────────────
type AlertLog struct {
	ID        int64     `json:"id"`
	CityID    int64     `json:"cityId"`
	Level     string    `json:"level"` // warn|error|critical
	Message   string    `json:"message"`
	SMSSent   bool      `json:"smsSent"`
	CreatedAt time.Time `json:"createdAt"`
}

// ── AuditLog ──────────────────────────────────────────────────
type AuditLog struct {
	ID        int64  `json:"id"`
	UserID    *int64 `json:"userId,omitempty"`
	Username  string `json:"username"`
	Role      string `json:"role"`
	Action    string `json:"action"`
	Detail    string `json:"detail"`
	IP        string `json:"ip"`
	CreatedAt string `json:"createdAt"`
}
