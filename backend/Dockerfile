# ── Stage 1: build ──────────────────────────────────���───────
FROM golang:1.22-alpine AS builder

# go-sqlite3 需要 CGO
RUN apk add --no-cache gcc musl-dev

WORKDIR /build
# 使用国内 Go 模块代理
ENV GOPROXY=https://goproxy.cn,https://goproxy.io,direct
ENV GONOSUMCHECK=*
# go.sum* — glob 使 go.sum 变为可选（首次构建时自动生成）
COPY go.mod go.sum* ./
# download 根据 go.mod 下载依赖并生成 go.sum（不需要源码）
RUN go mod download

COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -mod=mod -ldflags="-s -w" -o susuper .

# ── Stage 2: runtime ──────────────────────────────────��──────
FROM alpine:3.19

# ffmpeg 用于转码和推流；ca-certificates 用于 HTTPS 请求（SMS API）
RUN apk add --no-cache ffmpeg ca-certificates tzdata

# 设置时区为上海
ENV TZ=Asia/Shanghai

WORKDIR /app
COPY --from=builder /build/susuper .
COPY db/schema.sql ./db/

EXPOSE 8080
CMD ["./susuper"]
