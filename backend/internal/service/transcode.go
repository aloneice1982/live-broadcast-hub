// Package service — transcode.go
//
// 宣传片离线标准化转码 Service。
//
// 工作原理：
//   管理员上传 MP4 → 存入 uploads/ → 写入 DB (status=pending)
//   → EnqueueVideo(id) 放入 channel
//   → 最多 N 个 worker goroutine 并发执行 FFmpeg 转码
//   → 转码成功：transcoded_path 填入，status=done
//   → 转码失败：status=failed，error 写入 DB
//
// 目标格式（与上游直播流对齐，保证 SRS 无缝切换）：
//   视频: H.264 Main Profile, 1920x1080, 25fps, GOP=50(2s)
//   音频: AAC, 128k, 44100Hz, 双声道

package service

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"susuper/internal/config"
)

// TranscodeService 管理宣传片的离线转码队列
type TranscodeService struct {
	db    *sql.DB
	cfg   *config.Config
	queue chan int64     // 待转码的 video ID
	sem   chan struct{}  // 并发控制信号量
}

// NewTranscodeService 创建并启动后台 worker 协程
func NewTranscodeService(db *sql.DB, cfg *config.Config) *TranscodeService {
	s := &TranscodeService{
		db:    db,
		cfg:   cfg,
		queue: make(chan int64, 200),
		sem:   make(chan struct{}, cfg.TranscodeConcurrency),
	}
	go s.run()
	return s
}

// EnqueueVideo 将 videoID 放入转码队列（非阻塞，queue 满时返回错误）
func (s *TranscodeService) EnqueueVideo(videoID int64) error {
	select {
	case s.queue <- videoID:
		return nil
	default:
		return fmt.Errorf("transcode queue is full, try again later")
	}
}

// run 是后台 dispatcher goroutine，持续消费 queue
func (s *TranscodeService) run() {
	for videoID := range s.queue {
		s.sem <- struct{}{} // 占用一个 worker 槽
		go func(id int64) {
			defer func() { <-s.sem }()
			s.process(id)
		}(videoID)
	}
}

// process 执行单个宣传片的标准化转码
func (s *TranscodeService) process(videoID int64) {
	ctx := context.Background()
	logger := log.New(os.Stdout, fmt.Sprintf("[transcode vid=%d] ", videoID), log.LstdFlags)

	// 1. 从 DB 读取视频信息
	video, err := s.fetchVideo(ctx, videoID)
	if err != nil {
		logger.Printf("ERROR fetch video: %v", err)
		return
	}
	if video.TranscodeStatus != "pending" {
		logger.Printf("skip: status is %s", video.TranscodeStatus)
		return
	}

	// 2. 标记为 processing
	if err := s.updateStatus(ctx, videoID, "processing", nil); err != nil {
		logger.Printf("ERROR mark processing: %v", err)
		return
	}

	// 3. 确定输出路径
	outFilename := strings.TrimSuffix(video.StoredFilename, filepath.Ext(video.StoredFilename)) + "_std.mp4"
	outPath := filepath.Join(s.cfg.TranscodeDir, fmt.Sprintf("%d", video.CityID), outFilename)
	if err := os.MkdirAll(filepath.Dir(outPath), 0755); err != nil {
		s.failVideo(ctx, videoID, fmt.Sprintf("mkdir: %v", err))
		return
	}

	// 4. 执行 FFmpeg 转码
	logger.Printf("start: %s → %s", video.UploadPath, outPath)
	startAt := time.Now()

	args := s.buildTranscodeArgs(video.UploadPath, outPath)
	cmd := exec.CommandContext(ctx, s.cfg.FFmpegBin, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		errMsg := fmt.Sprintf("ffmpeg exit: %v\noutput:\n%s", err, truncate(string(output), 1000))
		logger.Printf("ERROR %s", errMsg)
		// 清理残留的不完整输出文件
		_ = os.Remove(outPath)
		s.failVideo(ctx, videoID, errMsg)
		return
	}

	elapsed := time.Since(startAt).Round(time.Second)
	logger.Printf("done in %s", elapsed)

	// 5. 探测转码后的时长
	duration, err := s.probeDuration(outPath)
	if err != nil {
		logger.Printf("WARN probe duration: %v", err)
	}

	// 5b. 截取第1秒封面缩略图（失败不阻断主流程）
	thumbPath := strings.TrimSuffix(outPath, ".mp4") + "_thumb.jpg"
	if err := exec.Command(s.cfg.FFmpegBin,
		"-ss", "1", "-i", outPath,
		"-frames:v", "1", "-q:v", "3", "-y", thumbPath,
	).Run(); err != nil {
		logger.Printf("WARN thumbnail: %v", err)
		thumbPath = ""
	}

	// 6. 写回 DB
	if err := s.markDone(ctx, videoID, outPath, thumbPath, duration); err != nil {
		logger.Printf("ERROR mark done: %v", err)
	}
}

