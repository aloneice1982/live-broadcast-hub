// Package srsapi — SRS HTTP API 客户端
//
// SRS（Simple Realtime Server）提供 HTTP API 用于查询流状态。
// 文档：https://ossrs.io/lts/en-us/docs/v5/doc/http-api
//
// 本包封装以下常用接口：
//   GET /api/v1/versions      → 版本信息（用于健康检查）
//   GET /api/v1/streams       → 列出所有活跃流
//   GET /api/v1/clients       → 列出所有客户端（推流者 + 拉流者）
//
// 典型使用场景（无缝切流）：
//   1. 旧 inject 进程正在推 live/tz
//   2. 新 inject 进程启动，也推 live/tz
//   3. SRS 断开旧连接（同一 stream key 只允许一路发布者）
//   4. 调用 WaitUntilPublishing(ctx, "live", "tz") 确认新流就绪
//   5. 推流切换完成，push-ffmpeg 的 reconnect 自动恢复

package srsapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Client SRS HTTP API 客户端
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// New 创建 SRS API 客户端
// baseURL 示例："http://srs:1985"
func New(host, port string) *Client {
	return &Client{
		baseURL: fmt.Sprintf("http://%s:%s", host, port),
		httpClient: &http.Client{Timeout: 5 * time.Second},
	}
}

// ── 响应结构体 ───────────────────────────────────────────────

// srsResponse SRS API 通用外层结构
type srsResponse[T any] struct {
	Code   int    `json:"code"`
	Server string `json:"server"`
	Data   T      `json:"data"`
}

// StreamsData /api/v1/streams 响应
type StreamsData struct {
	Streams []Stream `json:"streams"`
}

// Stream 代表 SRS 中的一路活跃流
type Stream struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`   // stream key，如 "tz"
	App      string  `json:"app"`    // 如 "live"
	TCUrl    string  `json:"tcUrl"`
	URL      string  `json:"url"`    // 完整 RTMP URL
	LiveMS   int64   `json:"live_ms"`// 已推流时长（毫秒）
	Clients  int     `json:"clients"` // 当前拉流者数量
	Frames   int     `json:"frames"`
	Send     Kbps    `json:"send_bytes"`
	Recv     Kbps    `json:"recv_bytes"`
	Publish  Publish `json:"publish"`
	Video    *Video  `json:"video,omitempty"`
	Audio    *Audio  `json:"audio,omitempty"`
}

type Publish struct {
	Active bool   `json:"active"`
	Cid    string `json:"cid"`
}

type Kbps struct {
	Bytes int64 `json:"bytes"`
	Kbps  struct {
		Recv30s int `json:"recv_30s"`
		Send30s int `json:"send_30s"`
	} `json:"kbps"`
}

type Video struct {
	Codec   string `json:"codec"`
	Profile string `json:"profile"`
	Level   string `json:"level"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
}

type Audio struct {
	Codec      string `json:"codec"`
	SampleRate int    `json:"sample_rate"`
	Channel    int    `json:"channel"`
	Profile    string `json:"profile"`
}

// VersionData /api/v1/versions 响应
type VersionData struct {
	Major int    `json:"major"`
	Minor int    `json:"minor"`
	Revision int `json:"revision"`
	Version string `json:"version"`
}

// ── 公开方法 ─────────────────────────────────────────────────

// Ping 检查 SRS 是否可达（用于 Docker healthcheck 之外的应用层确认）
func (c *Client) Ping(ctx context.Context) error {
	var resp srsResponse[VersionData]
	if err := c.get(ctx, "/api/v1/versions", &resp); err != nil {
		return err
	}
	if resp.Code != 0 {
		return fmt.Errorf("SRS API error code %d", resp.Code)
	}
	return nil
}

// GetVersion 返回 SRS 版本信息
func (c *Client) GetVersion(ctx context.Context) (*VersionData, error) {
	var resp srsResponse[VersionData]
	if err := c.get(ctx, "/api/v1/versions", &resp); err != nil {
		return nil, err
	}
	return &resp.Data, nil
}

// ListStreams 返回当前所有活跃推流
func (c *Client) ListStreams(ctx context.Context) ([]Stream, error) {
	var resp srsResponse[StreamsData]
	if err := c.get(ctx, "/api/v1/streams", &resp); err != nil {
		return nil, err
	}
	return resp.Data.Streams, nil
}

// FindStream 在活跃流中查找指定 app/stream 的流
// 返回 (stream, true) 或 (nil, false)
func (c *Client) FindStream(ctx context.Context, app, stream string) (*Stream, bool, error) {
	streams, err := c.ListStreams(ctx)
	if err != nil {
		return nil, false, err
	}
	for i := range streams {
		s := &streams[i]
		if s.App == app && s.Name == stream && s.Publish.Active {
			return s, true, nil
		}
	}
	return nil, false, nil
}

// IsPublishing 检查某条流是否正在推流（有活跃的 publisher）
func (c *Client) IsPublishing(ctx context.Context, app, stream string) (bool, error) {
	_, found, err := c.FindStream(ctx, app, stream)
	return found, err
}

// WaitUntilPublishing 等待指定流进入推流状态
// 用于切流时确认新 inject 进程已成功建立连接
//
// 参数：
//   timeout  — 最长等待时间，建议 5-10s
//   interval — 轮询间隔，建议 200ms
//
// 返回 nil 表示流已就绪，返回 error 表示超时或查询失败
func (c *Client) WaitUntilPublishing(ctx context.Context, app, stream string, timeout, interval time.Duration) error {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case t := <-ticker.C:
			if t.After(deadline) {
				return fmt.Errorf("timeout waiting for stream %s/%s to publish (waited %s)", app, stream, timeout)
			}
			publishing, err := c.IsPublishing(ctx, app, stream)
			if err != nil {
				continue // 暂时性查询失败，继续等待
			}
			if publishing {
				return nil
			}
		}
	}
}

// WaitUntilNotPublishing 等待某路流停止推流（用于确认旧进程已退出）
func (c *Client) WaitUntilNotPublishing(ctx context.Context, app, stream string, timeout, interval time.Duration) error {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case t := <-ticker.C:
			if t.After(deadline) {
				return fmt.Errorf("timeout waiting for stream %s/%s to stop", app, stream)
			}
			publishing, err := c.IsPublishing(ctx, app, stream)
			if err != nil {
				continue
			}
			if !publishing {
				return nil
			}
		}
	}
}

// GetStreamStats 获取某路流的详细统计信息（帧率、码率、分辨率等）
// 用于推流质量监控
func (c *Client) GetStreamStats(ctx context.Context, app, stream string) (*Stream, error) {
	s, found, err := c.FindStream(ctx, app, stream)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("stream %s/%s not found or not publishing", app, stream)
	}
	return s, nil
}

// ── 内部方法 ─────────────────────────────────────────────────

func (c *Client) get(ctx context.Context, path string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: HTTP %d", path, resp.StatusCode)
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}
