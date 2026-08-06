package secureskill

import "strings"

// ModelList 是 secure-skill 侧的上游模型名。每个分组（特价/海外/企业版）
// 都提供同样这三档，具体走哪个分组由渠道 key 决定（key 绑定分组）。
//
//	video-2.0-fast  蒸馏加速档
//	video-2.0-mini  小模型档（特价组该档 260805 实测"正在维护"）
//	video-2.0-pro   满血档 ★特价组实测：¥6.00/条 15秒 1280x720 锁脸通过
//
// 注意：企业版分组只接受 video-2.0-pro，传 fast/mini 会被拒。
var ModelList = []string{
	"video-2.0-fast",
	"video-2.0-mini",
	"video-2.0-pro",
}

var ChannelName = "secure-skill-video"

const (
	minDuration = 4
	maxDuration = 15
)

// friendlyReason 把上游错误翻译成用户可理解的说明。与 meaicc 一样，失败由上游
// 退款，文案里点明这一点。
func friendlyReason(msg, code string) string {
	s := strings.ToLower(msg + " " + code)
	switch {
	case strings.Contains(s, "insufficient balance"), strings.Contains(s, "billing_error"):
		return "上游账户余额不足，费用已退还。请稍后重试或改用其他档位"
	case strings.Contains(s, "moderation"), strings.Contains(s, "审核"):
		return "内容未通过上游审核，费用已退还。请调整提示词或参考图后重试"
	case strings.Contains(s, "must be http"), strings.Contains(s, "download"):
		return "参考素材无法下载，费用已退还。请确认素材是可公网访问的 http(s) 地址且未过期"
	case strings.Contains(s, "maintenance"), strings.Contains(s, "维护"):
		return "该模型正在维护，费用已退还。请稍后重试或改用其他档位"
	case strings.Contains(s, "model must be"):
		return "该分组不支持所选档位，费用已退还"
	}
	if strings.TrimSpace(msg) == "" {
		return "任务失败，费用已退还"
	}
	return msg + "（费用已退还）"
}
