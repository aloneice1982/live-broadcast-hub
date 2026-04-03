// Package service — scheduler.go
//
// 时间轴调度引擎。
//
// 工作原理：
//   RunSchedule(cityID, scheduleID) 启动一个 goroutine
//   按照 schedule_items.start_time 计算今天的目标时刻
//   time.Sleep 等待 → 调用 FFmpegService.SwitchSource 切换注入源
//   直到所有条目��行完毕或被手动停止

package service

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"susuper/internal/model"
)

// SchedulerService 负责按时间轴触发切流
type SchedulerService struct {
	db           *sql.DB
	ffmpeg       *FFmpegService
	cancels      sync.Map // cityID int64 → context.CancelFunc
	manualMu     sync.Mutex
	manualStates map[int64]*manualState // cityID → 手动模式状态
	logger       *log.Logger
}

// manualState 手动模式下保存已加载的条目列表和当前索引
type manualState struct {
	scheduleID int64
	items      []model.ScheduleItem
	curIdx     int
}

// NewSchedulerService 创建调度器
func NewSchedulerService(db *sql.DB, ffmpeg *FFmpegService) *SchedulerService {
	return &SchedulerService{
		db:           db,
		ffmpeg:       ffmpeg,
		manualStates: make(map[int64]*manualState),
		logger:       log.New(os.Stdout, "[scheduler] ", log.LstdFlags),
	}
}

// RunSchedule 启动某地市今日排期的执行
// 先加载所有条目，标记排期为 running，然后在 goroutine 中等待第一个条目的
// start_time 到达后再启动 FFmpeg，后续条目按时间轴驱动切流
func (s *SchedulerService) RunSchedule(cityID, scheduleID int64, forceNow bool) error {
	// 防止重复启动
	if _, loaded := s.cancels.Load(cityID); loaded {
		return fmt.Errorf("city %d scheduler already running", cityID)
	}

	items, err := s.loadItems(scheduleID)
	if err != nil {
		return fmt.Errorf("load schedule items: %w", err)
	}
	if len(items) == 0 {
		return fmt.Errorf("schedule %d has no items", scheduleID)
	}

	// 更新排期状态为 running（此时 ffmpeg 尚未启动，等待时间到）
	if err := s.updateScheduleStatus(scheduleID, "running"); err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.cancels.Store(cityID, cancel)

	go func() {
		defer func() {
			s.cancels.Delete(cityID)
			cancel()
		}()

		// 若非立即启动，等待第一个条目的 start_time
		if !forceNow {
			if t, err := parseScheduleTime(items[0].StartTime); err == nil {
				if wait := time.Until(t); wait > 0 {
					s.logger.Printf("city=%d item=%d waiting %.1fs until %s before first start",
						cityID, items[0].ID, wait.Seconds(), t.Format("15:04:05"))
					select {
					case <-ctx.Done():
						_ = s.updateScheduleStatus(scheduleID, "stopped")
						return
					case <-time.After(wait):
					}
				}
			}
		} else {
			s.logger.Printf("city=%d item=%d forceNow=true, starting immediately", cityID, items[0].ID)
		}

		// 启动第一个条目的推流
		if err := s.ffmpeg.StartCity(cityID, scheduleID, &items[0]); err != nil {
			s.cancels.Delete(cityID)
			_ = s.updateScheduleStatus(scheduleID, "stopped")
			s.logger.Printf("city=%d start city stream error: %v", cityID, err)
			return
		}

		s.run(ctx, cityID, scheduleID, items)
	}()

	return nil
}

// StopSchedule 手动停止某地市的排期执行
func (s *SchedulerService) StopSchedule(cityID, scheduleID int64) {
	if v, ok := s.cancels.Load(cityID); ok {
		v.(context.CancelFunc)()
		s.cancels.Delete(cityID)
	}
	s.ffmpeg.StopCity(cityID)
	_ = s.updateScheduleStatus(scheduleID, "stopped")
	// 清理手动模式状态
	s.manualMu.Lock()
	delete(s.manualStates, cityID)
	s.manualMu.Unlock()
	s.logger.Printf("city=%d schedule=%d stopped", cityID, scheduleID)
}

