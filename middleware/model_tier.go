package middleware

import (
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"

	"github.com/gin-gonic/gin"
)

// 档位模型对外只暴露基名，分辨率由请求的 size 决定。
//
// 档位必须在**分发之前**翻成带后缀的内部 SKU，因为模型名同时驱动三件事：
// 计价（controller/relay.go 的 ModelPriceHelper 取 info.OriginModelName）、
// 选渠道（abilities 匹配）、以及消费日志。晚一步改就会出现"按基名选渠道、
// 按档位名计价"这种错位。
//
// 账单与日志记的是**解析后**的 SKU（如 nano-banana-pro-4k / kling-3.0-1080p）：
// 参数会改价，不让客户看见变的是什么更糟。

// pixelsOfSize 从 "1920x1080" 这类尺寸串算总像素。
// 只给比例（"16:9"）、缺失或非法时返回 0 —— 调用方据此落最便宜的一档。
func pixelsOfSize(size string) int {
	s := strings.TrimSpace(strings.ToLower(size))
	if s == "" || strings.Contains(s, ":") {
		return 0
	}
	parts := strings.Split(s, "x")
	if len(parts) != 2 {
		return 0
	}
	w, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	h, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil || w <= 0 || h <= 0 {
		return 0
	}
	return w * h
}

// withTierSuffix 把对外的基名翻成内部档位 SKU。
// 已经带后缀的名字（客户显式请求 kling-3.0-1080p）不是基名，原样放过，不重复加。
func withTierSuffix(c *gin.Context, model string) string {
	family, ok := constant.TieredFamilyOf(model)
	if !ok {
		return model
	}
	var body struct {
		Size string `json:"size"`
	}
	if err := common.UnmarshalBodyReusable(c, &body); err != nil {
		return model
	}
	return model + family.SuffixForPixels(pixelsOfSize(body.Size))
}
