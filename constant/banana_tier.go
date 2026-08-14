package constant

import "strings"

// nano-banana 对外只暴露基名，1K/2K/4K 三档由请求的 size 决定，
// 分发中间件把它翻成带后缀的内部 SKU（middleware/banana_tier.go），
// /v1/models 则把这些 SKU 隐藏起来（controller/model.go）。
//
// 两处必须用同一份清单——各写各的，一旦不一致就会出现"隐藏了却没改写"
// 或"改写了却还列出来"这类静默错误，所以放在这里做单一来源。
var BananaTieredBaseModels = []string{"nano-banana-pro", "nano-banana-2"}

// IsBananaTieredBaseModel 判断是否是需要按 size 翻档位的对外基名。
func IsBananaTieredBaseModel(model string) bool {
	for _, base := range BananaTieredBaseModels {
		if model == base {
			return true
		}
	}
	return false
}

// IsBananaHiddenTierSKU 判断是否是不该出现在 /v1/models 里的内部档位 SKU。
// 只隐藏，不从 abilities 摘除——显式请求 nano-banana-pro-4k 仍照常工作。
func IsBananaHiddenTierSKU(model string) bool {
	for _, base := range BananaTieredBaseModels {
		if suffix, ok := strings.CutPrefix(model, base); ok {
			if suffix == "-2k" || suffix == "-4k" {
				return true
			}
		}
	}
	return false
}