// RunScheduleManual 手动模式：立即启动第一条目，不等待时间，等管理员手动推进
func (s *SchedulerService) RunScheduleManual(cityID, scheduleID int64) error {
	if _, loaded := s.cancels.Load(cityID); loaded {
		return fmt.Errorf("city %d scheduler already running", cityID)
	}

	items, err := s.loadItems(scheduleID)
	if err != nil {
		return fmt.Errorf("load schedule items: %w", err)
	}
	if len(items) == 0 {
		return fmt.Errorf("schedule %d has no items", scheduleID)
	}

	if err := s.updateScheduleStatus(scheduleID, "running"); err != nil {
		return err
	}

	if err := s.ffmpeg.StartCity(cityID, scheduleID, &items[0]); err != nil {
		_ = s.updateScheduleStatus(scheduleID, "draft")
		return fmt.Errorf("start city stream: %w", err)
	}

	s.manualMu.Lock()
	s.manualStates[cityID] = &manualState{
		scheduleID: scheduleID,
		items:      items,
		curIdx:     0,
	}
	s.manualMu.Unlock()

	s.logger.Printf("city=%d schedule=%d manual mode started (item 0/%d)", cityID, scheduleID, len(items))
	return nil
}

// AdvanceSchedule 手动模式下切到下一条目
func (s *SchedulerService) AdvanceSchedule(cityID int64) error {
	s.manualMu.Lock()
	st, ok := s.manualStates[cityID]
	if !ok {
		s.manualMu.Unlock()
		return fmt.Errorf("city %d is not in manual mode", cityID)
	}
	st.curIdx++
	if st.curIdx >= len(st.items) {
		// 所有条目已播完，结束
		scheduleID := st.scheduleID
		delete(s.manualStates, cityID)
		s.manualMu.Unlock()
		s.ffmpeg.StopCity(cityID)
		_ = s.updateScheduleStatus(scheduleID, "finished")
		s.logger.Printf("city=%d manual schedule=%d finished (all items played)", cityID, scheduleID)
		return nil
	}
	item := st.items[st.curIdx]
	s.manualMu.Unlock()

	s.logger.Printf("city=%d manual advance to item=%d (%s)", cityID, item.ID, item.ItemType)
	return s.ffmpeg.SwitchSource(cityID, &item)
}

// HasMoreManualItems 返回手动模式下是否还有下一条目
func (s *SchedulerService) HasMoreManualItems(cityID int64) bool {
	s.manualMu.Lock()
	defer s.manualMu.Unlock()
	st, ok := s.manualStates[cityID]
	if !ok {
		return false
	}
	return st.curIdx < len(st.items)-1
}

// IsRunning 返回该地市的调度 goroutine 是否还在运行（包括在等待 start_time 阶段）
func (s *SchedulerService) IsRunning(cityID int64) bool {
	_, ok := s.cancels.Load(cityID)
	return ok
}

