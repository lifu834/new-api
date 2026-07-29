package helper

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// setupIgnoreGroupRatioEnv 造一套贴近线上的分组倍率：codex 基础 0.14，
// enterprise 用户走 codex 时特殊倍率 0.05，image 分组 1.0。
func setupIgnoreGroupRatioEnv(t *testing.T, ignoreList string) {
	t.Helper()

	savedConfig := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(key, value string) error {
		savedConfig[key] = value
		return nil
	}))
	savedGroupRatio := ratio_setting.GroupRatio2JSONString()
	savedGroupGroupRatio := ratio_setting.GroupGroupRatio2JSONString()
	savedModelPrice := ratio_setting.ModelPrice2JSONString()
	t.Cleanup(func() {
		require.NoError(t, config.GlobalConfig.LoadFromDB(savedConfig))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(savedGroupRatio))
		require.NoError(t, ratio_setting.UpdateGroupGroupRatioByJSONString(savedGroupGroupRatio))
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(savedModelPrice))
	})

	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(
		`{"default":1,"codex":0.14,"image":1,"enterprise":1}`))
	require.NoError(t, ratio_setting.UpdateGroupGroupRatioByJSONString(
		`{"enterprise":{"codex":0.05}}`))
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(
		`{"gpt-image-2-1k":0.02,"dall-e-3":0.04}`))
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"billing_setting." + billing_setting.IgnoreGroupRatioField: ignoreList,
		"billing_setting.billing_mode":                             `{"gpt-image-2":"tiered_expr"}`,
		"billing_setting.billing_expr":                             `{"gpt-image-2":"tier(\"flat\", 60000)"}`,
	}))
}

func newRatioTestCtx(t *testing.T) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	req.Body = nil
	req.ContentLength = 0
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	return ctx
}

// 命中豁免名单的模型：无论从哪个分组调用，倍率都必须是 1.0，
// 且不能被 GroupGroupRatio 的特殊倍率覆盖。
func TestHandleGroupRatioIgnoresListedModels(t *testing.T) {
	setupIgnoreGroupRatioEnv(t, `["gpt-image-2*","ex-gpt-image-2*"]`)

	cases := []struct {
		name           string
		model          string
		userGroup      string
		usingGroup     string
		wantRatio      float64
		wantHasSpecial bool
	}{
		// 豁免命中
		{"精确名 + 企业走codex特殊倍率", "gpt-image-2", "enterprise", "codex", 1.0, false},
		{"通配命中 + 企业走codex", "gpt-image-2-pool", "enterprise", "codex", 1.0, false},
		{"通配命中 + 普通用户走codex", "gpt-image-2-1k", "default", "codex", 1.0, false},
		{"豁免模型走image分组仍是1", "gpt-image-2", "enterprise", "image", 1.0, false},
		{"ex 前缀命中", "ex-gpt-image-2", "vvip", "codex", 1.0, false},
		// 未命中：原有分组倍率逻辑必须原样保留
		{"未命中 + 企业走codex吃特殊倍率", "gpt-5.6-sol", "enterprise", "codex", 0.05, true},
		{"未命中 + 普通用户走codex吃基础倍率", "gpt-5.6-sol", "default", "codex", 0.14, false},
		{"未命中 + 走image分组", "claude-sonnet-4-6", "default", "image", 1.0, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := newRatioTestCtx(t)
			info := &relaycommon.RelayInfo{
				OriginModelName: tc.model,
				UserGroup:       tc.userGroup,
				UsingGroup:      tc.usingGroup,
			}
			got := HandleGroupRatio(ctx, info)
			require.Equal(t, tc.wantRatio, got.GroupRatio)
			require.Equal(t, tc.wantHasSpecial, got.HasSpecialRatio)
			// UsingGroup 不能被豁免逻辑改写——渠道选择和日志仍看真实分组
			require.Equal(t, tc.usingGroup, info.UsingGroup)
		})
	}
}

