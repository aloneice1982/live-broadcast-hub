// Package service — ffmpeg.go
//
// FFmpeg 进程守护 Service。
//
// 架构：每个地市维护两个 FFmpeg 子进程：
//
//   [inject-ffmpeg]  source ──push──► SRS :1935/live/{city_code}
//   [push-ffmpeg]    SRS ────pull──► 视频号推流地址
//
// 切流原理：
//   "无缝切换" = 只更换 inject 进程的输入源，push 进程和推流地址保持不变。
//   SRS 的 GOP 缓存（gop_cache=on）保证新 inject 上线后 push 端几乎无感。
//
// 熔断规则：
//   inject 或 push 进程意外退出 → 3 秒后自动重启 → 连续失败 N 次 → 触发熔断
//   熔断后：停止重试，记录 DB，发送短信告警

package service

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"susuper/internal/config"
	"susuper/internal/model"
	"susuper/pkg/ffscript"
	"susuper/pkg/srsapi"
)

const (
	procTypeInject      = "inject"
	procTypePush        = "push"
	procTypeInjectPromo = "inject-promo" // 宣传片单次插播进程
)

// managedProc 代表一个受守护的 FFmpeg 子进程
type managedProc struct {
	cmd       *exec.Cmd
	args      []string
	procType  string
	done      chan struct{} // 关闭时通知 watchdog
	stderrBuf *stderrRing  // 捕获 stderr 末尾行，用于错误诊断
	discarded bool         // true = 已被 SetMute/SwitchSource 主动替换，watchdog 不应重启
}

// stderrRing 保留 ffmpeg stderr 最后 N 行，用于故障诊断
type stderrRing struct {
	mu  sync.Mutex
	buf []string
	max int
}

func (r *stderrRing) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			r.buf = append(r.buf, line)
			if len(r.buf) > r.max {
				r.buf = r.buf[1:]
			}
		}
	}
	return len(p), nil
}

// LastMeaningful 返回最后一行非空 stderr 输出
func (r *stderrRing) LastMeaningful() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.buf) - 1; i >= 0; i-- {
		if strings.TrimSpace(r.buf[i]) != "" {
			return r.buf[i]
		}
	}
	return ""
}

// classifyFFmpegErr 根据 ffmpeg stderr 最后一行和退出错误给出中文原因
func classifyFFmpegErr(exitErr error, lastLine string) string {
	l := strings.ToLower(lastLine)
	switch {
	case strings.Contains(l, "403"), strings.Contains(l, "forbidden"),
		strings.Contains(l, "401"), strings.Contains(l, "unauthorized"):
		return "推流密钥被拒绝（密钥失效或错误）"
	case strings.Contains(l, "connection refused"):
		return "推流服务器拒绝连接（地址可能错误）"
	case strings.Contains(l, "connection timed out"), strings.Contains(l, "timed out"),
		strings.Contains(l, "i/o timeout"):
		return "连接超时（网络问题或服务器不可达）"
	case strings.Contains(l, "connection reset"), strings.Contains(l, "broken pipe"),
		strings.Contains(l, "econnreset"):
		return "连接被重置（直播源或推流服务器断开）"
	case strings.Contains(l, "no such file"), strings.Contains(l, "not found"):
		return "视频文件不存在（转码未完成或文件已删除）"
	case strings.Contains(l, "invalid data"), strings.Contains(l, "invalid argument"):
		return "视频流格式错误（直播源或宣传片编码异常）"
	case strings.Contains(l, "rtmp"), strings.Contains(l, "flv"):
		return "RTMP 推流失败（密钥或地址格式有误）"
	default:
		if exitErr != nil {
			return "进程异常退出：" + exitErr.Error()
		}
		return "未知错误"
	}
}

// citySession 代表某个地市的完整推流会话
type citySession struct {
	cityID              int64
	scheduleID          int64
	inject              *managedProc // 主 inject 进程（直播流）
	injectPromo         *managedProc // 宣传片插播进程（临时，播完自动退出或手动停止）
	push                *managedProc
	retries             int
	mu                  sync.Mutex
	ctx                 context.Context
	cancel              context.CancelFunc
	currentSourceURL    string // 当前 inject 进程使用的直播源 URL（静音切换时复用）
	isMuted             bool
	promoStartedAt      time.Time // 宣传片插播开始时间
	promoVideoDuration  int       // 宣传片时长（秒），用于倒计时
	promoLoop           bool      // 是否循环播放
}

