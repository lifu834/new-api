package dimensio

// ModelList 是 dimensio（jimeng.dimensio.cn）侧的上游模型名。
//
// 🔑 只暴露 **provider 直连** 模型，**不要**暴露裸名路由器
// （seedance-2.0 / -fast / -mini / 2.5）。裸名是"自动路由"，会落到某个
// 只有 720p 上限的 provider，把用户请求的 1080p **静默降级**成 720p——
// 260803 那次"1080p 拿不到、输出恒 960×960"的误判就是这么来的。
// 直连前缀虽仍可能被重路由，但只会路由到**能满足所请求分辨率**的家。
//
// 三字母前缀 = 上游来源代号：dvc / hgf / jmg / pxv / rlm。
//
// 价格（积分/秒，¥1 = 100 积分，260807 实测口径）：
//
//	                             480p  720p  1080p  4k/2160p   备注
//	rlm-seedance-2.0-mini         14    45      -       -      ratio 仅 16:9/9:16
//	hgf-seedance-2.0-mini         15    33      -       -      ★720p 最便宜
//	pxv-seedance-2.0-mini         19    36      -       -
//	dvc-seedance-2.0-fast         22    42      -       -      ✗ 无 omni_reference
//	hgf-seedance-2.0-fast         24    43      -       -
//	pxv-seedance-2.0-fast         25    50      -       -
//	rlm-seedance-2.0-fast         28    55     66       -      ★1080p 最便宜
//	hgf-seedance-2.0              36    62    120     260(4k)
//	rlm-seedance-2.0              38    69     95       -
//	pxv-seedance-2.0-standard     44    63    130     300(2160p)
//	dvc-seedance-2.0              32    52    120     220(4k)  ★4k 最便宜；✗ 无 omni
//	jmg-video-seedance-2.0-mini    -    44      -       -
//	jmg-video-seedance-2.0-fast-vip -   54      -       -
//	jmg-video-seedance-2.0-vip     -    68    160       -
//	hgf-seedance-2.5              75   148      -       -
//	jmg-video-seedance-2.5        80   148      -       -
var ModelList = []string{
	"dvc-seedance-2.0",
	"dvc-seedance-2.0-fast",
	"hgf-seedance-2.0",
	"hgf-seedance-2.0-fast",
	"hgf-seedance-2.0-mini",
	"hgf-seedance-2.5",
	"jmg-video-seedance-2.0-fast-vip",
	"jmg-video-seedance-2.0-mini",
	"jmg-video-seedance-2.0-vip",
	"jmg-video-seedance-2.5",
	"pxv-seedance-2.0-fast",
	"pxv-seedance-2.0-mini",
	"pxv-seedance-2.0-standard",
	// rlm 系 260816 已从上游 /v1/models 消失（下架），条目移除；
	// akl 系为同期新增（见 modelCapTable）。
}

var ChannelName = "dimensio-video"

// 上游硬约束
const (
	minDuration = 4
	maxDuration = 15
)

// functionMode 取值
const (
	modeOmni       = "omni_reference"    // 多素材参考（@image_file_N 引用），默认
	modeFirstLast  = "first_last_frames" // 0 图=文生 / 1 图=图生 / 2 图=首尾帧
	defaultRatio   = "16:9"
	defaultResName = "720p"
)

// modelCaps 是每个上游模型的硬能力。上游对越界参数的处理**不一致**——
// 有的直接拒（-2000），有的静默降级。静默降级是最难排查的那类问题
// （用户付了 1080p 的钱拿到 720p 还不知道），所以一律在网关侧前置拦截。
type modelCaps struct {
	Resolutions map[string]bool
	Ratios      map[string]bool
	// OmniRef 为 false 表示该模型只有 first_last_frames：
	// 做不了多图 @引用/锁脸，也不收参考视频与音频（dvc 系列）。
	OmniRef bool
}

