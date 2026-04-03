// Package ffscript — FFmpeg 脚本生成器
//
// 本包是整个系统中所有 FFmpeg 命令的唯一权威来源。
// 每一个参数的选择都有明确的注释说明原因，
// 方便运维人员在不熟悉 FFmpeg 的情况下理解和调试。
//
// 架构示意：
//
//   ┌─────────────────────────────────────────────────────┐
//   │  inject-ffmpeg（源 → SRS）                         │
//   │                                                     │
//   │  [promo_video]  MP4 loop ──────────────────────┐   │
//   │  [live_stream]  JSTV RTMP ─ volume gain ───────┤   │
//   │                                                 ▼   │
//   │                              rtmp://srs:1935/live/{code} │
//   └──────────────────────────────────────┬──────────────┘
//                                          │ SRS GOP 缓存 / 中继
//   ┌──────────────────────────────────────▼──────────────┐
//   │  push-ffmpeg（SRS → 视频号）                        │
//   │                                                     │
//   │  rtmp://srs:1935/live/{code} ──c copy──► 视频号     │
//   └─────────────────────────────────────────────────────┘
//
// 切流原理（无缝）：
//   1. 新 inject 进程先启动，推流到同一个 SRS stream key
//   2. SRS 收到新 publish 后断开旧连接（overwrite）
//   3. SRS GOP 缓存（2s）保证 push 端在新流就绪前有数据可发
//   4. 旧 inject 进程 SIGTERM，不需要 SIGKILL（已被 SRS 断开）
//   → push 进程和视频号推流地址全程不变，观众无感知

package ffscript

import (
	"fmt"
	"strings"
)

// InjectPromoArgs 生成"宣传片循环注入 SRS"的 FFmpeg 参数
//
// 用途：在比赛间隙或开赛前，将本地宣传片循环推入 SRS，
//       保持视频号推流不断线。
//
// 关键参数说明：
//   -re               读取速率锁定为文件原始帧率（1x 实时速）
//                     不加此参数 FFmpeg 会尽可能快地读取文件，
//                     导致推流速率远超直播码率，SRS 缓冲区溢出
//   -stream_loop -1   无限循环读取文件（-1 = 永久），
//                     直到进程被外部信号终止
//   -fflags +genpts   重新生成 PTS（Presentation Timestamp）
//                     文件循环时 PTS 会重置，不加此参数会导致
//                     下游解码器出现时间戳跳变，花屏或卡顿
//   -c copy           直接复制码流，不转码
//                     宣传片已在上传时离线标准化，格式与
//                     直播流一致，可安全 copy，CPU 占用极低
//   -f flv            输出封装格式（SRS RTMP 端点要求）
func InjectPromoArgs(transcodedPath, srsRTMPURL string) []string {
	return []string{
		"-re",
		"-stream_loop", "-1",
		"-fflags", "+genpts",
		"-i", transcodedPath,
		// 视频直通：宣传片已是标准格式（H.264 Main, 25fps, 固定 GOP）
		"-c", "copy",
		// 避免 flv 封装时因时间戳溢出导致播放器重置
		"-flvflags", "no_duration_filesize",
		"-f", "flv",
		srsRTMPURL,
	}
}

// InjectLiveArgs 生成"拉取上游直播流注入 SRS"的 FFmpeg 参数
//
// 用途：拉取 JSTV 直播源，叠加音量增益后推入 SRS。
//
// 关键参数说明：
//   -reconnect 1              输入流断开后自动重连（网络抖动保护）
//   -reconnect_streamed 1     对直播流（非点播）也启用重连
//   -reconnect_delay_max 3    最大重连间隔 3 秒，避免长时间断流
//   -c:v copy                 视频绝对禁止转码（系统核心约束）
//                             原始直播流已是 H.264，copy 直通
//                             可节省 80%+ CPU，VPS 可承受 5-6 路
//   -c:a aac                  音频需要重编码以应用 volume filter
//                             仅音频重编码，CPU 开销可接受（< 5%）
//   -b:a 128k                 音频码率（与宣传片标准化格式一致）
//   -ar 44100                 采样率（与宣传片一致，保证 SRS 切流连续）
//   -ac 2                     双声道（立体声）
//   -af "volume=X"            音量增益（FFmpeg audio filter）
//                             X 为浮点倍数，1.5 = 放大 1.5 倍
//                             建议范围：1.0-2.0，超过 2.0 易失真
//   -async 1                  音视频同步模式（1=扩展/丢弃样本同步）
//                             切流后音视频可能有微小偏差，此参数修正
func InjectLiveArgs(sourceURL, srsRTMPURL string, volumeGain float64) []string {
	// 安全边界：防止增益超出合理范围导致音频失真
	if volumeGain < 1.0 {
		volumeGain = 1.0
	}
	if volumeGain > 2.0 {
		volumeGain = 2.0
	}

	return []string{
		// RTMP 直播流输入（原生 RTMP，不用 librtmp）
		// -rtmp_live live 告知服务端这是直播流，避免服务端等待完整 duration
		"-rtmp_live", "live",
		"-i", sourceURL,
		// 视频：绝对禁止转码
		"-c:v", "copy",
		// 音频：轻量重编码 + 音量增益
		"-c:a", "aac",
		"-b:a", "128k",
		"-ar", "44100",
		"-ac", "2",
		"-af", fmt.Sprintf("volume=%.2f", volumeGain),
		"-async", "1",
		// 输出
		"-f", "flv",
		srsRTMPURL,
	}
}

