package constant

import "testing"

func TestIsHiddenModel(t *testing.T) {
	hidden := map[string]bool{
		"nano-banana-pro-2k": true, "nano-banana-pro-4k": true,
		"nano-banana-2-2k": true, "nano-banana-2-4k": true,
		"kling-3.0-1080p": true,
		"kling-3.0-720p":  true, // 历史别名，等同于基名
		// 历史遗留重复名：同渠道同价，只是把供应商写在了名字里
		"leonardo-kling-3.0": true, "leonardo-kling-3.0-turbo": true,
		"leonardo-kling-o3": true, "leonardo-veo-3.1": true,
		"leonardo-veo-3.1-fast": true,
		// seedance（overseas）：档位 SKU 与三个历史别名
		"sd-2.0-1080p": true, "sd-2.0-4k": true,
		"sd-2.0-720p": true, "sd-fast-720p": true, "sd-mini-720p": true,
	}
	// 基名本身必须**可见**（那是对外唯一暴露的名字），无关模型也不该被隐藏
	for _, m := range []string{"nano-banana-pro", "nano-banana-2", "kling-3.0",
		"nano-banana-pro-2k", "nano-banana-pro-4k", "nano-banana-2-2k", "nano-banana-2-4k",
		"kling-3.0-1080p", "kling-3.0-720p",
		"kling-3.0-turbo", "kling-o3", "nano-banana-pro-8k", "gpt-image-2", "veo-3.1", "",
		"leonardo-kling-3.0", "leonardo-kling-3.0-turbo", "leonardo-kling-o3",
		"leonardo-veo-3.1", "leonardo-veo-3.1-fast",
		"sd-2.0", "sd-fast", "sd-mini",
		"sd-2.0-1080p", "sd-2.0-4k", "sd-2.0-720p", "sd-fast-720p", "sd-mini-720p",
		"sd-2.5-720p", // 不属于任何档位族，不该被误隐藏
		"leonardo-seedance-2.0-mini"} { // 已下架，不在隐藏名单里也无所谓
		if got := IsHiddenModel(m); got != hidden[m] {
			t.Errorf("IsHiddenModel(%q) = %v, want %v", m, got, hidden[m])
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

	sd, ok := TieredFamilyOf("sd-2.0")
	if !ok {
		t.Fatal("sd-2.0 应当是档位基名")
	}
	for px, want := range map[int]string{
		0: "", 921600: "", // 不传 / 720p → 基名本身
		2073600: "-1080p", 3686400: "-1080p", // 1080p、2K 都落 1080p 档（没有 2K SKU）
		8294400: "-4k", 33177600: "-4k",
	} {
		if got := sd.SuffixForPixels(px); got != want {
			t.Errorf("sd.SuffixForPixels(%d) = %q, want %q", px, got, want)
		}
	}
	// fast / mini 只有一档：任何像素数都不加后缀
	fast, ok := TieredFamilyOf("sd-fast")
	if !ok {
		t.Fatal("sd-fast 应当是档位基名")
	}
	for _, px := range []int{0, 921600, 2073600, 8294400} {
		if got := fast.SuffixForPixels(px); got != "" {
			t.Errorf("sd-fast.SuffixForPixels(%d) = %q, want \"\"", px, got)
		}
	}
	// 旧名不是基名，显式请求时不该再被加后缀
	for _, m := range []string{"sd-2.0-720p", "sd-fast-720p", "sd-mini-720p", "sd-2.0-4k"} {
		if _, ok := TieredFamilyOf(m); ok {
			t.Errorf("%q 不该被当成档位基名", m)
		}
	}
}
