package constant

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
