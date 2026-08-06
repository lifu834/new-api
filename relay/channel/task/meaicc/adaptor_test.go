package meaicc

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/model"
)

// TestRewriteReferences 锁住素材引用语法的翻译。对外统一 @image1，各上游语法不同
// （现役 #174 认 @image1，meaicc 认 图1）——翻译错会导致参考图被上游忽略，
// 表现为"传了图却没锁脸"这种极难排查的问题。
func TestRewriteReferences(t *testing.T) {
	cases := map[string]string{
		"@image1 中的男子微笑":           "图1 中的男子微笑",
		"@image1 和 @image2 合影":     "图1 和 图2 合影",
		"@video1 的动作配 @audio1 的声音": "视频1 的动作配 音频1 的声音",
		"@img3 站在左边":               "图3 站在左边",
		"@voice2 说话":               "音频2 说话",
		"没有引用的普通提示词":               "没有引用的普通提示词",
		"@image10 双位数也要对":          "图10 双位数也要对",
	}
	for in, want := range cases {
		if got := rewriteReferences(in); got != want {
			t.Errorf("rewriteReferences(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestIsAboveHD 锁住"高于 720p 就报错"的判定。该上游只出 720p 且会静默忽略
// resolution，若这里放行，用户要 1080p 会拿到 720p 而毫不知情。
func TestIsAboveHD(t *testing.T) {
	above := []string{"1080p", "4K", "4k", "2k", "1440p", "1920x1080", "3840x2160", "1280x1440"}
	for _, s := range above {
		if !isAboveHD(s) {
			t.Errorf("isAboveHD(%q) = false, want true", s)
		}
	}
	notAbove := []string{"", "720p", "1280x720", "864x496", "480p", "随便写的"}
	for _, s := range notAbove {
		if isAboveHD(s) {
			t.Errorf("isAboveHD(%q) = true, want false", s)
		}
	}
}

// TestParseTaskResult 锁住上游状态解析。meaicc 用大写前缀且把失败原因拼在
// status 串里，没有独立 error 对象——用小写比较或找 error 字段都会漏判。
func TestParseTaskResult(t *testing.T) {
	a := &TaskAdaptor{}

	t.Run("成功时把成片URL放进Url", func(t *testing.T) {
		info, err := a.ParseTaskResult([]byte(
			`{"id":"x","status":"SUCCEEDED","object":"https://cdn.example.com/a.mp4","seconds":5}`))
		if err != nil {
			t.Fatal(err)
		}
		if info.Status != model.TaskStatusSuccess {
			t.Errorf("status = %v, want success", info.Status)
		}
		if info.Url != "https://cdn.example.com/a.mp4" {
			t.Errorf("Url = %q, 成片地址没被带出来，代理会拿不到视频", info.Url)
		}
	})

	t.Run("失败原因拼在status里且要翻译成人话", func(t *testing.T) {
		info, err := a.ParseTaskResult([]byte(
			`{"id":"x","status":"FAILED: capflow ret=-6 msg=shark block only"}`))
		if err != nil {
			t.Fatal(err)
		}
		if info.Status != model.TaskStatusFailure {
			t.Errorf("status = %v, want failure", info.Status)
		}
		if !strings.Contains(info.Reason, "审核") {
			t.Errorf("Reason = %q, 原始风控串没被翻译，用户看不懂", info.Reason)
		}
		if !strings.Contains(info.Reason, "已退还") {
			t.Errorf("Reason = %q, 没告知已退款，会引发客诉", info.Reason)
		}
	})

	t.Run("进行中与排队", func(t *testing.T) {
		for body, want := range map[string]string{
			`{"status":"RUNNING"}`: model.TaskStatusInProgress,
			`{"status":"PENDING"}`: model.TaskStatusQueued,
		} {
			info, err := a.ParseTaskResult([]byte(body))
			if err != nil {
				t.Fatal(err)
			}
			if info.Status != want {
				t.Errorf("%s → %v, want %v", body, info.Status, want)
			}
		}
	})
}

// TestFriendlyReason 锁住关键错误的翻译，尤其是"参考素材无法下载"——
// 这是最高频的用户侧问题（临时图床链接 1 小时失效），文案必须给出可行动的指引。
func TestFriendlyReason(t *testing.T) {
	cases := []struct{ raw, mustContain string }{
		{"FAILED: capflow ret=-6 msg=shark block only", "审核"},
		{"FAILED: 内容未通过审核，请修改提示词或参考图后重试", "审核"},
		{"FAILED: 参考素材无法下载，请检查素材地址", "公网访问"},
		{"reference material download failed with HTTP 404 Not Found", "公网访问"},
		{"FAILED: 账号积分不足", "额度不足"},
		{"FAILED: 当前模型正在维护，请稍后再试。", "维护"},
		{"videos-4-mini does not support the requested duration", "4–15"},
		{"videos-4-mini does not support the requested ratio", "16:9"},
		{"451 reference_image_privacy_error contains a real person", "真人"},
	}
	for _, c := range cases {
		got := friendlyReason(c.raw)
		if !strings.Contains(got, c.mustContain) {
			t.Errorf("friendlyReason(%q) = %q, 缺少关键信息 %q", c.raw, got, c.mustContain)
		}
	}
}

// TestConvertToOpenAIVideoScrubsUpstream 锁住防泄露：上游成片直链
// （minioapi.meaicc.com）与上游模型名（sd-2-c6）都不能出现在给用户的响应里。
func TestConvertToOpenAIVideoScrubsUpstream(t *testing.T) {
	a := &TaskAdaptor{}
	task := &model.Task{
		TaskID: "task_public123",
		Data: []byte(`{"id":"vid_upstream","task_id":"vid_upstream","model":"sd-2-c6",` +
			`"object":"https://minioapi.meaicc.com/public-images/x.mp4",` +
			`"metadata":{"url":"https://minioapi.meaicc.com/public-images/x.mp4"}}`),
	}
	task.Properties.OriginModelName = "seedance-2.0-mini"

	out, err := a.ConvertToOpenAIVideo(task)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, leak := range []string{"minioapi.meaicc.com", "sd-2-c6", "vid_upstream"} {
		if strings.Contains(s, leak) {
			t.Errorf("响应泄露上游信息 %q: %s", leak, s)
		}
	}
	if !strings.Contains(s, "task_public123") || !strings.Contains(s, "seedance-2.0-mini") {
		t.Errorf("对外字段没写对: %s", s)
	}
}

// TestConvertToOpenAIVideoSurfacesFriendlyFailure 锁住失败文案的透出。
// 260806 生产验证时发现: 库里 fail_reason 已翻译好，但用户查询响应回显的是
// task.Data 里的上游原始串（"FAILED: MODERATION_ERROR"），等于翻译白做。
func TestConvertToOpenAIVideoSurfacesFriendlyFailure(t *testing.T) {
	a := &TaskAdaptor{}
	task := &model.Task{
		TaskID:     "task_pub1",
		Status:     model.TaskStatusFailure,
		FailReason: "内容未通过上游审核，费用已退还。请调整提示词或参考图后重试",
		Data:       []byte(`{"id":"vid_x","status":"FAILED: MODERATION_ERROR","model":"sd-2-c6"}`),
	}
	task.Properties.OriginModelName = "seedance-2.0-mini"

	out, err := a.ConvertToOpenAIVideo(task)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if strings.Contains(s, "MODERATION_ERROR") {
		t.Errorf("仍在回显上游原始错误串: %s", s)
	}
	if !strings.Contains(s, "内容未通过上游审核") {
		t.Errorf("翻译后的失败原因没透出: %s", s)
	}
	if !strings.Contains(s, "已退还") {
		t.Errorf("未告知已退款，会引发客诉: %s", s)
	}
}
