package meaicc

import "strings"

// friendlyReason 把上游的原始错误串翻译成用户能看懂、且能据此行动的说明。
//
// 上游的失败信息形如 "FAILED: capflow ret=-6 msg=shark block only"，直接透传给
// 用户毫无意义。这些串是 260804-260805 实测收集的：
//
//	capflow ret=-6 shark block   字节风控拦截（同一提示词可能时灵时不灵）
//	内容未通过审核                 同上，中文形态
//	账号积分不足                   上游自己的账号池没钱了（与用户无关）
//	当前模型正在维护                上游模型下线维护
//	参考素材无法下载                参考图 URL 失效/不可公网访问（最常见的用户侧问题）
//	reference_image_privacy_error 该模型拒绝含真人的参考图
//	does not support the requested duration/ratio  参数超范围
//
// 失败一律由上游退款，所以文案里明确告知"费用已退还"，减少客诉。
func friendlyReason(raw string) string {
	s := strings.ToLower(raw)

	switch {
	case strings.Contains(s, "shark block"), strings.Contains(s, "capflow"),
		strings.Contains(raw, "内容未通过审核"), strings.Contains(s, "moderation"):
		return "内容未通过上游审核，费用已退还。请调整提示词或参考图后重试（同样内容偶发可通过，也可直接重试一次）"

	case strings.Contains(s, "reference_image_privacy_error"),
		strings.Contains(s, "contains a real person"):
		return "该模型不接受含真人的参考图，费用已退还。请改用支持真人的模型档位"

	case strings.Contains(raw, "参考素材无法下载"),
		strings.Contains(s, "reference material download failed"),
		strings.Contains(s, "must be http"):
		return "参考素材无法下载，费用已退还。请确认素材是可公网访问的 http(s) 地址且未过期（临时链接常在 1 小时内失效）"

	case strings.Contains(raw, "账号积分不足"), strings.Contains(s, "insufficient"):
		return "上游账号额度不足，费用已退还。请稍后重试或改用其他档位"

	case strings.Contains(raw, "正在维护"), strings.Contains(s, "maintenance"):
		return "该模型正在维护，费用已退还。请稍后重试或改用其他档位"

	case strings.Contains(s, "does not support the requested duration"):
		return "该模型不支持所请求的时长，费用已退还。有效范围为 4–15 秒"

	case strings.Contains(s, "does not support the requested ratio"):
		return "该模型不支持所请求的宽高比，费用已退还。可选 1:1 / 16:9 / 9:16 / 4:3 / 3:4"

	case strings.Contains(s, "does not support the requested resolution"):
		return "该模型不支持所请求的分辨率，费用已退还。该档位仅输出 720p"
	}

	// 未识别的错误：去掉 "FAILED:" 前缀后原样返回，至少不丢信息
	cleaned := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "FAILED:"))
	if cleaned == "" {
		return "任务失败，费用已退还"
	}
	return cleaned + "（费用已退还）"
}