// run 是调度主循环（在 goroutine 中运行）
func (s *SchedulerService) run(ctx context.Context, cityID, scheduleID int64, items []model.ScheduleItem) {
	defer func() {
		s.cancels.Delete(cityID)
		s.ffmpeg.StopCity(cityID)
		_ = s.updateScheduleStatus(scheduleID, "finished")
		s.logger.Printf("city=%d schedule=%d finished", cityID, scheduleID)
	}()

	// 第一个条目已在调用方等待并启动，从第二个开始等待切换
	for i := 1; i < len(items); i++ {
		item := items[i]
		targetTime, err := parseScheduleTime(item.StartTime)
		if err != nil {
			s.logger.Printf("city=%d skip item=%d bad time %q: %v", cityID, item.ID, item.StartTime, err)
			continue
		}

		waitDur := time.Until(targetTime)
		if waitDur < 0 {
			// 已经过了这个时间点，立刻执行
			s.logger.Printf("city=%d item=%d start_time %s already passed, switching now",
				cityID, item.ID, item.StartTime)
			waitDur = 0
		} else {
			s.logger.Printf("city=%d item=%d waiting %.1fs until %s",
				cityID, item.ID, waitDur.Seconds(), targetTime.Format("15:04:05"))
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(waitDur):
		}

		s.logger.Printf("city=%d switching to item=%d (%s) at %s",
			cityID, item.ID, item.ItemType, time.Now().Format("15:04:05"))
		if err := s.ffmpeg.SwitchSource(cityID, &item); err != nil {
			s.logger.Printf("city=%d SwitchSource error: %v", cityID, err)
		}
	}

	// 最后一个条目执行完毕后等待，直到被手动停止或 context 取消
	s.logger.Printf("city=%d all items dispatched, waiting for stop signal", cityID)
	<-ctx.Done()
}

// ── DB 辅助 ─────────────────────────────────────────────────────

// loadItems 加载某排期的所有条目（含关联的宣传片和直播源）
func (s *SchedulerService) loadItems(scheduleID int64) ([]model.ScheduleItem, error) {
	rows, err := s.db.Query(`
		SELECT
			si.id, si.schedule_id, si.order_index, si.item_type,
			si.promo_video_id, si.loop_count,
			si.stream_source_id, si.start_time,
			-- promo video fields
			pv.transcoded_path, pv.transcode_status,
			-- stream source fields
			ss.url
		FROM schedule_items si
		LEFT JOIN promotional_videos pv ON pv.id = si.promo_video_id
		LEFT JOIN stream_sources ss     ON ss.id = si.stream_source_id
		WHERE si.schedule_id = ?
		ORDER BY si.order_index ASC`, scheduleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []model.ScheduleItem
	for rows.Next() {
		var it model.ScheduleItem
		var transcodedPath sql.NullString
		var transcodeStatus sql.NullString
		var streamURL sql.NullString

		if err := rows.Scan(
			&it.ID, &it.ScheduleID, &it.OrderIndex, &it.ItemType,
			&it.PromoVideoID, &it.LoopCount,
			&it.StreamSourceID, &it.StartTime,
			&transcodedPath, &transcodeStatus,
			&streamURL,
		); err != nil {
			return nil, err
		}

		// 填充关联数据
		if it.ItemType == "promo_video" && it.PromoVideoID != nil {
			if transcodeStatus.String != "done" {
				return nil, fmt.Errorf("promo video id=%d is not transcoded yet (status=%s)",
					*it.PromoVideoID, transcodeStatus.String)
			}
			it.PromoVideo = &model.PromotionalVideo{
				TranscodeStatus: transcodeStatus.String,
			}
			if transcodedPath.Valid {
				it.PromoVideo.TranscodedPath = &transcodedPath.String
			}
		}
		if it.ItemType == "live_stream" && it.StreamSourceID != nil && streamURL.Valid {
			it.StreamSource = &model.StreamSource{URL: streamURL.String}
		}

		items = append(items, it)
	}
	return items, rows.Err()
}

func (s *SchedulerService) updateScheduleStatus(scheduleID int64, status string) error {
	_, err := s.db.Exec(
		`UPDATE schedules SET status=?, updated_at=? WHERE id=?`,
		status, time.Now(), scheduleID)
	return err
}

// parseScheduleTime 将 "HH:MM" 解析为今天的 time.Time（本地时区）
func parseScheduleTime(hhmm string) (time.Time, error) {
	now := time.Now()
	t, err := time.ParseInLocation("2006-01-02 15:04",
		fmt.Sprintf("%s %s", now.Format("2006-01-02"), hhmm), time.Local)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse %q: %w", hhmm, err)
	}
	return t, nil
}
