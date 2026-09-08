package operation_setting

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
)

// LongContextTier 长上下文阶梯计费规则。
//
// 背景:OpenAI GPT-5.x/6 系对超长上下文按更高档单价计费。gpt-5.6 全家 / 5.5 / 5.4 / gpt-6-astra
// 规律一致 —— 阈值 272k,输入 2x、缓存读 2x、输出 1.5x,且**整请求**按高档价计,不是只对超出部分。
// (价格目录字段 input_cost_per_token_above_272k_tokens 等;260908 对上游 6000 条 usage 实测:
//
//	≤272k 档单价/基准 = 1.000,>272k 档 = 2.000(输入/缓存读)、1.500(输出),零例外。)
//
// 我方上游(sub2api,组开关 long_context_pricing_enabled 全开)成本侧已按该阶梯**真实扣费**,
// 营收侧若不跟进,该档必亏(260908 实测毛利 −41%,而普通档 +33%)。
//
// 判据用 PromptTokens(含 cache tokens),与上游 input_tokens+cache_read_tokens 同口径。
// 空列表 = 功能关闭。倍数 <1 视为配置错误并忽略(防止把阶梯写成打折)。
type LongContextTier struct {
	Prefix     string  `json:"prefix"`
	Threshold  int     `json:"threshold"`
	InputMult  float64 `json:"input_mult"`
	OutputMult float64 `json:"output_mult"`
}

var LongContextTiers = []LongContextTier{}

func LongContextTiersToString() string {
	if len(LongContextTiers) == 0 {
		return ""
	}
	b, err := common.Marshal(LongContextTiers)
	if err != nil {
		return ""
	}
	return string(b)
}

func LongContextTiersFromString(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		LongContextTiers = []LongContextTier{}
		return nil
	}
	var parsed []LongContextTier
	if err := common.Unmarshal([]byte(s), &parsed); err != nil {
		return err
	}
	valid := make([]LongContextTier, 0, len(parsed))
	for _, t := range parsed {
		// 阈值必须为正;倍数缺省视为 1(不加价),<1 视为配置错误按 1 处理,绝不打折。
		if strings.TrimSpace(t.Prefix) == "" || t.Threshold <= 0 {
			continue
		}
		if t.InputMult < 1 {
			t.InputMult = 1
		}
		if t.OutputMult < 1 {
			t.OutputMult = 1
		}
		if t.InputMult == 1 && t.OutputMult == 1 {
			continue // 全 1 等于没配,不进表,省得每请求白匹配
		}
		valid = append(valid, t)
	}
	LongContextTiers = valid
	return nil
}

// MatchLongContextTier 返回命中的阶梯规则;未命中返回 nil。
// 同前缀多条时取**阈值最高**的命中项(便于将来配多级阶梯)。
func MatchLongContextTier(model string, promptTokens int) *LongContextTier {
	var hit *LongContextTier
	for i := range LongContextTiers {
		t := &LongContextTiers[i]
		if !strings.HasPrefix(model, t.Prefix) {
			continue
		}
		if promptTokens <= t.Threshold {
			continue
		}
		if hit == nil || t.Threshold > hit.Threshold {
			hit = t
		}
	}
	return hit
}
