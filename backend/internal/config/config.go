package config

import (
	"fmt"
	"os"
	"strconv"
)

type Config struct {
	AppPort  string
	AppEnv   string
	JWTSecret string

	DBPath      string
	UploadDir   string
	TranscodeDir string

	// SRS
	SRSHost     string
	SRSRTMPPort string
	SRSAPIPort  string
	SRSRTMPBase string // e.g. rtmp://srs:1935

	// FFmpeg
	FFmpegBin            string
	TranscodeConcurrency int
	TranscodeResolution  string
	TranscodeFPS         string
	TranscodeGOP         string // keyframe interval
	TranscodeVideoBitrate string
	TranscodeAudioBitrate string
	TranscodeAudioRate   string

	// Circuit breaker
	FFmpegMaxRetries    int
	FFmpegRetryDelaySec int

	// SMS
	SMSAPIURL          string
	SMSAPIKey          string
	SMSSuperAdminPhones string

	// Initial admin password
	AdminInitialPassword string
}

func Load() (*Config, error) {
	c := &Config{
		AppPort:   getEnv("APP_PORT", "8080"),
		AppEnv:    getEnv("APP_ENV", "development"),
		JWTSecret: mustEnv("JWT_SECRET"),

		DBPath:       getEnv("DB_PATH", "./data/db/susuper.db"),
		UploadDir:    getEnv("UPLOAD_DIR", "./data/uploads"),
		TranscodeDir: getEnv("TRANSCODE_DIR", "./data/transcoded"),

		SRSHost:     getEnv("SRS_HOST", "localhost"),
		SRSRTMPPort: getEnv("SRS_RTMP_PORT", "1935"),
		SRSAPIPort:  getEnv("SRS_API_PORT", "1985"),

		FFmpegBin:           getEnv("FFMPEG_BIN", "ffmpeg"),
		TranscodeResolution: getEnv("TRANSCODE_RESOLUTION", "1920x1080"),
		TranscodeFPS:        getEnv("TRANSCODE_FPS", "25"),
		TranscodeGOP:        getEnv("TRANSCODE_KEYFRAME_INTERVAL", "50"),
		TranscodeAudioBitrate: getEnv("TRANSCODE_AUDIO_BITRATE", "128k"),
		TranscodeAudioRate:   getEnv("TRANSCODE_AUDIO_SAMPLERATE", "44100"),

		SMSAPIURL:           getEnv("SMS_API_URL", ""),
		SMSAPIKey:           getEnv("SMS_API_KEY", ""),
		SMSSuperAdminPhones: getEnv("SMS_SUPER_ADMIN_PHONES", ""),

		AdminInitialPassword: getEnv("ADMIN_INITIAL_PASSWORD", "Admin@2025!"),
	}

	c.SRSRTMPBase = fmt.Sprintf("rtmp://%s:%s", c.SRSHost, c.SRSRTMPPort)

	var err error
	if c.TranscodeConcurrency, err = strconv.Atoi(getEnv("TRANSCODE_CONCURRENCY", "2")); err != nil {
		return nil, fmt.Errorf("TRANSCODE_CONCURRENCY: %w", err)
	}
	if c.FFmpegMaxRetries, err = strconv.Atoi(getEnv("FFMPEG_MAX_RETRIES", "3")); err != nil {
		return nil, fmt.Errorf("FFMPEG_MAX_RETRIES: %w", err)
	}
	if c.FFmpegRetryDelaySec, err = strconv.Atoi(getEnv("FFMPEG_RETRY_DELAY_SECONDS", "3")); err != nil {
		return nil, fmt.Errorf("FFMPEG_RETRY_DELAY_SECONDS: %w", err)
	}

	return c, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		// 开发模式下允许默认值
		return "dev-secret-change-in-production"
	}
	return v
}