// buildTranscodeArgs 生成标准化转码的 FFmpeg 参数
//
// 目标：与上游直播流格式高度一致，保证 SRS GOP 缓存可无缝切换
//   -c:v libx264 -preset veryfast -profile:v main   → H.264 Main，软件编码
//   -vf scale=1920x1080,fps=25                       → 分辨率和帧率统一
//   -g 50 -keyint_min 25 -sc_threshold 0             → 固定 2s GOP，禁止场景切换强制关键帧
//   -c:a aac -b:a 128k -ar 44100 -ac 2              → 音频统一格式
//   -movflags +faststart                             → MP4 头前置，方便直接 ffmpeg -re 读取
func (s *TranscodeService) buildTranscodeArgs(inputPath, outputPath string) []string {
	fps := s.cfg.TranscodeFPS
	gop := s.cfg.TranscodeGOP
	res := s.cfg.TranscodeResolution

	return []string{
		"-i", inputPath,
		// 视频
		"-c:v", "libx264",
		"-preset", "veryfast",
		"-profile:v", "main",
		"-level", "4.0",
		"-vf", fmt.Sprintf("scale=%s,fps=%s", strings.ReplaceAll(res, "x", ":"), fps),
		"-g", gop,
		"-keyint_min", gop,
		"-sc_threshold", "0", // 禁止场景切换强制关键帧
		"-b:v", "2500k",
		"-maxrate", "3000k",
		"-bufsize", "6000k",
		// 音频
		"-c:a", "aac",
		"-b:a", s.cfg.TranscodeAudioBitrate,
		"-ar", s.cfg.TranscodeAudioRate,
		"-ac", "2",
		// 容器
		"-movflags", "+faststart",
		"-y",       // 覆盖已有文件
		outputPath,
	}
}

// probeDuration 使用 ffprobe 获取视频时长（秒）
func (s *TranscodeService) probeDuration(filePath string) (int, error) {
	out, err := exec.Command("ffprobe",
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		filePath,
	).Output()
	if err != nil {
		return 0, err
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		return 0, err
	}
	return int(f), nil
}

// ── DB 辅助方法 ────────────────────────────────────────────────

type videoRow struct {
	CityID          int64
	UploadPath      string
	StoredFilename  string
	TranscodeStatus string
}

func (s *TranscodeService) fetchVideo(ctx context.Context, id int64) (*videoRow, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT city_id, upload_path, stored_filename, transcode_status
		   FROM promotional_videos WHERE id = ?`, id)
	var v videoRow
	if err := row.Scan(&v.CityID, &v.UploadPath, &v.StoredFilename, &v.TranscodeStatus); err != nil {
		return nil, err
	}
	return &v, nil
}

func (s *TranscodeService) updateStatus(ctx context.Context, id int64, status string, errMsg *string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE promotional_videos SET transcode_status=?, transcode_error=? WHERE id=?`,
		status, errMsg, id)
	return err
}

func (s *TranscodeService) failVideo(ctx context.Context, id int64, reason string) {
	_ = s.updateStatus(ctx, id, "failed", &reason)
}

func (s *TranscodeService) markDone(ctx context.Context, id int64, outPath, thumbPath string, duration int) error {
	var thumbVal interface{}
	if thumbPath != "" {
		thumbVal = thumbPath
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE promotional_videos
		    SET transcode_status='done', transcoded_path=?, thumbnail_path=?,
		        duration_seconds=?, transcode_error=NULL
		  WHERE id=?`,
		outPath, thumbVal, duration, id)
	return err
}

// truncate 防止错误信息超长
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "...(truncated)"
}
