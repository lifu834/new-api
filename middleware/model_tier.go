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

// tierNamePixels 把 "1080p" / "4k" 这类档位串折成代表性像素数，
// 好让它和 "1920x1080" 这种尺寸串走同一套档位判定。
// 取值用该档的标准分辨率，只要落在正确的区间即可，不必精确。
var tierNamePixels = map[string]int{
	"480p":  640 * 480,
	"540p":  960 * 540,
	"720p":  1280 * 720,
	"hd":    1280 * 720,
	"1080p": 1920 * 1080,
	"fhd":   1920 * 1080,
	"1440p": 2560 * 1440,
	"2k":    2560 * 1440,
	"2160p": 3840 * 2160,
	"4k":    3840 * 2160,
	"uhd":   3840 * 2160,
}

func pixelsOfTierName(s string) int {
	return tierNamePixels[strings.TrimSpace(strings.ToLower(s))]
}

// pixelsOfRequest 取请求想要的总像素。
//
// 认两个字段：`size`（生图那边的写法）与 `resolution`（视频那边的写法），
// 每个字段都同时接受 "1920x1080" 与 "1080p" 两种形态。
// 🔑 视频组的客户用的是 `resolution`，只读 `size` 会让他们的 1080p 请求
// 静默落到最便宜的一档——付 720p 的钱、拿 720p 的片，正是档位机制要防的事。
//
// 都取不到时返回 0，调用方据此落最便宜的一档（没传参数不该被多收钱）。
func pixelsOfRequest(c *gin.Context) int {
	var body struct {
		Size       string `json:"size"`
		Resolution string `json:"resolution"`
	}
	if err := common.UnmarshalBodyReusable(c, &body); err != nil {
		return 0
	}
	for _, v := range []string{body.Size, body.Resolution} {
		if px := pixelsOfSize(v); px > 0 {
			return px
		}
		if px := pixelsOfTierName(v); px > 0 {
			return px
		}
	}
	return 0
}

// withTierSuffix 把对外的基名翻成内部档位 SKU。
// 已经带后缀的名字（客户显式请求 kling-3.0-1080p）不是基名，原样放过，不重复加。
func withTierSuffix(c *gin.Context, model string) string {
	family, ok := constant.TieredFamilyOf(model)
	if !ok {
		return model
	}
	return model + family.SuffixForPixels(pixelsOfRequest(c))
}
