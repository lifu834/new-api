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

func TestPixelsOfTierName(t *testing.T) {
	cases := map[string]int{
		"720p": 921600, "1080p": 2073600, "4K": 8294400, "2160p": 8294400,
		"  1080P  ": 2073600, // 大小写与空白都要认
		"":          0, "1080": 0, "16:9": 0, "foo": 0,
	}
	for name, want := range cases {
		if got := pixelsOfTierName(name); got != want {
			t.Errorf("pixelsOfTierName(%q) = %d, want %d", name, got, want)
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
		// —— 260826：生图线也取消了裸名，改用 nano-banana-*-1k/-2k/-4k 六个显式 SKU
		// ⇒ nano-banana 族已移出档位表，**不再有任何按 size 的改写**。
		// 档位改由聚合层（image2api）按 SKU 名向上游强制下发，比按 size 猜更硬；
		// 直连的 Gemini 渠道走 imageSizeFromModel，同样按模型名后缀钉。
		{"banana 显式 1K 原样放过", "/v1/images/generations", "nano-banana-pro-1k",
			`{"size":"1024x1024"}`, "nano-banana-pro-1k"},
		{"banana 显式 2K 不因 size 变档", "/v1/images/generations", "nano-banana-pro-2k",
			`{"size":"1024x1024"}`, "nano-banana-pro-2k"},
		{"banana 显式 4K 不因 size 变档", "/v1/images/generations", "nano-banana-2-4k",
			`{"size":"1024x1024"}`, "nano-banana-2-4k"},
		// 关键回归：付 1K 却传 4096 的 size，**不再被自动升档**（也就不会按 4K 计费）。
		// 收入不再靠这层隐式改写保护，而是靠 image2api 覆写尺寸：客户拿到的就是 1K。
		{"banana 1K 传大 size 不升档", "/v1/images/generations", "nano-banana-2-1k",
			`{"size":"4096x4096"}`, "nano-banana-2-1k"},
		// 已废弃的裸名：不在任何渠道，中间件也不再认识它，原样放过后由路由层报错
		{"废弃裸名 nano-banana-2 不再被改写", "/v1/images/generations", "nano-banana-2",
			`{"size":"4096x4096"}`, "nano-banana-2"},

		// —— 260826：视频线取消裸名、改用显式分辨率名 ⇒ 不再有任何改写。
		// 这些用例现在正向断言"原样放过"，包括过去会被改写的组合。
		{"kling-3.0 不再按 size 改写", "/v1/videos", "kling-3.0",
			`{"size":"1920x1080","seconds":"5"}`, "kling-3.0"},
		{"kling-3.0-1080p 是显式 SKU，原样放过", "/v1/videos", "kling-3.0-1080p",
			`{"size":"1280x720","seconds":"5"}`, "kling-3.0-1080p"},
		{"sd-2.0-720p 原样放过", "/v1/videos", "sd-2.0-720p",
			`{"resolution":"1080p","duration":5}`, "sd-2.0-720p"},
		{"sd-2.0-1080p 原样放过", "/v1/videos", "sd-2.0-1080p",
			`{"resolution":"720p","duration":5}`, "sd-2.0-1080p"},
		{"sd-2.0-4k 原样放过", "/v1/videos", "sd-2.0-4k",
			`{"duration":5}`, "sd-2.0-4k"},
		{"sd-fast-720p 原样放过", "/v1/videos", "sd-fast-720p",
			`{"resolution":"1080p"}`, "sd-fast-720p"},
		{"sd-mini-720p 原样放过", "/v1/videos", "sd-mini-720p",
			`{"size":"3840x2160"}`, "sd-mini-720p"},
		// 已废弃的裸名：不在任何渠道，中间件也不再认识它，原样放过后由路由层报错
		{"废弃裸名 sd-2.0 不再被改写", "/v1/videos", "sd-2.0",
			`{"size":"3840x2160","duration":5}`, "sd-2.0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := withTierSuffix(ctxWith(t, tc.path, tc.body), tc.model); got != tc.want {
				t.Errorf("withTierSuffix(%q) = %q, want %q", tc.model, got, tc.want)
			}
		})
	}
}
