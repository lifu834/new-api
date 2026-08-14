package middleware

import (
	"bytes"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestBananaTierSuffix(t *testing.T) {
	cases := map[string]string{
		// 1K：常见的 OpenAI 小尺寸档
		"1024x1024": "",
		"1536x1024": "",
		"1024x1536": "",
		"1792x1024": "",
		// 2K
		"2048x2048": "-2k",
		"2048x1152": "-2k",
		"1536x1536": "-2k", // 2.36M
		// 4K —— 3840x2160 = 8,294,400 正是我们的真 4K 判据，必须落在 4K
		"3840x2160": "-4k",
		"2160x3840": "-4k",
		"4096x4096": "-4k",
		"2880x2880": "-4k", // 8.29M，与 3840x2160 同像素
		// 只给比例、缺失、非法 → 落最便宜的一档，不能因为参数没传就多收钱
		"16:9":     "",
		"":         "",
		"1024":     "",
		"axb":      "",
		"0x1024":   "",
		"-10x1024": "",
	}
	for size, want := range cases {
		if got := bananaTierSuffix(size); got != want {
			t.Errorf("bananaTierSuffix(%q) = %q, want %q", size, got, want)
		}
	}
}

func newImageCtx(t *testing.T, body string) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/images/generations", bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	return c
}

func TestWithBananaTierSuffix(t *testing.T) {
	cases := []struct {
		name, model, body, want string
	}{
		{"pro 默认 1K", "nano-banana-pro",
			`{"model":"nano-banana-pro","prompt":"x","size":"1024x1024"}`, "nano-banana-pro"},
		{"pro 4K", "nano-banana-pro",
			`{"model":"nano-banana-pro","prompt":"x","size":"4096x4096"}`, "nano-banana-pro-4k"},
		{"pro 16:9 4K", "nano-banana-pro",
			`{"model":"nano-banana-pro","prompt":"x","size":"3840x2160"}`, "nano-banana-pro-4k"},
		{"banana-2 2K", "nano-banana-2",
			`{"model":"nano-banana-2","prompt":"x","size":"2048x2048"}`, "nano-banana-2-2k"},
		{"不传 size 走 1K", "nano-banana-2",
			`{"model":"nano-banana-2","prompt":"x"}`, "nano-banana-2"},
		// 显式写档位名的老调用必须原样放过，不能叠成 -4k-4k
		{"显式 SKU 不重复加后缀", "nano-banana-pro-4k",
			`{"model":"nano-banana-pro-4k","prompt":"x","size":"1024x1024"}`, "nano-banana-pro-4k"},
		// 非 banana 模型一律不碰——gpt-image-2 的 size 是另一套语义
		{"gpt-image-2 不受影响", "gpt-image-2",
			`{"model":"gpt-image-2","prompt":"x","size":"3840x2160"}`, "gpt-image-2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := withBananaTierSuffix(newImageCtx(t, tc.body), tc.model); got != tc.want {
				t.Errorf("withBananaTierSuffix(%q) = %q, want %q", tc.model, got, tc.want)
			}
		})
	}
}
