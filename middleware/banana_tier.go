package middleware

import (
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"

	"github.com/gin-gonic/gin"
)

// nano-banana 对外只暴露两个模型名，1K/2K/4K 三档由请求的 size 决定。
//
// 但档位必须在**分发之前**翻成带后缀的内部 SKU，因为模型名同时驱动三件事：
// 计价（controller/relay.go 的 ModelPriceHelper 取 info.OriginModelName）、
// 选渠道（abilities 匹配）、以及消费日志。晚一步改就会出现"按 pro 选渠道、
// 按 pro-4k 计价"这种错位。
//
// 客户账单和日志里记的是**解析后**的 SKU（nano-banana-pro-4k），
// 这样虽然请求名与账单名不同，但客户能看出自己买的是哪一档——
// 参数会改价，不让他看见变的是什么更糟。
// 档位按**总像素**分，比例不影响档位（比例只决定出图形状）。
// 三档的名义像素是 1024²=1.05M、2048²=4.19M、4096²=16.8M，
// 分界取在能把常见尺寸分对的位置：
//   - 1536x1024=1.57M、1792x1024=1.83M 属 1K
//   - 2048x1152=2.36M、2048x2048=4.19M 属 2K
//   - 3840x2160=8.29M、4096x4096=16.8M 属 4K
//
// 注意 4K 分界不能用几何中点 8.39M —— 那会把 3840x2160（8,294,400，
// 正是我们自己的真 4K 判据）错分到 2K。
const (
	tier2KMinPixels = 2_000_000
	tier4KMinPixels = 6_000_000
)

// bananaTierSuffix 由 size 得出档位后缀。size 缺失或只给了比例（如 "16:9"）时
// 按 1K —— 默认落在最便宜的一档，客户不会因为没传参数而被多收钱。
func bananaTierSuffix(size string) string {
	s := strings.TrimSpace(strings.ToLower(size))
	if s == "" || strings.Contains(s, ":") {
		return ""
	}
	parts := strings.Split(s, "x")
	if len(parts) != 2 {
		return ""
	}
	w, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	h, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil || w <= 0 || h <= 0 {
		return ""
	}
	switch px := w * h; {
	case px >= tier4KMinPixels:
		return "-4k"
	case px >= tier2KMinPixels:
		return "-2k"
	default:
		return ""
	}
}

// withBananaTierSuffix 把对外的基名翻成内部档位 SKU。
// 已经带后缀的名字（客户显式请求 nano-banana-pro-4k）原样放过，不重复加。
func withBananaTierSuffix(c *gin.Context, model string) string {
	if !constant.IsBananaTieredBaseModel(model) {
		return model
	}
	var body struct {
		Size string `json:"size"`
	}
	if err := common.UnmarshalBodyReusable(c, &body); err != nil {
		return model
	}
	return model + bananaTierSuffix(body.Size)
}