// FFmpegService 管理所有地市的推流进程
type FFmpegService struct {
	sessions map[int64]*citySession
	mu       sync.RWMutex
	db       *sql.DB
	cfg      *config.Config
	sms      *SMSService
	srs      *srsapi.Client
	logger   *log.Logger
}

// NewFFmpegService 创建 FFmpegService
func NewFFmpegService(db *sql.DB, cfg *config.Config, sms *SMSService) *FFmpegService {
	return &FFmpegService{
		sessions: make(map[int64]*citySession),
		db:       db,
		cfg:      cfg,
		sms:             sms,
		srs:             srsapi.New(cfg.SRSHost, cfg.SRSAPIPort),
		logger:          log.New(os.Stdout, "[ffmpeg] ", log.LstdFlags),
	}
}

// ── 公开 API ────────────────────────────────────────────────────

// StartCity 启动某个地市的推流会话
// 1. 从 DB 读取推流配置和首个排期条目
// 2. 启动 push-ffmpeg（SRS → 视频号）
// 3. 启动 inject-ffmpeg（source → SRS）
func (s *FFmpegService) StartCity(cityID, scheduleID int64, firstItem *model.ScheduleItem) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if sess, ok := s.sessions[cityID]; ok && sess.ctx.Err() == nil {
		return fmt.Errorf("city %d already has an active session", cityID)
	}

	cfg, err := s.loadStreamConfig(cityID)
	if err != nil {
		return fmt.Errorf("load stream config: %w", err)
	}
	if cfg.PushURL == nil || cfg.PushKey == nil {
		return fmt.Errorf("city %d: push_url or push_key not configured", cityID)
	}

	ctx, cancel := context.WithCancel(context.Background())
	sess := &citySession{
		cityID:     cityID,
		scheduleID: scheduleID,
		ctx:        ctx,
		cancel:     cancel,
	}
	s.sessions[cityID] = sess

	// 启动 push 进程（SRS → 视频号），这个进程一旦启动就不换
	pushArgs := s.buildPushArgs(cfg)
	if err := s.startProc(sess, procTypePush, pushArgs); err != nil {
		cancel()
		delete(s.sessions, cityID)
		return fmt.Errorf("start push proc: %w", err)
	}

	// 等待 push 进程预热（SRS 需要有流才能拉）后启动 inject
	time.Sleep(500 * time.Millisecond)

	injectArgs, err := s.buildInjectArgs(cfg, firstItem)
	if err != nil {
		s.stopCityLocked(cityID)
		return fmt.Errorf("build inject args: %w", err)
	}
	if err := s.startProc(sess, procTypeInject, injectArgs); err != nil {
		s.stopCityLocked(cityID)
		return fmt.Errorf("start inject proc: %w", err)
	}
	// 记录直播源 URL，供 SetMute 切换时复用
	if firstItem.StreamSource != nil {
		sess.currentSourceURL = firstItem.StreamSource.URL
	}

	_ = s.updateDBStatus(cityID, scheduleID, "streaming", firstItem.ID, 0)
	s.logger.Printf("city=%d started (schedule=%d)", cityID, scheduleID)
	return nil
}

// SwitchSource 切换 inject 源（宣传片 ↔ 直播流）
// push 进程保持不变，只重启 inject 进程
func (s *FFmpegService) SwitchSource(cityID int64, item *model.ScheduleItem) error {
	s.mu.RLock()
	sess, ok := s.sessions[cityID]
	s.mu.RUnlock()
	if !ok {
		return fmt.Errorf("city %d: no active session", cityID)
	}

	cfg, err := s.loadStreamConfig(cityID)
	if err != nil {
		return err
	}

	newArgs, err := s.buildInjectArgs(cfg, item)
	if err != nil {
		return err
	}

	sess.mu.Lock()
	defer sess.mu.Unlock()

	// 无缝切流：先启动新进程 → SRS 确认 → 再停旧进程
	// 详见 seamlessSwitch 函数注释
	oldInject := sess.inject
	oldInject.discarded = true // 通知 watchdog 不要重启旧进程
	if err := s.seamlessSwitch(sess, newArgs, cfg.SRSApp, cfg.SRSStream); err != nil {
		return fmt.Errorf("seamless switch: %w", err)
	}

	// 旧进程此时已被 SRS 断开，SIGTERM 收尾
	s.killProc(oldInject)

	_ = s.updateCurrentItem(cityID, item.ID)
	s.logger.Printf("city=%d switched to item=%d (%s)", cityID, item.ID, item.ItemType)
	return nil
}

