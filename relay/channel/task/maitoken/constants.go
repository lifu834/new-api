package maitoken

// ModelList 是 mai-token（api.mai-token.com）的公开模型名。
//
// 🔑 **输出分辨率由模型名决定**，不由 resolution 参数决定（官方文档明示：
// "推荐不要额外传 resolution，避免模型档位与参数冲突"）。这与我们海外组
// 按档位定价的方式一致 —— 见 resolutionFromModelName。
//
// 我方成本（¥/秒，260808 从 /api/pricing 取）：
//
//	sd-mini-480p  0.25   sd-mini-720p  0.40
//	sd-fast-480p  0.30   sd-fast-720p  0.53
//	sd-2.0-480p   0.42   sd-2.0-720p   0.73   sd-2.0-1080p 1.50   sd-2.0-4k 2.90
var ModelList = []string{
	"sd-2.0-1080p",
	"sd-2.0-720p",
	"sd-2.0-480p",
	"sd-2.0-4k",
	"sd-fast-720p",
	"sd-fast-480p",
	"sd-mini-720p",
	"sd-mini-480p",
}

var ChannelName = "mai-token-video"

// 上游硬约束（官方文档 §5.4 / §18）
const (
	minDuration = 4
	maxDuration = 15
	maxImages   = 9 // first_frame / last_frame 也计入这 9 张
	maxAudios   = 3
	maxVideos   = 3
)

// validRatios 见文档 §5.1。
var validRatios = map[string]bool{
	"16:9": true,
	"9:16": true,
	"1:1":  true,
	"4:3":  true,
	"3:4":  true,
	"21:9": true,
}

// content[] 元素的 role（文档 §5.2）
const (
	roleRefImage = "reference_image"
	roleFirst    = "first_frame"
	roleLast     = "last_frame"
	roleRefAudio = "reference_audio"
	roleRefVideo = "reference_video"
)
