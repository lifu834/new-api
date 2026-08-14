package middleware

import (
	"bytes"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestPixelsOfSize(t *testing.T) {
	cases := map[string]int{
		"1024x1024": 1048576,
		"1920x1080": 2073600,
		"3840x2160": 8294400,
		// 只给比例 / 缺失 / 非法 → 0，调用方据此落最便宜的一档
		"16:9": 0, "": 0, "1024": 0, "axb": 0, "0x1024": 0, "-10x1024": 0, "1024x0": 0,
	}
	for size, want := range cases {
		if got := pixelsOfSize(size); got != want {
			t.Errorf("pixelsOfSize(%q) = %d, want %d", size, got, want)
		}
	}
}

func ctxWith(t *testing.T, path, body string) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", path, bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	return c
}

func TestWithTierSuffix(t *testing.T) {
	cases := []struct{ name, path, model, body, want string }{
		// —— banana：1024²=1.05M / 2048²=4.19M / 4096²=16.8M
		{"banana 默认 1K", "/v1/images/generations", "nano-banana-pro",
			`{"size":"1024x1024"}`, "nano-banana-pro"},
		{"banana 2K", "/v1/images/generations", "nano-banana-pro",
			`{"size":"2048x2048"}`, "nano-banana-pro-2k"},
		{"banana 4K", "/v1/images/generations", "nano-banana-2",
			`{"size":"4096x4096"}`, "nano-banana-2-4k"},
		{"banana 16:9 4K", "/v1/images/generations", "nano-banana-pro",
			`{"size":"3840x2160"}`, "nano-banana-pro-4k"},

		// —— kling：1280x720=921,600 / 1920x1080=2,073,600
		{"kling 默认 720p", "/v1/videos", "kling-3.0",
			`{"size":"1280x720","seconds":"5"}`, "kling-3.0"},
		{"kling 1080p", "/v1/videos", "kling-3.0",
			`{"size":"1920x1080","seconds":"5"}`, "kling-3.0-1080p"},
		{"kling 竖版 1080p", "/v1/videos", "kling-3.0",
			`{"size":"1080x1920","seconds":"5"}`, "kling-3.0-1080p"},
		{"kling 不传 size 落最便宜档", "/v1/videos", "kling-3.0",
			`{"seconds":"5"}`, "kling-3.0"},

		// —— 边界：显式档位名不是基名，不重复加后缀
		{"显式 kling-3.0-1080p 原样放过", "/v1/videos", "kling-3.0-1080p",
			`{"size":"1280x720"}`, "kling-3.0-1080p"},
		{"显式 banana-4k 原样放过", "/v1/images/generations", "nano-banana-pro-4k",
			`{"size":"1024x1024"}`, "nano-banana-pro-4k"},
		// —— 非档位模型一律不碰
		{"veo 不受影响", "/v1/videos", "veo-3.1",
			`{"size":"1920x1080"}`, "veo-3.1"},
		{"gpt-image-2 不受影响", "/v1/images/generations", "gpt-image-2",
			`{"size":"3840x2160"}`, "gpt-image-2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := withTierSuffix(ctxWith(t, tc.path, tc.body), tc.model); got != tc.want {
				t.Errorf("withTierSuffix(%q) = %q, want %q", tc.model, got, tc.want)
			}
		})
	}
}
