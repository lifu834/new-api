package billing_setting

import (
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/samber/lo"
)

const (
	BillingModeRatio      = "ratio"
	BillingModeTieredExpr = "tiered_expr"
	BillingModeField      = "billing_mode"
	BillingExprField      = "billing_expr"
	IgnoreGroupRatioField = "ignore_group_ratio"
)

// BillingSetting is managed by config.GlobalConfig.Register.
// DB keys: billing_setting.billing_mode, billing_setting.billing_expr,
// billing_setting.ignore_group_ratio
type BillingSetting struct {
	BillingMode map[string]string `json:"billing_mode"`
	BillingExpr map[string]string `json:"billing_expr"`
	// IgnoreGroupRatio 列出豁免分组倍率的模型：命中的模型无论从哪个分组调用，
	// 都按标价原样计费（分组倍率按 1.0 处理）。为按次计价模型（如生图）而设——
	// 这类模型卖的是"一次调用"，与 token 分组折扣无关，挂进低倍率分组
	// （例如 codex 0.05）会把单次价格稀释到成本线以下。
	// 支持精确模型名与 * 结尾的前缀通配，例如 "gpt-image-2*"。
	IgnoreGroupRatio []string `json:"ignore_group_ratio"`
}

var billingSetting = BillingSetting{
	BillingMode: make(map[string]string),
	BillingExpr: make(map[string]string),
}

func init() {
	config.GlobalConfig.Register("billing_setting", &billingSetting)
}

// ---------------------------------------------------------------------------
// Read accessors (hot path, must be fast)
// ---------------------------------------------------------------------------

func GetBillingMode(model string) string {
	if mode, ok := billingSetting.BillingMode[model]; ok {
		return mode
	}
	return BillingModeRatio
}

func GetBillingExpr(model string) (string, bool) {
	expr, ok := billingSetting.BillingExpr[model]
	return expr, ok
}

// IsGroupRatioIgnored 判断模型是否豁免分组倍率（详见 BillingSetting.IgnoreGroupRatio）。
//
// 匹配规则：精确模型名，或以 * 结尾的前缀通配（照 ModelPrice 里 "gpt-4-gizmo-*" 的习惯）。
// 裸 "*" 会豁免全站所有模型，属于误配而非合法用法，此处直接忽略；
// 保存时 ValidateIgnoreGroupRatio 也会拒绝它，两处都拦以防绕过 UI 直改 DB。
func IsGroupRatioIgnored(modelName string) bool {
	if modelName == "" {
		return false
	}
	// 取一次切片头，避免遍历期间被配置热更新替换
	patterns := billingSetting.IgnoreGroupRatio
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" || pattern == "*" {
			continue
		}
		if prefix, ok := strings.CutSuffix(pattern, "*"); ok {
			if strings.HasPrefix(modelName, prefix) {
				return true
			}
			continue
		}
		if pattern == modelName {
			return true
		}
	}
	return false
}

// ValidateIgnoreGroupRatio 校验待保存的豁免列表。
func ValidateIgnoreGroupRatio(patterns []string) error {
	for _, pattern := range patterns {
		trimmed := strings.TrimSpace(pattern)
		if trimmed == "" {
			return fmt.Errorf("模型名不能为空")
		}
		if trimmed == "*" {
			return fmt.Errorf("不允许使用裸 \"*\"，那会让全站所有模型都不计分组倍率；请写成具体前缀，例如 \"gpt-image-2*\"")
		}
		if strings.Count(trimmed, "*") > 1 || (strings.Contains(trimmed, "*") && !strings.HasSuffix(trimmed, "*")) {
			return fmt.Errorf("通配符 * 只能出现一次且必须在结尾：%s", trimmed)
		}
	}
	return nil
}

func GetIgnoreGroupRatioCopy() []string {
	return append([]string(nil), billingSetting.IgnoreGroupRatio...)
}

func GetBillingModeCopy() map[string]string {
	return lo.Assign(billingSetting.BillingMode)
}

func GetBillingExprCopy() map[string]string {
	return lo.Assign(billingSetting.BillingExpr)
}

func GetPricingSyncData(base map[string]any) map[string]any {
	extra := make(map[string]any, 2)
	if modes := GetBillingModeCopy(); len(modes) > 0 {
		extra[BillingModeField] = modes
	}
	if exprs := GetBillingExprCopy(); len(exprs) > 0 {
		extra[BillingExprField] = exprs
	}
	return lo.Assign(base, extra)
}

// ---------------------------------------------------------------------------
// Smoke test (called externally for validation before save)
// ---------------------------------------------------------------------------

func SmokeTestExpr(exprStr string) error {
	return smokeTestExpr(exprStr)
}

func smokeTestExpr(exprStr string) error {
	vectors := []billingexpr.TokenParams{
		{P: 0, C: 0, Len: 0},
		{P: 1000, C: 1000, Len: 1000},
		{P: 100000, C: 100000, Len: 100000},
		{P: 1000000, C: 1000000, Len: 1000000},
	}
	requests := []billingexpr.RequestInput{
		{},
		{
			Headers: map[string]string{
				"anthropic-beta": "fast-mode-2026-02-01",
			},
			Body: []byte(`{"service_tier":"fast","stream_options":{"include_usage":true},"messages":[1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21]}`),
		},
	}

	for _, v := range vectors {
		for _, request := range requests {
			result, _, err := billingexpr.RunExprWithRequest(exprStr, v, request)
			if err != nil {
				return fmt.Errorf("vector {p=%g, c=%g}: run failed: %w", v.P, v.C, err)
			}
			if result < 0 {
				return fmt.Errorf("vector {p=%g, c=%g}: result %f < 0", v.P, v.C, result)
			}
		}
	}
	return nil
}
