package maitoken

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// TestBuildContentShape 是本适配器存在的理由所在：把统一契约的扁平素材字段
// 转成 mai 的 content[] 多模态数组。转错或漏转都会被上游**静默忽略**
// （照常出片、照常收费，成片与素材无关）——260808 实测坐实过。
func TestBuildContentShape(t *testing.T) {
	m := materials{
		Images: []string{"https://a/1.jpg", "https://a/2.jpg"},
		Audios: []string{"https://a/x.mp3"},
		Videos: []string{"https://a/y.mp4"},
	}
	items := buildContent("让 @image1 动起来", m)

	// 第一条必须是 text，且与顶层 prompt 同内容（文档 §17.1）
	if items[0].Type != "text" || items[0].Text != "让 @image1 动起来" {
		t.Fatalf("first item = %+v, want text 元素", items[0])
	}
	// 图片在音频/视频之前，且 @imageN 编号即出现顺序
	if items[1].Type != "image_url" || items[1].Role != roleRefImage ||
		items[1].ImageURL == nil || items[1].ImageURL.URL != "https://a/1.jpg" {
		t.Errorf("items[1] = %+v, want 第一张参考图", items[1])
	}
	if items[2].ImageURL == nil || items[2].ImageURL.URL != "https://a/2.jpg" {
		t.Errorf("items[2] 应是第二张参考图（@image2）")
	}
	if items[3].Type != "audio_url" || items[3].Role != roleRefAudio || items[3].AudioURL == nil {
		t.Errorf("items[3] = %+v, want 音频元素", items[3])
	}
	if items[4].Type != "video_url" || items[4].Role != roleRefVideo || items[4].VideoURL == nil {
		t.Errorf("items[4] = %+v, want 视频元素", items[4])
	}
	// 三种媒体各用自己的字段名，不能串
	if items[3].ImageURL != nil || items[4].ImageURL != nil {
		t.Error("音频/视频元素不应带 image_url 字段")
	}
}

// TestBuildContentFirstLast 首尾帧要打上各自的 role，否则上游会当普通参考图。
func TestBuildContentFirstLast(t *testing.T) {
	m := materials{Images: []string{"https://a/first.jpg", "https://a/last.jpg"}, FirstLast: true}
	items := buildContent("从 @image1 过渡到 @image2", m)
	if items[1].Role != roleFirst {
		t.Errorf("首帧 role = %q, want %q", items[1].Role, roleFirst)
	}
	if items[2].Role != roleLast {
		t.Errorf("尾帧 role = %q, want %q", items[2].Role, roleLast)
	}
}

// TestResolutionFromModelName 档位由模型名决定（文档明示不要传 resolution）。
func TestResolutionFromModelName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"sd-2.0-1080p", "1080p"},
		{"sd-mini-720p", "720p"},
		{"sd-fast-480p", "480p"},
		{"sd-2.0-4k", "4k"},
		{"sd-2.0", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := resolutionFromModelName(tc.in); got != tc.want {
			t.Errorf("resolutionFromModelName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestEstimateBillingSeconds mai 按秒计费；换掉 sora 适配器后必须自己提供
// seconds 乘数，否则每单少收（dimensio 上已经踩过一次）。
func TestEstimateBillingSeconds(t *testing.T) {
	a := &TaskAdaptor{}
	cases := []struct {
		req  relaycommon.TaskSubmitReq
		want float64
	}{
		{relaycommon.TaskSubmitReq{Duration: 4}, 4},
		{relaycommon.TaskSubmitReq{Seconds: "15"}, 15},
		{relaycommon.TaskSubmitReq{}, 5},
	}
	for _, tc := range cases {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Set("task_request", tc.req)
		got := a.EstimateBilling(c, &relaycommon.RelayInfo{})
		if got == nil || got["seconds"] != tc.want {
			t.Errorf("%+v => %v, want seconds=%v", tc.req, got, tc.want)
		}
	}
}

func TestParseTaskResult(t *testing.T) {
	a := &TaskAdaptor{}

	done := `{"id":"task_x","status":"completed","progress":100,
	          "video_url":"https://cdn.mai-token.com/video/task_x.mp4"}`
	info, err := a.ParseTaskResult([]byte(done))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Status != model.TaskStatusSuccess {
		t.Errorf("status = %v, want success", info.Status)
	}
	if info.Url != "https://cdn.mai-token.com/video/task_x.mp4" {
		t.Errorf("url = %q，成片直链必须落到 Url", info.Url)
	}

	// mai 用 in_progress（不是 processing），拼错会让任务卡在未知状态
	prog := `{"id":"task_x","status":"in_progress","progress":56}`
	info, _ = a.ParseTaskResult([]byte(prog))
	if info.Status != model.TaskStatusInProgress || info.Progress != "56%" {
		t.Errorf("in_progress => %v %q", info.Status, info.Progress)
	}

	queued := `{"id":"task_x","status":"queued","progress":0}`
	info, _ = a.ParseTaskResult([]byte(queued))
	if info.Status != model.TaskStatusQueued {
		t.Errorf("queued => %v, want queued", info.Status)
	}

	failed := `{"id":"task_x","status":"failed","progress":100,
	            "error":{"code":"generation_failed","message":"参考image下载失败（直连，已重试代理+直连各2次）"}}`
	info, _ = a.ParseTaskResult([]byte(failed))
	if info.Status != model.TaskStatusFailure {
		t.Fatalf("failed => %v, want failure", info.Status)
	}
	if !contains(info.Reason, "参考素材无法下载") || !contains(info.Reason, "费用已退还") {
		t.Errorf("Reason = %q，应翻译成可行动说明并注明退款", info.Reason)
	}
}

func TestFriendlyReason(t *testing.T) {
	cases := []struct{ code, msg, want string }{
		{"generation_failed", "参考image下载失败（直连…）", "参考素材无法下载"},
		{"", "content blocked by moderation", "内容未通过上游审核"},
		{"", "reference image contains a real person", "不接受含真人的参考图"},
		{"", "insufficient balance", "上游账号额度不足"},
		{"generation_failed", "", "任务失败"},
	}
	for _, tc := range cases {
		got := friendlyReason(tc.code, tc.msg)
		if !contains(got, tc.want) {
			t.Errorf("friendlyReason(%q,%q) = %q, want contains %q", tc.code, tc.msg, got, tc.want)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestConvertToOpenAIVideoScrubsUpstream 成片直链是 cdn.mai-token.com，
// 直接返给客户等于把上游身份和绕过我们的路径一并奉送。
func TestConvertToOpenAIVideoScrubsUpstream(t *testing.T) {
	a := &TaskAdaptor{}
	task := &model.Task{
		TaskID: "task_public_1",
		Data: []byte(`{"id":"up_1","object":"video","model":"sd-mini-720p","status":"completed",
		  "video_url":"https://cdn.mai-token.com/video/up_1.mp4"}`),
	}
	task.Properties.OriginModelName = "sd-mini-720p"

	out, err := a.ConvertToOpenAIVideo(task)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := gjson.GetBytes(out, "id").String(); got != "task_public_1" {
		t.Errorf("id = %q, want public id", got)
	}
	if got := gjson.GetBytes(out, "video_url").String(); contains(got, "mai-token.com") {
		t.Errorf("成片直链未被代理化: %q", got)
	}
}
