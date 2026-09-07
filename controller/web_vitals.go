package controller

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

// POST /api/log/web-vitals —— 前端 Core Web Vitals 信标。
//
// 发送方是 nycatai 的 plugins/web-vitals.client.ts：页面切到后台或关闭时用 sendBeacon
// 发一次 {type:"web_vitals", url, lcp, fid, cls, ttfb, fcp, ts}。此前后端没有这个路由，
// 每次页面加载白打一发 404（260907 前）。
//
// 设计取舍：
//   - 匿名、无鉴权（信标发出时通常没有登录态，也不该要求有）；靠 CriticalRateLimit 限速 +
//     2KB 体积上限 + 只收白名单数字字段，把它当成"任何人都能写一行"的接口来防。
//   - 不记 IP / UA 原文 / 用户 id：性能统计用不上，记了反而变成隐私数据。只按 UA 粗分
//     mobile / desktop 两桶，因为移动端 LCP 天然更差，混在一起没法看。
//   - 落 <log-dir>/web-vitals/YYYY-MM-DD.jsonl（容器里 /data/logs/web-vitals，宿主机
//     /opt/newapi/data/newapi/logs/web-vitals），Channel Ops 直接读文件聚合，不进 DB：
//     这是运维视角的旁路数据，与业务库无关，也不值得一张表。保留 14 天。
//   - 不管成功失败一律 204：信标客户端不看响应，多余的错误体只是浪费。

const (
	webVitalsMaxBody  = 2048
	webVitalsKeepDays = 14
	webVitalsDir      = "web-vitals"
	webVitalsMaxURL   = 200
)

// 允许的指标及其合理上限（毫秒；cls 是无量纲比值）。超界视为脏数据丢弃。
var webVitalsFields = map[string]float64{
	"lcp":  600000,
	"fid":  600000,
	"fcp":  600000,
	"ttfb": 600000,
	"cls":  100,
}

var (
	webVitalsMu       sync.Mutex
	webVitalsPruneDay string
)

func WebVitalsBeacon(c *gin.Context) {
	defer c.Status(http.StatusNoContent)

	body, err := io.ReadAll(io.LimitReader(c.Request.Body, webVitalsMaxBody+1))
	if err != nil || len(body) == 0 || len(body) > webVitalsMaxBody {
		return
	}
	var in map[string]any
	if err := json.Unmarshal(body, &in); err != nil {
		return
	}
	if t, _ := in["type"].(string); t != "web_vitals" {
		return
	}
	rec := webVitalsRecord(in, c.GetHeader("User-Agent"))
	if rec == nil {
		return
	}
	if err := appendWebVitals(rec); err != nil {
		common.SysLog("web-vitals append failed: " + err.Error())
	}
}

// webVitalsRecord 把信标体整理成落盘记录；没有任何有效指标时返回 nil。
func webVitalsRecord(in map[string]any, ua string) map[string]any {
	rec := map[string]any{
		"ts":  time.Now().Unix(),
		"url": sanitizeVitalsURL(in["url"]),
		"dev": deviceBucket(ua),
	}
	n := 0
	for k, max := range webVitalsFields {
		v, ok := in[k].(float64)
		if !ok || v < 0 || v > max {
			continue
		}
		rec[k] = v
		n++
	}
	if n == 0 {
		return nil
	}
	return rec
}

// sanitizeVitalsURL 只保留路径：去掉 query / fragment，限长，非 ASCII 可见字符剔除。
func sanitizeVitalsURL(v any) string {
	s, _ := v.(string)
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	var b strings.Builder
	for _, r := range s {
		if r > 32 && r < 127 {
			b.WriteRune(r)
		}
		if b.Len() >= webVitalsMaxURL {
			break
		}
	}
	if b.Len() == 0 {
		return "/"
	}
	return b.String()
}

func deviceBucket(ua string) string {
	if strings.Contains(strings.ToLower(ua), "mobile") {
		return "mobile"
	}
	return "desktop"
}

func webVitalsBaseDir() string {
	base := "./logs"
	if common.LogDir != nil && *common.LogDir != "" {
		base = *common.LogDir
	}
	return filepath.Join(base, webVitalsDir)
}

func appendWebVitals(rec map[string]any) error {
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	dir := webVitalsBaseDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	day := time.Now().UTC().Format("2006-01-02")

	webVitalsMu.Lock()
	defer webVitalsMu.Unlock()

	f, err := os.OpenFile(filepath.Join(dir, day+".jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.Write(append(line, '\n'))
	cerr := f.Close()
	if webVitalsPruneDay != day {
		webVitalsPruneDay = day
		pruneWebVitals(dir, webVitalsKeepDays)
	}
	if werr != nil {
		return werr
	}
	return cerr
}

// pruneWebVitals 删掉早于 keepDays 的日文件；每天第一笔写入时顺手做一次。
func pruneWebVitals(dir string, keepDays int) {
	cutoff := time.Now().UTC().AddDate(0, 0, -keepDays).Format("2006-01-02")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".jsonl") || len(name) != len("2006-01-02.jsonl") {
			continue
		}
		if name[:10] < cutoff {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
}
