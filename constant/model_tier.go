package constant

import "strings"

// 档位模型族：对外只暴露一个基名，分辨率由请求的 size 决定，
// 分发中间件按总像素把它翻成带后缀的内部 SKU（middleware/model_tier.go），
// /v1/models 再把这些 SKU 隐藏（controller/model.go）。
//
// 为什么必须翻成不同的模型名而不是只传参数：模型名同时驱动**计价**、**选渠道**
// 和**消费日志**三件事。分辨率不同成本就不同（banana 2K/4K 上游更贵；
// kling 1080p 与 720p 是两个不同的上游模型），必须让它们是不同的计费口径。
//
// 两处（改写 + 隐藏）必须用同一份表，各写各的一旦不一致就会出现
// "隐藏了却没改写"或"改写了却还列出来"这类静默错误。

// ModelTier 一个档位：达到 MinPixels 就用 Suffix。表内按 MinPixels 升序。
type ModelTier struct {
	MinPixels int
	Suffix    string
}

// TieredModelFamily 一族共享同一套档位阶梯的模型。
type TieredModelFamily struct {
	Bases []string    // 对外暴露的基名
	Tiers []ModelTier // 档位阶梯，第一条必须是 MinPixels=0 的默认档
	// Aliases 是同样要从 /v1/models 隐藏的历史遗留名（与某个档位等价，
	// 但不是由基名+后缀拼出来的，例如 kling-3.0-720p 等同于基名本身）。
	Aliases []string
}

// 档位分界取"能把常见尺寸分对"的位置，不取几何中点——
// 中点会把 3840x2160（8,294,400，正是我们的真 4K 判据）错分到 2K 档。
var TieredModelFamilies = []TieredModelFamily{
	{
		// nano-banana：1024²=1.05M / 2048²=4.19M / 4096²=16.8M
		Bases: []string{"nano-banana-pro", "nano-banana-2"},
		Tiers: []ModelTier{{0, ""}, {2_000_000, "-2k"}, {6_000_000, "-4k"}},
	},
	{
		// kling：1280x720=921,600 / 1920x1080=2,073,600
		// ⚠️ 两家上游的分辨率机制不同：rolldek 编在模型名里
		// （kling-3.0-omni-720p/-1080p），leo 靠请求体里的 size 透传。
		// 改写模型名同时满足两者：rolldek 靠 model_mapping 拿到对应上游名，
		// leo 则因为客户原本的 size 还在请求体里而自然出对应分辨率。
		Bases:   []string{"kling-3.0"},
		Tiers:   []ModelTier{{0, ""}, {1_500_000, "-1080p"}},
		Aliases: []string{"kling-3.0-720p"},
	},
}

// TieredFamilyOf 返回该模型名所属的档位族（仅当它是对外基名时）。
func TieredFamilyOf(model string) (*TieredModelFamily, bool) {
	for i := range TieredModelFamilies {
		for _, b := range TieredModelFamilies[i].Bases {
			if model == b {
				return &TieredModelFamilies[i], true
			}
		}
	}
	return nil, false
}

// SuffixForPixels 按总像素选档位后缀。像素数为 0（size 缺失或只给了比例）时
// 落最便宜的一档——客户不该因为没传参数而被多收钱。
func (f *TieredModelFamily) SuffixForPixels(px int) string {
	suffix := ""
	for _, t := range f.Tiers {
		if px >= t.MinPixels {
			suffix = t.Suffix
		}
	}
	return suffix
}

// IsHiddenTierSKU 判断是否是不该出现在 /v1/models 里的内部档位 SKU。
// 只隐藏，不从 abilities 摘除——显式请求 nano-banana-pro-4k / kling-3.0-1080p
// 的老调用仍照常工作。
func IsHiddenTierSKU(model string) bool {
	for _, f := range TieredModelFamilies {
		for _, a := range f.Aliases {
			if model == a {
				return true
			}
		}
		for _, b := range f.Bases {
			rest, ok := strings.CutPrefix(model, b)
			if !ok || rest == "" {
				continue
			}
			for _, t := range f.Tiers {
				if t.Suffix != "" && rest == t.Suffix {
					return true
				}
			}
		}
	}
	return false
}
