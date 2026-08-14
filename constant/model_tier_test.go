package constant

import "testing"

func TestIsHiddenTierSKU(t *testing.T) {
	hidden := map[string]bool{
		"nano-banana-pro-2k": true, "nano-banana-pro-4k": true,
		"nano-banana-2-2k": true, "nano-banana-2-4k": true,
		"kling-3.0-1080p": true,
		"kling-3.0-720p":  true, // 历史别名，等同于基名
	}
	// 基名本身必须**可见**（那是对外唯一暴露的名字），无关模型也不该被隐藏
	for _, m := range []string{"nano-banana-pro", "nano-banana-2", "kling-3.0",
		"nano-banana-pro-2k", "nano-banana-pro-4k", "nano-banana-2-2k", "nano-banana-2-4k",
		"kling-3.0-1080p", "kling-3.0-720p",
		"kling-3.0-turbo", "kling-o3", "nano-banana-pro-8k", "gpt-image-2", "veo-3.1", ""} {
		if got := IsHiddenTierSKU(m); got != hidden[m] {
			t.Errorf("IsHiddenTierSKU(%q) = %v, want %v", m, got, hidden[m])
		}
	}
}

func TestSuffixForPixels(t *testing.T) {
	banana, ok := TieredFamilyOf("nano-banana-pro")
	if !ok {
		t.Fatal("nano-banana-pro 应当是档位基名")
	}
	for px, want := range map[int]string{0: "", 1048576: "", 2073600: "-2k",
		4194304: "-2k", 8294400: "-4k", 16777216: "-4k"} {
		if got := banana.SuffixForPixels(px); got != want {
			t.Errorf("banana.SuffixForPixels(%d) = %q, want %q", px, got, want)
		}
	}
	kling, ok := TieredFamilyOf("kling-3.0")
	if !ok {
		t.Fatal("kling-3.0 应当是档位基名")
	}
	for px, want := range map[int]string{0: "", 921600: "", 2073600: "-1080p", 8294400: "-1080p"} {
		if got := kling.SuffixForPixels(px); got != want {
			t.Errorf("kling.SuffixForPixels(%d) = %q, want %q", px, got, want)
		}
	}
	// kling-3.0-turbo 不是基名（它自己不分档）
	if _, ok := TieredFamilyOf("kling-3.0-turbo"); ok {
		t.Error("kling-3.0-turbo 不该被当成档位基名")
	}
}
