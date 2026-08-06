package meaicc

// ModelList 是 meaicc 侧的上游模型名。渠道通过 model_mapping 把对外名
// （如 seedance-2.0-mini）映射到这里的某一个。
//
// 各档能力与实测状态（260805 全量实测，详见 memory/video-upstream-todo）：
//
//	sd-2-fast  ¥1.0  9图/无真人           时长上限仅 10s（静默截断，勿用于 >10s）
//	sd-2-c1    ¥1.5  9图/后台过真人
//	sd-2-c6    ¥1.9  4图3视频1音频/原生真人  ★锁脸已验证，1254x720
//	sd-2-c7    ¥2.0  9图3视频0音频/原生真人
//	sd-2-c3    ¥2.0  9图3视频3音频/原生真人
//	sd-2-c2    ¥2.5  9图3视频3音频/后台过真人 ✗ 传真人参考图会被 451 拒绝
//	sd-2-c4    ¥3.0  4图3视频1音频/原生真人  ★锁脸已验证，1280x720
//	sd-2-c5    ¥4.0  9图3视频3音频/原生真人  ★锁脸已验证，1254x720（933 档）
//	sd-2-c8    ¥4.5  9图3视频3音频/原生真人（标"满血"，画质优势未验证）
var ModelList = []string{
	"sd-2-fast",
	"sd-2-c1",
	"sd-2-c2",
	"sd-2-c3",
	"sd-2-c4",
	"sd-2-c5",
	"sd-2-c6",
	"sd-2-c7",
	"sd-2-c8",
}

var ChannelName = "meaicc-video"

// 上游硬约束（实测）
const (
	minDuration = 4
	maxDuration = 15
)

// validRatios 是上游接受的宽高比。上游对 ratio 是**先扣费后异步校验**
// （除 sd-2-c6 外），非法值会白跑一轮并占用几十秒，故必须在网关侧前置拦截。
var validRatios = map[string]bool{
	"1:1":  true,
	"16:9": true,
	"9:16": true,
	"4:3":  true,
	"3:4":  true,
}

// mediaTypeImage 等为上游 media[].type 的合法取值。
const (
	mediaTypeImage = "reference_image"
	mediaTypeVideo = "reference_video"
	mediaTypeVoice = "reference_voice"
	mediaTypeFirst = "first_frame"
	mediaTypeLast  = "last_frame"
)