// PushArgs 生成"SRS → 视频号推流"的 FFmpeg 参数
//
// 用途：将 SRS 内部流持续推送到微信视频号 RTMP 地址。
//       此进程全程不停，切流时保持不变。
//
// 关键参数说明：
//   -reconnect / -reconnect_streamed
//                     SRS 切流瞬间新旧 inject 交替，
//                     push 端会看到极短的流中断，自动重连恢复
//   -reconnect_delay_max 2
//                     切流时最快 2 秒内重新建立连接，
//                     视频号短暂缓冲足以掩盖此延迟
//   -c copy           SRS 已接收好的码流直接转发，不再处理
//   -f flv            视频号 RTMP 推流要求 FLV 封装
//   -flvflags no_duration_filesize
//                     FLV 直播场景禁用 duration 字段，
//                     避免视频号服务端误判为点播文件
func PushArgs(srsRTMPURL, destURL string) []string {
	return []string{
		// 从 SRS 拉流（RTMP，原生实现）
		"-rtmp_live", "live",
		"-i", srsRTMPURL,
		// 直接转发，不处理
		"-c", "copy",
		"-f", "flv",
		"-flvflags", "no_duration_filesize",
		destURL,
	}
}

// TranscodeArgs 生成"宣传片离线标准化转码"的 FFmpeg 参数
//
// 用途：将上传的任意 MP4 转码为与上游直播流格式高度一致的标准文件。
//       这是"无缝切换"的物理基础：
//       直播流和宣传片拥有相同的编码参数，
//       SRS 收到新 inject 时无需重新初始化解码器。
//
// 目标规格：
//   视频: H.264 Main Profile Level 4.0, 1920×1080, 25fps
//         GOP = 50帧（2秒），固定关键帧间隔
//   音频: AAC-LC, 128kbps, 44100Hz, 双声道
//
// 关键参数说明：
//   -preset veryfast  编码速度/质量平衡，离线任务无需最优质量
//   -profile:v main   Main Profile，移动端和机顶盒通用兼容
//   -level 4.0        对应 1080p@30fps 的标准兼容级别
//   -g 50             GOP = 50 帧（@ 25fps = 2 秒一个关键帧）
//                     与直播流 GOP 对齐，SRS 切流时 GOP 缓存有效
//   -keyint_min 50    最小 GOP 也是 50，禁止在 IDR 帧之间插额外关键帧
//   -sc_threshold 0   禁止场景切换强制关键帧
//                     （默认行为会在场景变化处插入关键帧，
//                      导致 GOP 不规则，切流时对齐困难）
//   -movflags +faststart
//                     将 MP4 moov atom 移到文件头
//                     FFmpeg -re 读取时无需 seek 到文件尾，
//                     减少循环开始时的卡顿
func TranscodeArgs(inputPath, outputPath, resolution, fps, gop, audioBitrate, audioRate string) []string {
	// 将 "1920x1080" 转为 "1920:1080"（FFmpeg scale filter 语法）
	scaleFilter := strings.ReplaceAll(resolution, "x", ":")

	return []string{
		"-i", inputPath,
		// ── 视频 ──
		"-c:v", "libx264",
		"-preset", "veryfast",
		"-profile:v", "main",
		"-level:v", "4.0",
		// scale + fps 滤镜（顺序：先缩放再设帧率，避免重复帧）
		"-vf", fmt.Sprintf("scale=%s:force_original_aspect_ratio=decrease,pad=%s:(ow-iw)/2:(oh-ih)/2,fps=%s",
			scaleFilter, scaleFilter, fps),
		"-g", gop,
		"-keyint_min", gop,
		"-sc_threshold", "0",
		"-b:v", "2500k",
		"-maxrate", "3000k",
		"-bufsize", "6000k",
		// ── 音频 ──
		"-c:a", "aac",
		"-b:a", audioBitrate,
		"-ar", audioRate,
		"-ac", "2",
		// ── 容器 ──
		"-movflags", "+faststart",
		// 覆盖已有输出文件
		"-y",
		outputPath,
	}
}

// SRSStreamURL 构造 SRS RTMP 挂载点 URL
// 格式: rtmp://{host}:{port}/{app}/{stream}
func SRSStreamURL(host, port, app, stream string) string {
	return fmt.Sprintf("rtmp://%s:%s/%s/%s", host, port, app, stream)
}

// PushDestURL 构造推流目标 URL（推流地址 + / + 推流密钥）
func PushDestURL(pushURL, pushKey string) string {
	base := strings.TrimRight(pushURL, "/")
	return fmt.Sprintf("%s/%s", base, pushKey)
}

// CmdString 将参数列表格式化为可在终端直接执行的命令字符串（调试用）
func CmdString(bin string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, bin)
	for _, a := range args {
		// 含空格或特殊字符的参数加引号
		if strings.ContainsAny(a, " \t\"'&|;<>()") {
			parts = append(parts, fmt.Sprintf("%q", a))
		} else {
			parts = append(parts, a)
		}
	}
	return strings.Join(parts, " ")
}
