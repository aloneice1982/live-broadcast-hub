// Package service — sms.go
//
// 短信告警 Service。
// 触发熔断时，向对应地市管理员和超管发送 HTTP 短信 API 请求。
// SMS_API_URL 留空时静默跳过（不影响主流程）。

package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"susuper/internal/config"
)

// SMSService 封装短信告警发送逻辑
type SMSService struct {
	db     *sql.DB
	cfg    *config.Config
	client *http.Client
	logger *log.Logger
}

// NewSMSService 创建 SMSService
func NewSMSService(db *sql.DB, cfg *config.Config) *SMSService {
	return &SMSService{
		db:     db,
		cfg:    cfg,
		client: &http.Client{Timeout: 10 * time.Second},
		logger: log.New(os.Stdout, "[sms] ", log.LstdFlags),
	}
}

// SendCityAlert 向某地市管理员 + 超管发送告警短信
func (s *SMSService) SendCityAlert(cityID int64, message string) {
	if s.cfg.SMSAPIURL == "" {
		s.logger.Printf("SMS_API_URL not configured, skip alert for city=%d", cityID)
		return
	}

	phones, err := s.resolvePhones(cityID)
	if err != nil {
		s.logger.Printf("resolve phones for city=%d: %v", cityID, err)
		return
	}
	if len(phones) == 0 {
		s.logger.Printf("no phone numbers configured for city=%d", cityID)
		return
	}

	for _, phone := range phones {
		if err := s.send(phone, message); err != nil {
			s.logger.Printf("send to %s failed: %v", phone, err)
		} else {
			s.logger.Printf("sent to %s", phone)
		}
	}

	// 标记告警日志已发送短信
	_, _ = s.db.Exec(
		`UPDATE alert_logs SET sms_sent=1
		  WHERE city_id=? AND sms_sent=0
		  ORDER BY created_at DESC LIMIT 1`, cityID)
}

// resolvePhones 合并地市管理员手机号和超管手机号
func (s *SMSService) resolvePhones(cityID int64) ([]string, error) {
	seen := make(map[string]struct{})
	var phones []string

	addPhone := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		if _, ok := seen[p]; !ok {
			seen[p] = struct{}{}
			phones = append(phones, p)
		}
	}

	// 1. 地市管理员手机号
	rows, err := s.db.Query(
		`SELECT phone FROM users WHERE city_id=? AND role='city_admin' AND phone IS NOT NULL`, cityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err == nil {
			addPhone(p)
		}
	}

	// 2. 超管手机号（来自 .env SMS_SUPER_ADMIN_PHONES）
	for _, p := range strings.Split(s.cfg.SMSSuperAdminPhones, ",") {
		addPhone(p)
	}

	return phones, nil
}

// send 调用 HTTP 短信 API 网关
// 请求格式：POST JSON { "phone": "...", "message": "...", "key": "..." }
// 具体格式由运营商 API 决定，这里实现通用版本，可按需修改
func (s *SMSService) send(phone, message string) error {
	payload := map[string]string{
		"phone":   phone,
		"message": message,
		"key":     s.cfg.SMSAPIKey,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.SMSAPIURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("SMS API returned HTTP %d", resp.StatusCode)
	}
	return nil
}
