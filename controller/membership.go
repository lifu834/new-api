package controller

import (
	"net/http"

	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/gin-gonic/gin"
)

// GET /api/membership/tiers —— 会员四档的门槛与逐分组倍率（匿名可读）。
//
// 为什么要有这个接口：/api/pricing 的 group_ratio 已经按**调用者自己的档位**折算过了，
// 登录用户只看得见自己那一档。会员页与兑换页要并排展示四档差价，只能另开一个口子。
// 硬编码矩阵是踩过的坑（价格会漂，文档里的假数字比没有更糟），所以这里实时读
// options.GroupRatio（挂牌价）+ options.GroupGroupRatio（档位交叉倍率）。
//
// 门槛按**累计充值**核定（260908 拍板），与 nycatai-ops/scripts/tier-scan.py 同源；
// 这里只报数，不做判定——用户当前档位以 /api/user/self 的 group 为准。
//
// 只暴露对客的计费分组：内部仿真（qa/test）、直连专用（vip）和档位组本身
// （default/enterprise/vvip 作为 usingGroup 时无渠道）都不列。

type membershipTier struct {
	Group     string             `json:"group"`
	Name      string             `json:"name"`
	En        string             `json:"en"`
	Threshold int                `json:"threshold"` // 累计充值门槛，单位元
	Ratios    map[string]float64 `json:"ratios"`    // 计费分组 → 该档实付倍率
}

// 四档：内部 key 固定，改 key 是全链路手术（vvip 硬编码在计费表达式 / X-User-Tier /
// 豁免名单 / GroupGroupRatio 四处），只改展示名。
var membershipLadder = []struct {
	Group     string
	Name      string
	En        string
	Threshold int
}{
	{"default", "标准", "Standard", 0},
	{"plus", "进阶", "Plus", 100},
	{"enterprise", "专业", "Pro", 500},
	{"vvip", "旗舰", "Max", 2000},
}

// 对客展示的计费分组与顺序。新增计费分组要手工加进来——宁可漏列也不要把内部组
// （qa 仿真流量、vip 直连）漏给用户看。
var membershipBillingGroups = []struct {
	Group string `json:"group"`
	Label string `json:"label"`
}{
	{"codex", "特价 GPT"},
	{"codex_pro", "特价 GPT Pro"},
	{"claude", "Claude"},
	{"claude_cursor", "Claude Cursor"},
	{"gemini", "Gemini"},
	{"grok", "Grok"},
	{"max_claude_limited", "满血 Max"},
	{"max_claude_unlimited", "满血 Max 不限"},
}

func GetMembershipTiers(c *gin.Context) {
	base := ratio_setting.GetGroupRatioCopy()

	tiers := make([]membershipTier, 0, len(membershipLadder))
	for _, l := range membershipLadder {
		ratios := make(map[string]float64, len(membershipBillingGroups))
		for _, bg := range membershipBillingGroups {
			// 该档没有交叉倍率条目 = 按挂牌价计费，如实回落，不要假装有优惠
			if r, ok := ratio_setting.GetGroupGroupRatio(l.Group, bg.Group); ok {
				ratios[bg.Group] = r
			} else if r, ok := base[bg.Group]; ok {
				ratios[bg.Group] = r
			}
		}
		tiers = append(tiers, membershipTier{
			Group: l.Group, Name: l.Name, En: l.En, Threshold: l.Threshold, Ratios: ratios,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"basis":  "recharge", // 门槛口径：累计充值
			"tiers":  tiers,
			"groups": membershipBillingGroups,
		},
	})
}
