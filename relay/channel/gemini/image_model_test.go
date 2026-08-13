package gemini

import "testing"

// TestIsGeminiNativeImageModel 守住「Gemini 原生生图 vs imagen」这条分界。
//
// 背景（260813 实测）：gemini-3-pro-image / gemini-3.1-flash-image（市面别名
// nano-banana-pro / nano-banana-2）走 :generateContent + contents 协议，
// 而 imagen 系走 :predict + instances/parameters。两者混淆的后果是实打实的：
// 判成 imagen → 被 "only imagen models are supported" 拒；
// 走直通不转换 → 上游回 "contents is required"。两种错我都撞过。
func TestIsGeminiNativeImageModel(t *testing.T) {
	native := []string{
		"gemini-3-pro-image",
		"gemini-3-pro-image-preview",
		"gemini-3.1-flash-image",
		"gemini-3.1-flash-image-preview",
		"gemini-2.5-flash-image",
		"nano-banana-pro",
		"nano-banana-2",
		"nano-banana-pro-1k", // 中转站带档位后缀的写法
		"NANO-BANANA-2-4K",   // 大小写不敏感
	}
	for _, m := range native {
		if !isGeminiNativeImageModel(m) {
			t.Errorf("%q 应判为 Gemini 原生生图模型（走 :generateContent）", m)
		}
	}

	// imagen 必须留在原有 :predict 分支，别被新逻辑抢走
	notNative := []string{
		"imagen-3.0-generate-002",
		"imagen-4.0-ultra-generate-001",
		"gemini-2.5-flash",     // 纯文本，不含 image
		"gemini-3-pro-preview", // 同上
		"gpt-image-2",          // 别家的生图模型，不该被这里接管
		"",
	}
	for _, m := range notNative {
		if isGeminiNativeImageModel(m) {
			t.Errorf("%q 不应判为 Gemini 原生生图模型", m)
		}
	}
}
