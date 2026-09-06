package constant

import "testing"

func TestIsHiddenModel(t *testing.T) {
	hidden := map[string]bool{
		// 只剩历史遗留重复名：同渠道同价，只是把供应商写进了名字里
		"leonardo-kling-3.0": true, "leonardo-kling-3.0-turbo": true,
		"leonardo-kling-o3": true, "leonardo-veo-3.1": true,
		"leonardo-veo-3.1-fast": true,
		// 260826：生图线也取消了裸名（nano-banana-*-1k/-2k/-4k 六个显式 SKU），
		// nano-banana 族随之移出档位表 ⇒ **2K/4K 不再被隐藏**。
		// 此前它们是"基名+后缀拼出的内部 SKU"而被藏起来，客户根本发现不了在售的高档位；
		// 视频线吃过同样的亏（sd-2.0-720p 等六个主力 SKU 一度整个消失）。
	}
	// 全部必须**可见**：显式 SKU 名就是对外名本身，无关模型也不该被误伤
	for _, m := range []string{
		// 生图六个在售 SKU
		"nano-banana-2-1k", "nano-banana-2-2k", "nano-banana-2-4k",
		"nano-banana-pro-1k", "nano-banana-pro-2k", "nano-banana-pro-4k",
		// 已废弃的裸名：不在任何渠道，也不该被特殊对待
		"nano-banana-pro", "nano-banana-2", "kling-3.0",
		// 不存在的档位，不该被规则误判
		"nano-banana-pro-8k",
		"kling-3.0-1080p", "kling-3.0-720p", "kling-3.0-turbo", "kling-o3",
		"gpt-image-2", "gpt-image-2-1k", "veo-3.1", "",
		"leonardo-kling-3.0", "leonardo-kling-3.0-turbo", "leonardo-kling-o3",
		"leonardo-veo-3.1", "leonardo-veo-3.1-fast",
		"sd-2.0", "sd-fast", "sd-mini",
		"sd-2.0-1080p", "sd-2.0-4k", "sd-2.0-720p", "sd-fast-720p", "sd-mini-720p",
		"sd-2.5-720p",
		"leonardo-seedance-2.0-mini"} {
		if got := IsHiddenModel(m); got != hidden[m] {
			t.Errorf("IsHiddenModel(%q) = %v, want %v", m, got, hidden[m])
		}
	}
}

// TestNoTieredFamilies 守住 260826 的决定：**全线改用显式分辨率 SKU 名**，
// 档位不再由 size 隐式改写。这张表现在应当是空的。
//
// 之所以正向断言"空"而不是删掉测试：这张表一旦被重新填上，会同时恢复两个行为
// —— 按 size 改写模型名、以及把 `基名+后缀` 的 SKU 从 /v1/models 隐藏。
// 后者是真正有害的那个（藏掉在售 SKU），必须有测试挡着，不能靠记性。
func TestNoTieredFamilies(t *testing.T) {
	if len(TieredModelFamilies) != 0 {
		t.Fatalf("TieredModelFamilies 应为空（全线用显式 SKU 名），实际有 %d 族",
			len(TieredModelFamilies))
	}
	// 曾经的基名，如今都不该再被当成档位基名
	for _, m := range []string{
		"nano-banana-pro", "nano-banana-2",
		"kling-3.0", "sd-2.0", "sd-fast", "sd-mini", "kling-3.0-turbo",
	} {
		if _, ok := TieredFamilyOf(m); ok {
			t.Errorf("%q 不该再是档位基名（260826 起全线用显式分辨率名）", m)
		}
	}
	// 反向不变量：显式 SKU 名任何情况下都不该被当成基名，
	// 否则会被二次加后缀（nano-banana-2-2k-4k / sd-2.0-720p-1080p 这种）
	for _, m := range []string{
		"nano-banana-2-1k", "nano-banana-2-2k", "nano-banana-2-4k",
		"nano-banana-pro-1k", "nano-banana-pro-2k", "nano-banana-pro-4k",
		"sd-2.0-720p", "sd-2.0-1080p", "sd-2.0-4k",
		"sd-fast-720p", "sd-mini-720p",
		"kling-3.0-1080p", "kling-3.0-720p",
	} {
		if _, ok := TieredFamilyOf(m); ok {
			t.Errorf("%q 不该被当成档位基名", m)
		}
	}
}

// TestSuffixForPixels 档位阶梯本身的算法没变（将来真有"一个基名多档位"的上游
// 还会用到），用一个**本地构造**的族来测，不依赖生产表的内容。
func TestSuffixForPixels(t *testing.T) {
	f := TieredModelFamily{
		Bases: []string{"demo"},
		Tiers: []ModelTier{{0, ""}, {2_000_000, "-2k"}, {6_000_000, "-4k"}},
	}
	for px, want := range map[int]string{
		0: "", 1048576: "", 2073600: "-2k", 4194304: "-2k",
		// 3840x2160 = 8,294,400 是我们的真 4K 判据，必须落 -4k 而不是 -2k
		8294400: "-4k", 16777216: "-4k",
	} {
		if got := f.SuffixForPixels(px); got != want {
			t.Errorf("SuffixForPixels(%d) = %q, want %q", px, got, want)
		}
	}
}
