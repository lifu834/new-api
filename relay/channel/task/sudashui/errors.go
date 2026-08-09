package sudashui

import "strings"

// friendlyReason 把 sudashuiapi 的失败原因翻译成用户能据此行动的说明。
//
// 260808 实测采集到的形态：
//
//	"检测到参考素材包含人脸，请更换不含人脸的图片后再试"  (err_code=invalid_asset)
//	  —— wf 系列等便宜档明确卡真人，价目描述里也写了"卡真人，需自行处理"
//	"Bad Request"（err_code 与 message 全是这一句）
//	  —— sdas-hn- 系列当时全线如此，同一 payload 打 wf 却成功 ⇒ 是该上游渠道坏了，
//	     不是我方参数问题。对用户要说"换档位"，而不是让他去改参数。
//
// 失败一律不扣费（实测失败任务 credits=0），文案注明以减少客诉。
func friendlyReason(code, msg string) string {
	s := strings.ToLower(msg)
	c := strings.ToLower(code)

	switch {
	case strings.Contains(msg, "包含人脸"), strings.Contains(c, "invalid_asset"),
		strings.Contains(s, "face"):
		return "该档位不接受含人脸的参考图，费用已退还。请改用支持真人的档位"

	case strings.Contains(msg, "审核"), strings.Contains(msg, "合规"),
		strings.Contains(s, "moderation"), strings.Contains(s, "blocked"):
		return "内容未通过上游审核，费用已退还。请调整提示词或参考素材后重试（同样内容偶发可通过，也可直接重试一次）"

	case strings.Contains(msg, "下载"), strings.Contains(s, "download"),
		strings.Contains(s, "fetch"):
		return "参考素材无法下载，费用已退还。请确认链接可公网直接访问且未过期（该站上传的素材有效期仅约 2 小时）"

	case strings.Contains(msg, "额度"), strings.Contains(msg, "余额"),
		strings.Contains(s, "insufficient"), strings.Contains(s, "quota"):
		return "上游额度不足，费用已退还。请稍后重试或改用其他档位"

	case strings.Contains(msg, "维护"), strings.Contains(s, "maintenance"):
		return "该档位正在维护，费用已退还。请改用其他档位"

	// 上游只回一句无信息量的 "Bad Request"：实测这是**该上游渠道自身故障**
	// （同一请求体打别的渠道代号成功），让用户去改参数是误导。
	case strings.TrimSpace(msg) == "Bad Request", c == "bad request":
		return "该档位当前不可用（上游返回无效请求），费用已退还。请改用其他档位或稍后重试"
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
