package constant

import "strings"

type TaskPlatform string

const (
	TaskPlatformSuno       TaskPlatform = "suno"
	TaskPlatformMidjourney              = "mj"
)

const (
	SunoActionMusic  = "MUSIC"
	SunoActionLyrics = "LYRICS"

	TaskActionGenerate          = "generate"
	TaskActionTextGenerate      = "textGenerate"
	TaskActionFirstTailGenerate = "firstTailGenerate"
	TaskActionReferenceGenerate = "referenceGenerate"
	TaskActionRemix             = "remixGenerate"
)

// 任务型（视频 / 图像）模型的计费单位，按**承接渠道类型**判定：
// 下列渠道的 EstimateBilling 会返回 seconds 乘数 ⇒ ModelPrice 是「每秒」单价；
// 其余任务渠道走 taskcommon.BaseBilling（无乘数）⇒ ModelPrice 是「每次」一口价。
// /api/pricing 用它给每个按次模型标注 billing_unit，模型广场据此显示 按秒 / 按次 / 按张，
// 不必再用「按秒模型按实际时长结算」这类通用文案兜底。
// ⚠️ 新增按秒 adaptor 时必须同步加进来，否则前端会把按秒模型标成按次（少标不多标）。
const (
	BillingUnitSecond = "second"
	BillingUnitCall   = "call"
	BillingUnitMixed  = "mixed" // 同一对外名同时挂了按秒与按次渠道——配置异常，前端如实标出
)

var perSecondTaskChannelTypes = map[int]bool{
	ChannelTypeSora:             true, // video2api / leonardo2api / yunshu 等同形态渠道也是 55
	ChannelTypeAdobe2ApiVideo:   true,
	ChannelTypeSecureSkillVideo: true,
	ChannelTypeDimensioVideo:    true,
	ChannelTypeMaiTokenVideo:    true,
	ChannelTypeSudashuiVideo:    true,
}

func IsPerSecondTaskChannel(channelType int) bool { return perSecondTaskChannelTypes[channelType] }

// 按次一口价的视频 SKU：挂在按秒渠道（type 55 video2api）上，但卖的是「一条片子」，
// 不乘时长。依据：站内文档承诺「时长不影响价格」+ 8 月实扣口径（seedance-2.0 1425000 /
// minimax-h3-2k 1750000 quota = 1× 标价）。260907 发现它们迁到 #201 后 sora 适配器会乘
// 缺省 5 秒 ⇒ 多收 5 倍，故在此白名单。sora 适配器 EstimateBilling 与 /api/pricing 的
// billing_unit 共用这一份，两边不会再各说各话。精确模型名匹配（小写）。
var perCallVideoSKUs = map[string]bool{
	"seedance-2.0":  true,
	"minimax-h3-2k": true,
}

func IsPerCallVideoSKU(model string) bool {
	return perCallVideoSKUs[strings.ToLower(strings.TrimSpace(model))]
}

// TaskBillingUnitForModel 在渠道判定之上叠加按次 SKU 白名单——pricing 应调用这个而不是 TaskBillingUnit。
func TaskBillingUnitForModel(model string, channelTypes []int) string {
	if IsPerCallVideoSKU(model) {
		return BillingUnitCall
	}
	return TaskBillingUnit(channelTypes)
}

// TaskBillingUnit 汇总一个模型所有启用渠道的计费单位。
func TaskBillingUnit(channelTypes []int) string {
	second, call := false, false
	for _, t := range channelTypes {
		if IsPerSecondTaskChannel(t) {
			second = true
		} else {
			call = true
		}
	}
	switch {
	case second && call:
		return BillingUnitMixed
	case second:
		return BillingUnitSecond
	default:
		return BillingUnitCall
	}
}

var SunoModel2Action = map[string]string{
	"suno_music":  SunoActionMusic,
	"suno_lyrics": SunoActionLyrics,
}
