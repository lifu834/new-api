package dimensio

import (
	"fmt"
	"strings"
)

// friendlyReason 把 dimensio 的错误翻译成用户能据此行动的说明。
//
// 上游错误统一是 {code, message, data}，code 为负数（官方文档 260807 抄录）：
//
//	-2000 请求参数非法          缺必填/类型错/值越界/不支持该参数
//	-2001 请求失败              上游暂不可用、超时、内部错误，可重试
//	-2003 远程文件URL非法        素材 URL 无法访问或格式不对
//	-2004 远程文件超出大小
//	-2006 内容合规被阻止         提示词或素材触发审核
//	-2007 图像生成失败
//	-2008 视频生成失败           上游失败或轮询超时
//	-2009 渠道积分不足           上游某渠道账号没钱（与用户无关）
//	-2010 未授权                API Key 缺失/无效/过期
//	-2011 资源不存在            taskId 不存在/过期/不属于本账号
//	-2012 平台积分预算已用完      我方账户余额不足
//	-2013 并发已满              稍后重试
//
// 生成阶段失败由上游退款，文案统一告知"费用已退还"以减少客诉。
func friendlyReason(code int, raw string) string {
	s := strings.ToLower(raw)

	switch code {
	case -2006:
		return "内容未通过上游审核，费用已退还。请调整提示词或参考素材后重试（同样内容偶发可通过，也可直接重试一次）"
	case -2003:
		return "参考素材无法下载，费用已退还。请确认素材是可公网访问的 https 地址且未过期（临时图床链接常在 1 小时内失效）"
	case -2004:
		return "参考素材超出上游大小限制，费用已退还。请压缩后重试"
	case -2009:
		return "上游账号额度不足，费用已退还。请稍后重试或改用其他档位"
	case -2012:
		return "上游平台额度不足，费用已退还。请联系我们充值"
	case -2013:
		return "上游并发已满，费用已退还。请稍后重试"
	case -2001, -2008:
		// -2008 也可能裹着更具体的原因，优先看文案
		if r := byMessage(s, raw); r != "" {
			return r
		}
		return "上游生成失败，费用已退还。请稍后重试"
	case -2011:
		return "任务不存在或已过期"
	case -2010:
		return "上游鉴权失败，请联系我们处理"
	}

	if r := byMessage(s, raw); r != "" {
		return r
	}

	cleaned := strings.TrimSpace(raw)
	if cleaned == "" {
		if code != 0 {
			return fmt.Sprintf("任务失败（上游 code %d），费用已退还", code)
		}
		return "任务失败，费用已退还"
	}
	return cleaned + "（费用已退还）"
}

// byMessage 在没有可靠 code 时按文案兜底识别。
//
// 异步失败（status=failed）**不带 code**，只有 error 字符串，所以这里是主力路径，
// 不是兜底。以下文案为 260807 实测采集。
func byMessage(lower, raw string) string {
	switch {
	// 素材取不到/格式不对——实测最常见的用户侧问题（图床过期、返回的是 HTML 错误页）
	case strings.Contains(lower, "unsupported image format"),
		strings.Contains(lower, "unsupported file"),
		strings.Contains(lower, "invalid image"):
		return "参考素材无法读取，费用已退还。请确认链接直接指向图片本身（而非网页），且可公网访问、未过期（临时图床常在 1 小时内失效）"
	case strings.Contains(lower, "download"), strings.Contains(lower, "fetch failed"),
		strings.Contains(lower, "not found"):
		return "参考素材无法下载，费用已退还。请确认素材是可公网访问的 https 地址且未过期"
	// 上游"没有渠道支持当前请求参数"=该模型不支持所请求的分辨率/比例/时长组合。
	// 正常情况下前置校验已拦掉，走到这里说明能力表与上游实际不一致，需要复核。
	case strings.Contains(raw, "没有渠道支持当前请求参数"),
		strings.Contains(lower, "no channel supports"):
		return "该档位不支持所请求的分辨率/宽高比/时长组合，费用已退还。请调整参数或改用更高档位"
	case strings.Contains(lower, "privacy"), strings.Contains(lower, "real person"),
		strings.Contains(raw, "真人"):
		return "该模型不接受含真人的参考图，费用已退还。请改用支持真人的档位"
	case strings.Contains(lower, "moderation"), strings.Contains(raw, "合规"),
		strings.Contains(raw, "审核"):
		return "内容未通过上游审核，费用已退还。请调整提示词或参考素材后重试"
	case strings.Contains(raw, "首尾帧"):
		return "首尾帧模式需要正好 2 张图片：请只传首帧与尾帧，或改用多素材参考模式"
	case strings.Contains(lower, "http"), strings.Contains(raw, "素材必须"):
		return "参考素材必须是可公网访问的 https 地址，费用已退还"
	case strings.Contains(lower, "insufficient"), strings.Contains(raw, "积分不足"):
		return "上游额度不足，费用已退还。请稍后重试或改用其他档位"
	}
	return ""
}
