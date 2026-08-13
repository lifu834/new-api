package gemini

import (
	"encoding/json"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"math"
	"strconv"
	"strings"
)

// geminiImageConfig 对应 generationConfig.imageConfig —— Gemini 原生生图（nano-banana 系）
// 控制出图分辨率与宽高比的地方。dto 里它是 json.RawMessage，所以这里自己拼。
type geminiImageConfig struct {
	ImageSize   string `json:"imageSize,omitempty"`
	AspectRatio string `json:"aspectRatio,omitempty"`
}

// imageSizeFromModel 从模型名后缀取档位。
//
// 我们按档位定价（1K / 2K / 4K 三个 SKU 卖不同的钱），所以档位必须由**我们**钉死，
// 不能省略了让上游取默认值：上游模型一次静默更新就可能把默认档从 1K 变成别的，
// 客户拿到的东西和付的钱就对不上了。因此不带后缀 = 显式 1K。
func imageSizeFromModel(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.HasSuffix(m, "-4k"):
		return "4K"
	case strings.HasSuffix(m, "-2k"):
		return "2K"
	default:
		return "1K"
	}
}

// Gemini 生图支持的宽高比白名单。
var geminiAspectRatios = []struct {
	name  string
	value float64
}{
	{"1:1", 1.0 / 1.0},
	{"2:3", 2.0 / 3.0},
	{"3:2", 3.0 / 2.0},
	{"3:4", 3.0 / 4.0},
	{"4:3", 4.0 / 3.0},
	{"4:5", 4.0 / 5.0},
	{"5:4", 5.0 / 4.0},
	{"9:16", 9.0 / 16.0},
	{"16:9", 16.0 / 9.0},
	{"21:9", 21.0 / 9.0},
}

// aspectRatioTolerance 是"够接近就吸附"的相对误差上限。
// 1024x1792（OpenAI 的竖版档）实际是 4:7=0.5714，离 9:16=0.5625 差 1.6%，
// 应当吸到 9:16；差得更远的就宁可不传，让上游用模型默认比例。
const aspectRatioTolerance = 0.03

// aspectRatioFromSize 把 OpenAI 的 size 参数翻成 Gemini 的 aspectRatio。
// 允许客户直接写 "16:9" 这种比例式（与 imagen 分支的既有行为一致）。
// 落不进白名单就返回空串 —— 省略该字段，而不是擅自改成别的比例。
func aspectRatioFromSize(size string) string {
	s := strings.TrimSpace(size)
	if s == "" {
		return ""
	}
	if strings.Contains(s, ":") {
		for _, ar := range geminiAspectRatios {
			if ar.name == s {
				return s
			}
		}
		return ""
	}

	parts := strings.Split(strings.ToLower(s), "x")
	if len(parts) != 2 {
		return ""
	}
	w, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	h, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil || w <= 0 || h <= 0 {
		return ""
	}

	want := float64(w) / float64(h)
	best, bestErr := "", math.MaxFloat64
	for _, ar := range geminiAspectRatios {
		if e := math.Abs(ar.value-want) / ar.value; e < bestErr {
			best, bestErr = ar.name, e
		}
	}
	if bestErr > aspectRatioTolerance {
		return ""
	}
	return best
}

// buildGeminiImageConfig 组装 imageConfig。档位取自客户请求的模型名（计费口径），
// 不是映射后的上游名 —— 上游名各家不同（rolldek 是 gemini-3-pro-image-preview，
// 星桥是 nano-banana-pro-4k），只有我们自己的 SKU 名才是档位的唯一真相。
func buildGeminiImageConfig(billingModel, size string) json.RawMessage {
	cfg := geminiImageConfig{
		ImageSize:   imageSizeFromModel(billingModel),
		AspectRatio: aspectRatioFromSize(size),
	}
	b, err := common.Marshal(cfg)
	if err != nil {
		return nil
	}
	return json.RawMessage(b)
}

// geminiNoImageReason 从"没有图"的响应里榨出可读的原因。
// 上游给 200 却不给图通常是三种情况：安全策略拦掉（promptFeedback.blockReason
// 或 finishReason=IMAGE_SAFETY）、生成被截断（MAX_TOKENS）、或者模型改用文字回答。
func geminiNoImageReason(resp *dto.GeminiChatResponse) string {
	if resp == nil {
		return "empty response"
	}
	parts := make([]string, 0, 3)
	if resp.PromptFeedback != nil && resp.PromptFeedback.BlockReason != nil && *resp.PromptFeedback.BlockReason != "" {
		parts = append(parts, "blockReason="+*resp.PromptFeedback.BlockReason)
	}
	for _, cand := range resp.Candidates {
		if cand.FinishReason != nil && *cand.FinishReason != "" {
			parts = append(parts, "finishReason="+*cand.FinishReason)
		}
		for _, p := range cand.Content.Parts {
			if txt := strings.TrimSpace(p.Text); txt != "" {
				if len(txt) > 200 {
					txt = txt[:200] + "..."
				}
				parts = append(parts, "text="+txt)
			}
		}
	}
	if len(parts) == 0 {
		return "no candidates"
	}
	return strings.Join(parts, "; ")
}