func caps(res, ratios []string, omni bool) modelCaps {
	m := modelCaps{Resolutions: map[string]bool{}, Ratios: map[string]bool{}, OmniRef: omni}
	for _, r := range res {
		m.Resolutions[r] = true
	}
	for _, r := range ratios {
		m.Ratios[r] = true
	}
	return m
}

var (
	ratiosFull = []string{"1:1", "21:9", "16:9", "9:16", "3:4", "4:3"}
	ratiosAuto = []string{"auto", "16:9", "9:16", "4:3", "3:4", "1:1", "21:9"}
	// akl 家族用 adaptive 代替 auto（260816 实测：adaptive 可用）
	ratiosAdaptive = []string{"adaptive", "1:1", "4:3", "3:4", "16:9", "9:16", "21:9"}
	// rlm 只收这两种，其余会被拒或重路由
	ratiosRLM = []string{"16:9", "9:16"}
)

var modelCapTable = map[string]modelCaps{
	// dvc：OmniRef 原记 false（"只有 first_last"）——❌ 260816 实测推翻：
	// omni_reference + 真人参考图正常出片且**锁脸成功**（用户肉眼比对确认，
	// banana 合成脸 → dvc 480p，¥0.84）。它是 dimensio 最便宜的锁脸线。
	"dvc-seedance-2.0":                caps([]string{"480p", "720p", "1080p", "4k"}, ratiosFull, true),
	"dvc-seedance-2.0-fast":           caps([]string{"480p", "720p"}, ratiosFull, true),
	// akl（260816 补录，/v1/models + 实测）：比例枚举是 adaptive 不是 auto；
	// ⚠️ 纯文生**不能**用 first_last_frames 模式（akl 会静默失败连 fail_reason
	// 都不给，dvc 的"first_last+0图=文生"惯例不通用），默认 omni 即可。
	// 锁脸未实测，暂按支持处理（同价位 hgf 已验，akl 是候选降本线）。
	"akl-seedance-2.0":                caps([]string{"480p", "720p", "1080p", "4k"}, ratiosAdaptive, true),
	"akl-seedance-2.0-fast":           caps([]string{"480p", "720p"}, ratiosAdaptive, true),
	"akl-seedance-2.0-mini":           caps([]string{"480p", "720p"}, ratiosAdaptive, true),
	"hgf-seedance-2.0":                caps([]string{"480p", "720p", "1080p", "4k"}, ratiosAuto, true),
	"hgf-seedance-2.0-fast":           caps([]string{"480p", "720p"}, ratiosAuto, true),
	"hgf-seedance-2.0-mini":           caps([]string{"480p", "720p"}, ratiosAuto, true),
	"hgf-seedance-2.5":                caps([]string{"480p", "720p"}, ratiosAuto, true),
	"jmg-video-seedance-2.0-fast-vip": caps([]string{"720p"}, ratiosFull, true),
	"jmg-video-seedance-2.0-mini":     caps([]string{"720p"}, ratiosFull, true),
	"jmg-video-seedance-2.0-vip":      caps([]string{"720p", "1080p"}, ratiosFull, true),
	"jmg-video-seedance-2.5":          caps([]string{"480p", "720p"}, ratiosFull, true),
	"pxv-seedance-2.0-fast":           caps([]string{"480p", "720p"}, ratiosFull, true),
	"pxv-seedance-2.0-mini":           caps([]string{"480p", "720p"}, ratiosFull, true),
	"pxv-seedance-2.0-standard":       caps([]string{"480p", "720p", "1080p", "2160p"}, ratiosFull, true),
	// rlm-* 已下架（260816 上游 /v1/models 不再返回），条目移除。
}

// capsFor 取模型能力；未知模型返回 (zero, false)，调用方一律放行
// （新模型上线时不至于被网关误拦，代价是失去前置校验）。
func capsFor(model string) (modelCaps, bool) {
	c, ok := modelCapTable[model]
	return c, ok
}