// StopCity 停止某个地市的推流会话
func (s *FFmpegService) StopCity(cityID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopCityLocked(cityID)
}

func (s *FFmpegService) stopCityLocked(cityID int64) {
	sess, ok := s.sessions[cityID]
	if !ok {
		return
	}
	s.logger.Printf("city=%d stopping...", cityID)
	sess.cancel()
	s.killProc(sess.inject)
	s.killProc(sess.injectPromo) // 停止宣传片插播进程（如果存在）
	s.killProc(sess.push)
	delete(s.sessions, cityID)
	_ = s.updateDBStatus(cityID, 0, "idle", 0, 0)
}

// GetStatus 返回某地市当前状态
func (s *FFmpegService) GetStatus(cityID int64) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[cityID]
	if !ok {
		return "idle"
	}
	_ = sess
	// 从 DB 读取最新状态
	var status string
	_ = s.db.QueryRow(`SELECT status FROM ffmpeg_processes WHERE city_id=?`, cityID).Scan(&status)
	return status
}

// HasSession 返回该地市是否有活跃的推流 session（内存中）
func (s *FFmpegService) HasSession(cityID int64) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.sessions[cityID]
	return ok
}

// IsPromoInserting 返回该地市是否正在插播宣传片
func (s *FFmpegService) IsPromoInserting(cityID int64) bool {
	s.mu.RLock()
	sess, ok := s.sessions[cityID]
	s.mu.RUnlock()
	if !ok {
		return false
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.injectPromo != nil
}

// PromoStatus 返回当前插播状态：是否插播中、是否循环、剩余秒数、开始时间、视频时长
func (s *FFmpegService) PromoStatus(cityID int64) (inserting bool, loop bool, remainingSecs int, startedAt time.Time, videoDuration int) {
	s.mu.RLock()
	sess, ok := s.sessions[cityID]
	s.mu.RUnlock()
	if !ok {
		return false, false, 0, time.Time{}, 0
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.injectPromo == nil {
		return false, false, 0, time.Time{}, 0
	}
	if sess.promoLoop {
		// 循环模式：计算本轮剩余
		elapsed := int(time.Since(sess.promoStartedAt).Seconds())
		dur := sess.promoVideoDuration
		if dur <= 0 {
			return true, true, -1, sess.promoStartedAt, dur
		}
		rem := dur - (elapsed % dur)
		return true, true, rem, sess.promoStartedAt, dur
	}
	// 单次模式：总时长 - 已过时间
	elapsed := int(time.Since(sess.promoStartedAt).Seconds())
	rem := sess.promoVideoDuration - elapsed
	if rem <= 0 {
		// 时间已到，视为不在插播状态（watchdogPromo 很快会设 injectPromo=nil）
		return false, false, 0, time.Time{}, 0
	}
	return true, false, rem, sess.promoStartedAt, sess.promoVideoDuration
}

// StopPromoInsert 手动停止插播（用于循环模式）
func (s *FFmpegService) StopPromoInsert(cityID int64) error {
	s.mu.RLock()
	sess, ok := s.sessions[cityID]
	s.mu.RUnlock()
	if !ok {
		return fmt.Errorf("city %d: no active session", cityID)
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.injectPromo == nil {
		return fmt.Errorf("no promo playing")
	}
	sess.injectPromo.discarded = true
	s.killProc(sess.injectPromo)
	return nil
}

// SetMute 切换静音状态：以 gain=0（静音）或原始 gain 重启 inject 进程，push 进程不受影响
func (s *FFmpegService) SetMute(cityID int64, muted bool) error {
	s.mu.RLock()
	sess, ok := s.sessions[cityID]
	s.mu.RUnlock()
	if !ok {
		return fmt.Errorf("city %d: no active session", cityID)
	}

	cfg, err := s.loadStreamConfig(cityID)
	if err != nil {
		return err
	}
	if sess.currentSourceURL == "" {
		return fmt.Errorf("city %d: source URL not recorded (not a direct push?)", cityID)
	}

	// 临时覆盖 VolumeGain：静音时置 0，恢复时用 DB 原值
	if muted {
		cfg.VolumeGain = 0
	}
	item := &model.ScheduleItem{
		ItemType:     "live_stream",
		StreamSource: &model.StreamSource{URL: sess.currentSourceURL},
	}
	newArgs, err := s.buildInjectArgs(cfg, item)
	if err != nil {
		return err
	}

	sess.mu.Lock()
	defer sess.mu.Unlock()
	old := sess.inject
	old.discarded = true // 通知 watchdog 不要重启旧进程
	if err := s.startProc(sess, procTypeInject, newArgs); err != nil {
		return err
	}
	s.killProc(old)
	sess.isMuted = muted
	return nil
}

// StartPromoInsert 启动宣传片插播（播放一次后自动退出）
// 插播期间先暂停 inject 进程释放 SRS 推流槽，播完后恢复
func (s *FFmpegService) StartPromoInsert(cityID int64, promoVideoID int64, loop bool) error {
	s.mu.RLock()
	sess, ok := s.sessions[cityID]
	s.mu.RUnlock()
	if !ok {
		return fmt.Errorf("city %d: no active session", cityID)
	}

	sess.mu.Lock()
	defer sess.mu.Unlock()

	if sess.injectPromo != nil {
		return fmt.Errorf("promo already playing")
	}

	// 1. 查询宣传片路径和时长
	var transcodedPath string
	var cityCode string
	var videoDuration int
	err := s.db.QueryRow(
		`SELECT pv.transcoded_path, c.code, COALESCE(pv.duration_seconds, 0)
		   FROM promotional_videos pv
		   JOIN cities c ON pv.city_id = c.id
		  WHERE pv.id=? AND pv.city_id=? AND pv.transcode_status='done'`,
		promoVideoID, cityID,
	).Scan(&transcodedPath, &cityCode, &videoDuration)
	if err != nil {
		return fmt.Errorf("promo video not found or not transcoded")
	}

	// 2. 暂停 inject 进程，释放 SRS 推流槽给宣传片
	oldInject := sess.inject
	if oldInject != nil {
		oldInject.discarded = true // 不让 watchdog 自动重启
		s.killProc(oldInject)
		sess.inject = nil
	}

	// 3. 构建 inject-promo 命令（播放 1 次）
	srsURL := fmt.Sprintf("rtmp://%s:%s/live/%s", s.cfg.SRSHost, s.cfg.SRSRTMPPort, cityCode)
	var args []string
	if loop {
		args = ffscript.InjectPromoLoopArgs(transcodedPath, srsURL)
	} else {
		args = ffscript.InjectPromoOnceArgs(transcodedPath, srsURL)
	}

	// 4. 启动 inject-promo 进程
	if err := s.startProc(sess, procTypeInjectPromo, args); err != nil {
		// 启动失败，立即恢复 inject 进程
		if oldInject != nil {
			_ = s.startProc(sess, procTypeInject, oldInject.args)
		}
		return fmt.Errorf("start inject-promo: %w", err)
	}

	// 记录插播元数据（用于倒计时）
	sess.promoStartedAt = time.Now()
	sess.promoVideoDuration = videoDuration
	sess.promoLoop = loop

	// 5. 启动专用 watchdog（播完自动退出，恢复 inject）
	go s.watchdogPromo(sess, sess.injectPromo, oldInject)

	s.logger.Printf("city=%d promo insert started (video_id=%d, loop=%v)", cityID, promoVideoID, loop)
	return nil
}

// watchdogPromo 监控宣传片插播进程（播完自动退出，恢复 inject）
func (s *FFmpegService) watchdogPromo(sess *citySession, mp *managedProc, oldInject *managedProc) {
	err := mp.cmd.Wait()
	close(mp.done)

	sess.mu.Lock()
	defer sess.mu.Unlock()

	sess.injectPromo = nil

	// 无论正常还是异常退出，都恢复 inject 进程
	if oldInject != nil && sess.ctx.Err() == nil {
		if startErr := s.startProc(sess, procTypeInject, oldInject.args); startErr != nil {
			s.logger.Printf("city=%d promo: restore inject failed: %v", sess.cityID, startErr)
		} else {
			go s.watchdog(sess, sess.inject)
			s.logger.Printf("city=%d promo: inject restored", sess.cityID)
		}
	}

	if mp.cmd.ProcessState != nil && mp.cmd.ProcessState.ExitCode() == 0 {
		s.logger.Printf("city=%d promo insert finished normally", sess.cityID)
		return
	}

	// 被 StopPromoInsert 主动 kill（discarded=true），不算异常
	if mp.discarded {
		s.logger.Printf("city=%d promo insert stopped manually", sess.cityID)
		return
	}

	// 异常退出，记录告警
	reason := classifyFFmpegErr(err, mp.stderrBuf.LastMeaningful())
	_ = s.logAlert(sess.cityID, "warn", fmt.Sprintf("宣传片插播异常退出：%s", reason))
	s.logger.Printf("city=%d promo insert failed: %v", sess.cityID, err)
}

// ── 进程启动与守护 ──────────────────────────────────────────────

// startProc 启动一个 FFmpeg 子进程并注册 watchdog goroutine
func (s *FFmpegService) startProc(sess *citySession, procType string, args []string) error {
	cmd := exec.CommandContext(sess.ctx, s.cfg.FFmpegBin, args...)
	cmd.Stdout = os.Stdout
	ring := &stderrRing{max: 30}
	cmd.Stderr = io.MultiWriter(os.Stderr, ring) // tee: 容器日志 + 环形缓冲供错误诊断

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("exec start [%s]: %w", procType, err)
	}

	// 降低 FFmpeg 进程的 OOM 优先级，避免系统内存紧张时被 OOM Killer 优先 kill
	// oom_score_adj 范围 -1000（永不 kill）到 1000（优先 kill），默认 0
	// -500 表示比普通进程更难被 kill，同时不影响系统在极端情况下的回收能力
	if pid := cmd.Process.Pid; pid > 0 {
		oomPath := fmt.Sprintf("/proc/%d/oom_score_adj", pid)
		if err := os.WriteFile(oomPath, []byte("-500"), 0644); err != nil {
			s.logger.Printf("WARN: set oom_score_adj for pid %d failed: %v", pid, err)
		}
	}

	mp := &managedProc{
		cmd:       cmd,
		args:      args,
		procType:  procType,
		done:      make(chan struct{}),
		stderrBuf: ring,
	}

	switch procType {
	case procTypeInject:
		sess.inject = mp
	case procTypePush:
		sess.push = mp
	case procTypeInjectPromo:
		sess.injectPromo = mp
	}

	// 宣传片插播进程由 watchdogPromo 单独管理，不走标准 watchdog
	if procType != procTypeInjectPromo {
		go s.watchdog(sess, mp)
	}
	s.logger.Printf("city=%d [%s] started pid=%d", sess.cityID, procType, cmd.Process.Pid)
	return nil
}

// watchdog 监控进程退出，并根据熔断规则决定是否重启
func (s *FFmpegService) watchdog(sess *citySession, mp *managedProc) {
	err := mp.cmd.Wait()
	close(mp.done)

	// 如果 context 已取消（正常停止）或进程已被主动替换，不重启
	if sess.ctx.Err() != nil || mp.discarded {
		return
	}

	sess.mu.Lock()
	sess.retries++
	retries := sess.retries
	sess.mu.Unlock()

	s.logger.Printf("city=%d [%s] exited (err=%v), retries=%d/%d",
		sess.cityID, mp.procType, err, retries, s.cfg.FFmpegMaxRetries)
	reason := classifyFFmpegErr(err, mp.stderrBuf.LastMeaningful())
	_ = s.logAlert(sess.cityID, "warn",
		fmt.Sprintf("[%s] 进程退出 (重试 %d/%d)：%s",
			mp.procType, retries, s.cfg.FFmpegMaxRetries, reason))

	// 熔断检查
	if retries >= s.cfg.FFmpegMaxRetries {
		s.triggerCircuitBreaker(sess, mp.procType)
		return
	}

	// 延迟重启
	select {
	case <-sess.ctx.Done():
		return
	case <-time.After(time.Duration(s.cfg.FFmpegRetryDelaySec) * time.Second):
	}

	s.logger.Printf("city=%d [%s] restarting...", sess.cityID, mp.procType)
	cfg, err2 := s.loadStreamConfig(sess.cityID)
	if err2 != nil {
		s.logger.Printf("city=%d reload config failed: %v", sess.cityID, err2)
		return
	}

	var newArgs []string
	switch mp.procType {
	case procTypePush:
		newArgs = s.buildPushArgs(cfg)
	case procTypeInject:
		// 重启 inject 时用相同参数（保持当前排期条目）
		newArgs = mp.args
	}

	_ = s.updateDBStatus(sess.cityID, sess.scheduleID, "streaming", 0, retries)

	sess.mu.Lock()
	defer sess.mu.Unlock()
	_ = s.startProc(sess, mp.procType, newArgs)
}

// triggerCircuitBreaker 连续失败达到阈值，触发熔断
func (s *FFmpegService) triggerCircuitBreaker(sess *citySession, procType string) {
	s.logger.Printf("city=%d CIRCUIT BREAKER OPEN (%s failed %d times)",
		sess.cityID, procType, s.cfg.FFmpegMaxRetries)

	_ = s.updateDBStatus(sess.cityID, sess.scheduleID, "breaker_open", 0, sess.retries)
	// 同步将排期状态改回 stopped，允许用户重新启动
	if sess.scheduleID > 0 {
		_, _ = s.db.Exec(`UPDATE schedules SET status='stopped' WHERE id=?`, sess.scheduleID)
	}

	// 从最后一个失败进程的 stderr 中提取原因
	var lastLine string
	if procType == procTypePush && sess.push != nil && sess.push.stderrBuf != nil {
		lastLine = sess.push.stderrBuf.LastMeaningful()
	} else if procType == procTypeInject && sess.inject != nil && sess.inject.stderrBuf != nil {
		lastLine = sess.inject.stderrBuf.LastMeaningful()
	}
	reason := classifyFFmpegErr(nil, lastLine)

	procName := map[string]string{
		procTypePush:   "推流进程（中台→视频号）",
		procTypeInject: "注入进程（直播源→中台）",
	}[procType]
	msg := fmt.Sprintf(
		"【苏超直播告警】%s 连续失败 %d 次，城市 ID=%d。故障原因：%s。"+
			"请检查推流密钥/网络/直播源后点「清除熔断」重启。",
		procName, s.cfg.FFmpegMaxRetries, sess.cityID, reason)
	_ = s.logAlert(sess.cityID, "critical", msg)

	// 发送短信告警（异步，不阻塞）
	go s.sms.SendCityAlert(sess.cityID, msg)

	// 停止所有进程，清除会话
	s.mu.Lock()
	s.killProc(sess.inject)
	s.killProc(sess.push)
	sess.cancel()
	delete(s.sessions, sess.cityID)
	s.mu.Unlock()
}

// killProc 优雅地终止进程（SIGTERM → 等待 3s → SIGKILL）
func (s *FFmpegService) killProc(mp *managedProc) {
	if mp == nil || mp.cmd == nil || mp.cmd.Process == nil {
		return
	}
	// FFmpeg 收到 SIGTERM 会完成当前帧然后退出
	_ = mp.cmd.Process.Signal(os.Interrupt)
	select {
	case <-mp.done:
	case <-time.After(3 * time.Second):
		_ = mp.cmd.Process.Kill()
	}
}

// ── FFmpeg 命令构建 ─────────────────────────────────────────────

type streamCfg struct {
	PushURL    *string
	PushKey    *string
	VolumeGain float64
	SRSApp     string
	SRSStream  string
}

func (s *FFmpegService) loadStreamConfig(cityID int64) (*streamCfg, error) {
	row := s.db.QueryRow(
		`SELECT push_url, push_key, volume_gain, srs_app, srs_stream
		   FROM stream_configs WHERE city_id=?`, cityID)
	c := &streamCfg{}
	if err := row.Scan(&c.PushURL, &c.PushKey, &c.VolumeGain, &c.SRSApp, &c.SRSStream); err != nil {
		return nil, err
	}
	return c, nil
}

// buildPushArgs 构建推流进程参数：SRS → 视频号
// 使用 ffscript 包，所有参数说明见 pkg/ffscript/generator.go
func (s *FFmpegService) buildPushArgs(cfg *streamCfg) []string {
	srsURL := ffscript.SRSStreamURL(s.cfg.SRSHost, s.cfg.SRSRTMPPort, cfg.SRSApp, cfg.SRSStream)
	destURL := ffscript.PushDestURL(*cfg.PushURL, *cfg.PushKey)
	args := ffscript.PushArgs(srsURL, destURL)
	s.logger.Printf("push cmd: %s", ffscript.CmdString(s.cfg.FFmpegBin, args))
	return args
}

// buildInjectArgs 根据排期条目构建注入进程参数
// 使用 ffscript 包，所有参数说明见 pkg/ffscript/generator.go
func (s *FFmpegService) buildInjectArgs(cfg *streamCfg, item *model.ScheduleItem) ([]string, error) {
	srsTarget := ffscript.SRSStreamURL(s.cfg.SRSHost, s.cfg.SRSRTMPPort, cfg.SRSApp, cfg.SRSStream)

	var args []string
	switch item.ItemType {
	case "promo_video":
		if item.PromoVideo == nil || item.PromoVideo.TranscodedPath == nil {
			return nil, fmt.Errorf("promo video not ready (id=%v)", item.PromoVideoID)
		}
		args = ffscript.InjectPromoArgs(*item.PromoVideo.TranscodedPath, srsTarget)

	case "live_stream":
		if item.StreamSource == nil {
			return nil, fmt.Errorf("stream source not loaded (id=%v)", item.StreamSourceID)
		}
		args = ffscript.InjectLiveArgs(item.StreamSource.URL, srsTarget, cfg.VolumeGain)

	default:
		return nil, fmt.Errorf("unknown item_type: %s", item.ItemType)
	}

	s.logger.Printf("inject cmd: %s", ffscript.CmdString(s.cfg.FFmpegBin, args))
	return args, nil
}

// seamlessSwitch 无缝切换 inject 源
//
// 切换流程：
//  1. 启动新 inject 进程（与旧进程共存约 3s）
//  2. 通过 SRS API 轮询，确认新流已进入 publishing 状态
//  3. SIGTERM 旧 inject 进程（此时 SRS 已断开旧连接，旧进程自然退出）
//  4. push 进程全程不停，SRS GOP 缓存（2s）覆盖切换窗口
//
// 相比直接 kill 旧进程再启动新进程：
//   旧方式：kill → 新进程建连延迟 → push 端断流 1-3s
//   新方式：两进程短暂共存 → SRS 选择新连接 → 旧进程退出 → push 无感
func (s *FFmpegService) seamlessSwitch(sess *citySession, newArgs []string, srsApp, srsStream string) error {
	// 1. 启动新 inject 进程
	if err := s.startProc(sess, procTypeInject, newArgs); err != nil {
		return fmt.Errorf("start new inject: %w", err)
	}

	// 2. 等待 SRS 确认新流 publishing（最多等待 8s）
	ctx, cancel := context.WithTimeout(sess.ctx, 8*time.Second)
	defer cancel()

	s.logger.Printf("city=%d waiting for SRS stream %s/%s to be publishing...", sess.cityID, srsApp, srsStream)
	if err := s.srs.WaitUntilPublishing(ctx, srsApp, srsStream, 8*time.Second, 200*time.Millisecond); err != nil {
		// SRS 确认超时不是致命错误（SRS 可能短暂不可达），继续切流
		s.logger.Printf("city=%d WARN SRS confirm timeout: %v (proceeding anyway)", sess.cityID, err)
	} else {
		s.logger.Printf("city=%d SRS confirmed new stream publishing", sess.cityID)
	}

	return nil
}

// ── DB 写回辅助 ─────────────────────────────────────────────────

func (s *FFmpegService) updateDBStatus(cityID, scheduleID int64, status string, currentItemID int64, retryCount int) error {
	args := []interface{}{status, retryCount, time.Now(), cityID}
	q := `UPDATE ffmpeg_processes
	         SET status=?, retry_count=?, updated_at=?`
	if scheduleID > 0 {
		q += `, schedule_id=?`
		args = append(args[:3], append([]interface{}{scheduleID}, args[3:]...)...)
	}
	if currentItemID > 0 {
		q += `, current_item_id=?`
		args = append(args, currentItemID)
	}
	// 推流开始时记录本地时间（用 SQLite localtime，避免 UTC/东八区混用导致前端计时器错乱）
	if status == "streaming" {
		q += `, last_started_at=datetime('now','localtime')`
	}
	q += ` WHERE city_id=?`
	_, err := s.db.Exec(q, args...)
	return err
}

func (s *FFmpegService) updateCurrentItem(cityID, itemID int64) error {
	_, err := s.db.Exec(
		`UPDATE ffmpeg_processes SET current_item_id=?, updated_at=? WHERE city_id=?`,
		itemID, time.Now(), cityID)
	return err
}

func (s *FFmpegService) logAlert(cityID int64, level, message string) error {
	_, err := s.db.Exec(
		`INSERT INTO alert_logs (city_id, level, message) VALUES (?,?,?)`,
		cityID, level, message)
	return err
}
