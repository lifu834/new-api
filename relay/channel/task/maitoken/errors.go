package maitoken

import "strings"

// friendlyReason 把 mai 的失败原因翻译成用户能据此行动的说明。
//
// 上游失败形如 {"code":"generation_failed","message":"参考image下载失败（直连，
// 已重试代理+直连各2次；URL 不可达/超时/防盗链，或 base64 解码失败）"} ——
// 文案本身还算具体，但夹带了上游的内部重试细节，且没告诉用户"钱退了没有"。
//
// 生成阶段失败由上游退款，文案统一注明"费用已退还"以减少客诉。
func friendlyReason(code, msg string) string {
	s := strings.ToLower(msg)

	switch {
	case strings.Contains(msg, "参考image下载失败"),
		strings.Contains(msg, "下载失败"),
		strings.Contains(s, "download"):
		return "参考素材无法下载，费用已退还。请确认链接可公网直接访问、直接返回文件本身（不是网页），且在任务完成前保持有效（临时图床常在 1 小时内失效）"

	case strings.Contains(s, "moderation"), strings.Contains(msg, "审核"),
		strings.Contains(msg, "合规"), strings.Contains(s, "blocked"):
		return "内容未通过上游审核，费用已退还。请调整提示词或参考素材后重试（同样内容偶发可通过，也可直接重试一次）"

	case strings.Contains(s, "real person"), strings.Contains(s, "privacy"):
		return "该档位不接受含真人的参考图，费用已退还。请改用支持真人的档位"

	case strings.Contains(s, "insufficient"), strings.Contains(msg, "额度"),
		strings.Contains(msg, "余额"):
		return "上游账号额度不足，费用已退还。请稍后重试或改用其他档位"

	case strings.Contains(s, "timeout"), strings.Contains(msg, "超时"):
		return "上游生成超时，费用已退还。请稍后重试"

	case strings.Contains(s, "resolution"), strings.Contains(msg, "分辨率"):
		return "该档位不支持所请求的分辨率，费用已退还。分辨率由模型名决定，请改用对应档位的模型"
	}

	cleaned := strings.TrimSpace(msg)
	if cleaned == "" {
		if code != "" {
			return "任务失败（" + code + "），费用已退还"
		}
		return "任务失败，费用已退还"
	}
	return cleaned + "（费用已退还）"
}
