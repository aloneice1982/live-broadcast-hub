package handler

import (
	"bufio"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// ── 带宽采样（package 级后台 goroutine）──────────────────────────────
//
// 读取宿主机 /host/proc/net/dev（由 docker-compose 只读挂载自 /proc/net/dev）。
// 只统计物理/主干网卡，过滤虚拟网卡（veth*、br-*、docker*、virbr*、tun*、tap*）
// 避免 Unraid/Docker 虚拟桥接接口重复计算同一份外网流��。
//
// 采样间隔 2s，存最新 Mbps 到 netStats。

const hostProcNetDev = "/host/proc/net/dev"

type netStats struct {
	mu          sync.RWMutex
	UploadMbps  float64
	DownMbps    float64
}

var globalNetStats netStats

func init() {
	go func() {
		var prevRx, prevTx uint64
		var prevTime time.Time

		for {
			rx, tx, err := readPhysicalNetBytes()
			now := time.Now()
			if err == nil && !prevTime.IsZero() {
				dt := now.Sub(prevTime).Seconds()
				if dt > 0 && rx >= prevRx && tx >= prevTx {
					upMbps := float64(tx-prevTx) * 8 / 1e6 / dt
					downMbps := float64(rx-prevRx) * 8 / 1e6 / dt
					globalNetStats.mu.Lock()
					globalNetStats.UploadMbps = math.Round(upMbps*100) / 100
					globalNetStats.DownMbps = math.Round(downMbps*100) / 100
					globalNetStats.mu.Unlock()
				}
			}
			if err == nil {
				prevRx, prevTx = rx, tx
				prevTime = now
			}
			time.Sleep(2 * time.Second)
		}
	}()
}

// readPhysicalNetBytes 读取 /host/proc/net/dev，累加所有物理网卡的 rxBytes/txBytes。
// 跳过 lo 及虚拟接口（veth, br-, docker, virbr, tun, tap）。
// Unraid 的主干桥接口通常是 br0（物理），保留。
func readPhysicalNetBytes() (rx, tx uint64, err error) {
	f, err := os.Open(hostProcNetDev)
	if err != nil {
		return
	}
	defer f.Close()

	virtualPrefixes := []string{"veth", "br-", "docker", "virbr", "tun", "tap"}

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		// 跳过标题行
		if !strings.Contains(line, ":") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		iface := strings.TrimSpace(parts[0])

		// 跳过 loopback
		if iface == "lo" {
			continue
		}
		// 跳过虚拟网卡
		isVirtual := false
		for _, prefix := range virtualPrefixes {
			if strings.HasPrefix(iface, prefix) {
				isVirtual = true
				break
			}
		}
		if isVirtual {
			continue
		}

		// /proc/net/dev 列顺序（空格分隔）：
		// rxBytes rxPkts rxErrs rxDrop rxFIFO rxFrame rxComp rxMcast
		// txBytes txPkts txErrs txDrop txFIFO txColls txCarr txComp
		fields := strings.Fields(parts[1])
		if len(fields) < 9 {
			continue
		}
		r, e1 := strconv.ParseUint(fields[0], 10, 64)
		t, e2 := strconv.ParseUint(fields[8], 10, 64)
		if e1 == nil && e2 == nil {
			rx += r
			tx += t
		}
	}
	err = scanner.Err()
	return
}

// getSystemMetrics GET /api/system/metrics（无需鉴权）
func (h *Handler) getSystemMetrics(c *gin.Context) {
	globalNetStats.mu.RLock()
	up := globalNetStats.UploadMbps
	down := globalNetStats.DownMbps
	globalNetStats.mu.RUnlock()
	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"uploadMbps":   up,
		"downloadMbps": down,
	}})
}
