package controller

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

func withTempLogDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := common.LogDir
	common.LogDir = &dir
	t.Cleanup(func() { common.LogDir = old })
	return dir
}

func postBeacon(body string, ua string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/log/web-vitals", bytes.NewBufferString(body))
	c.Request.Header.Set("User-Agent", ua)
	WebVitalsBeacon(c)
	c.Writer.WriteHeaderNow() // 真实链路由 gin 引擎在 handler 结束后写头；测试上下文要手动 flush
	return w
}

func TestWebVitalsBeacon_WritesSanitizedLine(t *testing.T) {
	dir := withTempLogDir(t)
	w := postBeacon(`{"type":"web_vitals","url":"/dashboard?token=secret#x","lcp":1234,"cls":0.05,"fid":-1,"ttfb":"12","bogus":1,"ts":1}`, "Mozilla/5.0 (iPhone) Mobile Safari")
	if w.Code != http.StatusNoContent {
		t.Fatalf("status %d", w.Code)
	}
	files, _ := filepath.Glob(filepath.Join(dir, webVitalsDir, "*.jsonl"))
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %v", files)
	}
	b, _ := os.ReadFile(files[0])
	line := string(b)
	for _, want := range []string{`"url":"/dashboard"`, `"lcp":1234`, `"cls":0.05`, `"dev":"mobile"`} {
		if !strings.Contains(line, want) {
			t.Errorf("line missing %s: %s", want, line)
		}
	}
	for _, bad := range []string{"secret", `"fid"`, `"ttfb"`, "bogus"} {
		if strings.Contains(line, bad) {
			t.Errorf("line should not contain %s: %s", bad, line)
		}
	}
}

func TestWebVitalsBeacon_RejectsJunk(t *testing.T) {
	dir := withTempLogDir(t)
	cases := []string{
		``,
		`not json`,
		`{"type":"other","lcp":1}`,
		`{"type":"web_vitals","url":"/x"}`,
		`{"type":"web_vitals","lcp":1e9}`,
		`{"type":"web_vitals","lcp":1,"pad":"` + strings.Repeat("x", webVitalsMaxBody) + `"}`,
	}
	for _, body := range cases {
		if w := postBeacon(body, ""); w.Code != http.StatusNoContent {
			t.Errorf("status %d for %q", w.Code, body[:min(len(body), 40)])
		}
	}
	if files, _ := filepath.Glob(filepath.Join(dir, webVitalsDir, "*.jsonl")); len(files) != 0 {
		t.Fatalf("junk should not be written, got %v", files)
	}
}

func TestPruneWebVitals(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"2000-01-01.jsonl", "2999-01-01.jsonl", "keep.txt"} {
		_ = os.WriteFile(filepath.Join(dir, name), []byte("x\n"), 0o644)
	}
	pruneWebVitals(dir, webVitalsKeepDays)
	for name, want := range map[string]bool{"2000-01-01.jsonl": false, "2999-01-01.jsonl": true, "keep.txt": true} {
		_, err := os.Stat(filepath.Join(dir, name))
		if (err == nil) != want {
			t.Errorf("%s exists=%v want %v", name, err == nil, want)
		}
	}
}