// 按次计价（ModelPrice）模型：预扣额度必须是标价原价，不再乘分组倍率。
func TestModelPriceHelperPerCallIgnoresGroupRatio(t *testing.T) {
	setupIgnoreGroupRatioEnv(t, `["gpt-image-2*"]`)

	// $0.02 * QuotaPerUnit，倍率被豁免
	ctx := newRatioTestCtx(t)
	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-image-2-1k",
		UserGroup:       "enterprise",
		UsingGroup:      "codex",
	}
	priceData, err := ModelPriceHelperPerCall(ctx, info)
	require.NoError(t, err)
	require.Equal(t, 1.0, priceData.GroupRatioInfo.GroupRatio)
	require.Equal(t, int(0.02*common.QuotaPerUnit), priceData.Quota)

	// 对照组：未登记的按次模型仍吃 0.05
	ctx2 := newRatioTestCtx(t)
	info2 := &relaycommon.RelayInfo{
		OriginModelName: "dall-e-3",
		UserGroup:       "enterprise",
		UsingGroup:      "codex",
	}
	priceData2, err := ModelPriceHelperPerCall(ctx2, info2)
	require.NoError(t, err)
	require.Equal(t, 0.05, priceData2.GroupRatioInfo.GroupRatio)
	require.Equal(t, int(0.04*common.QuotaPerUnit*0.05), priceData2.Quota)
}

// tiered_expr 模型：豁免后表达式常量直接落地，且冻结进结算快照的倍率也必须是 1.0
// （否则会出现"预扣不乘、结算乘"的撕裂）。
func TestModelPriceHelperTieredIgnoresGroupRatio(t *testing.T) {
	setupIgnoreGroupRatioEnv(t, `["gpt-image-2*"]`)

	ctx := newRatioTestCtx(t)
	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-image-2",
		UserGroup:       "enterprise",
		UsingGroup:      "codex",
		RequestHeaders:  map[string]string{"Content-Type": "application/json"},
		BillingRequestInput: &billingexpr.RequestInput{
			Headers: map[string]string{"Content-Type": "application/json"},
			Body:    []byte(`{"size":"1024x1024"}`),
		},
	}

	priceData, err := ModelPriceHelper(ctx, info, 0, &types.TokenCountMeta{})
	require.NoError(t, err)
	require.Equal(t, 1.0, priceData.GroupRatioInfo.GroupRatio)
	// 60000 / 1e6 * QuotaPerUnit = $0.06
	require.Equal(t, int(60000.0/1_000_000*common.QuotaPerUnit), priceData.QuotaToPreConsume)

	require.NotNil(t, info.TieredBillingSnapshot)
	require.Equal(t, 1.0, info.TieredBillingSnapshot.GroupRatio)
	require.Equal(t, "flat", info.TieredBillingSnapshot.EstimatedTier)
}

func TestModelPriceHelperTieredUsesPreloadedRequestInput(t *testing.T) {
	gin.SetMode(gin.TestMode)

	saved := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(key, value string) error {
		saved[key] = value
		return nil
	}))
	t.Cleanup(func() {
		require.NoError(t, config.GlobalConfig.LoadFromDB(saved))
	})

	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"billing_setting.billing_mode": `{"tiered-test-model":"tiered_expr"}`,
		"billing_setting.billing_expr": `{"tiered-test-model":"param(\"stream\") == true ? tier(\"stream\", p * 3) : tier(\"base\", p * 2)"}`,
	}))

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/api/channel/test/1", nil)
	req.Body = nil
	req.ContentLength = 0
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	ctx.Set("group", "default")

	info := &relaycommon.RelayInfo{
		OriginModelName: "tiered-test-model",
		UserGroup:       "default",
		UsingGroup:      "default",
		RequestHeaders:  map[string]string{"Content-Type": "application/json"},
		BillingRequestInput: &billingexpr.RequestInput{
			Headers: map[string]string{"Content-Type": "application/json"},
			Body:    []byte(`{"stream":true}`),
		},
	}

	priceData, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})
	require.NoError(t, err)
	require.Equal(t, 1500, priceData.QuotaToPreConsume)
	require.NotNil(t, info.TieredBillingSnapshot)
	require.Equal(t, "stream", info.TieredBillingSnapshot.EstimatedTier)
	require.Equal(t, billing_setting.BillingModeTieredExpr, info.TieredBillingSnapshot.BillingMode)
	require.Equal(t, common.QuotaPerUnit, info.TieredBillingSnapshot.QuotaPerUnit)
}
