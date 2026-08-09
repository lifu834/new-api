package sudashui

// ModelList 是 sudashuiapi（api.sudashuiapi.com）的模型名。
//
// 🔑 **模型名以「模型广场」/ GET /v1/models 为准**——该站上下架非常频繁
// （59 条公告里"维护""额度已用尽临时下架"反复出现），本列表只是给渠道
// 编辑器做候选，实际可用性以渠道体检为准。
//
// 前缀是**上游渠道代号**（gf/gf2/gf3/hn/wf/xh/hj/pd/ll/lm/sl/pg/my/bf/cvk…），
// 同一能力在不同代号下价格与稳定性差别很大。260808 实测：
//
//	· sdas-gf3-*  官方标准（非超分），**支持真人锁脸**，¥0.414/秒@720p ← 主力
//	· sdas-wf-*   最便宜（¥0.125/秒）但**明确卡真人**，只能做非人物素材
//	· sdas-hn-*   全线 400 Bad Request（同一 payload 打 wf 成功）——该代号当时是坏的
//
// 价格（¥/秒；来自站点 /api/pricing 的 model_ratio，其 FAQ 明示"每秒价格=输入价格"）：
//
//	wf-fast-933-480p 0.075   wf-fast-933-720p 0.125   wf-pro-933-720p 0.20
//	ll-sd2.5-pro-720p 0.225  gf-fast-720p 0.225       bf-2.0-fast 0.21
//	gf3-fast-720p 0.2645     gf-720p 0.265            gf-1080p 0.275
//	gf-2k 0.325              gf-4k 0.35               wf-pro-933-1080p 0.35
//	gf3-720p 0.414           gf3-1080p 0.4761         sl-sd2.5-pro-720p 0.75
var ModelList = []string{
	// 官方标准（非超分）——可锁脸，接入主力
	"sdas-gf3-seedance-2.0-720p",
	"sdas-gf3-seedance-2.0-fast-720p",
	"sdas-gf3-seedance-2.0-1080p",
	// 官方特价（超分线）
	"sdas-gf-seedance-2.0-fast-720p",
	"sdas-gf-seedance-2.0-720p",
	"sdas-gf-seedance-2.0-1080p",
	"sdas-gf-seedance-2.0-2k",
	"sdas-gf-seedance-2.0-4k",
	// Seedance 2.5（4–30 秒 / 30 图 / 10 音）
	"sdas-ll-sd2.5-pro-720p",
	"sdas-sl-sd2.5-pro-480p",
	"sdas-sl-sd2.5-pro-720p",
	"sdas-lm-sd2.5-pro-480p",
	"sdas-lm-sd2.5-pro-720p",
	// 933 按秒（⚠ wf 系列卡真人）
	"sdas-wf-sd2.0-fast-933-720p",
	"sdas-wf-sd2.0-pro-933-720p",
	"sdas-wf-sd2.0-pro-933-1080p",
	// 933 按条
	"sdas-xh-sd2.0-933-3-pro-720p",
	"sdas-pd-sd2.0-pro-933-5-720p",
	"ld-sdas-cvk-pro-933-720p",
}

var ChannelName = "sudashui-video"

// 上游硬约束
const (
	minDuration = 4
	maxDuration = 30 // Seedance 2.5 支持到 30 秒；2.0 线实际上限 15，由上游校验
)

// modeReferences / modeFrames 是 payload.mode 的取值。
const (
	modeReferences = "references" // 多素材参考，@image1/@video1/@audio1 引用
	modeFrames     = "frames"     // 首尾帧：firstFrameUrl 下标 0、lastFrameUrl 下标 1
)

// validRatios 见官方文档。
var validRatios = map[string]bool{
	"16:9": true,
	"9:16": true,
	"1:1":  true,
	"4:3":  true,
	"3:4":  true,
	"21:9": true,
}
