package maitoken

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"net/http/httptest"
	"strings"
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

// TestConvertToOpenAIVideoScrubsUpstream 用 260810 线上抓到的**真实** Hailuo-H3
// 返回体做回归：里面有 `result_url` 这个当初黑名单没覆盖的字段，把成片直链
// （cdn.guoguomg.com）连同上游任务 ID 一起漏给了客户。
//
// 断言方式是**整串扫描**而不是逐字段检查 —— 上游随时可能再加字段，
// 只有"整个返回体里不许出现上游域名/上游 ID"才拦得住下一次。
func TestConvertToOpenAIVideoScrubsUpstream(t *testing.T) {
	a := &TaskAdaptor{}
	const raw = `{
	  "completed_at":1786335549,"created_at":1786335323,
	  "id":"task_qeYpV4bPTGVNniG6WyPISW6gOtCeorVN",
	  "metadata":{"url":"https://cdn.guoguomg.com/video-jobs/2026/08/10/task_qeYpV4bPTGVNniG6WyPISW6gOtCeorVN/video.mp4"},
	  "model":"Hailuo-H3","object":"video","progress":100,"quality":"standard",
	  "result_url":"https://cdn.guoguomg.com/video-jobs/2026/08/10/task_qeYpV4bPTGVNniG6WyPISW6gOtCeorVN/video.mp4",
	  "status":"completed","task_id":"task_C6RY20j4bcrGWeMcY1sr23zJR2R0Zxdh",
	  "url":"https://cdn.guoguomg.com/video-jobs/2026/08/10/task_qeYpV4bPTGVNniG6WyPISW6gOtCeorVN/video.mp4",
	  "video_url":"https://cdn.guoguomg.com/video-jobs/2026/08/10/task_qeYpV4bPTGVNniG6WyPISW6gOtCeorVN/video.mp4"}`

	task := &model.Task{TaskID: "task_public_1", Status: model.TaskStatusSuccess, Data: []byte(raw)}
	task.Properties.OriginModelName = "minimax-h3-2k"

	out, err := a.ConvertToOpenAIVideo(task)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	s := string(out)
	for _, leak := range []string{
		"guoguomg", "mai-token", // 上游域名
		"task_qeYpV4bPTGVNniG6WyPISW6gOtCeorVN", // 上游任务 ID（id / URL 路径里）
		"task_C6RY20j4bcrGWeMcY1sr23zJR2R0Zxdh", // 上游第二个任务 ID
		"Hailuo-H3",                             // 上游模型名
	} {
		if contains(s, leak) {
			t.Errorf("对外返回体泄漏上游信息 %q:\n%s", leak, s)
		}
	}

	if got := gjson.GetBytes(out, "id").String(); got != "task_public_1" {
		t.Errorf("id = %q, want 本站任务 ID", got)
	}
	if got := gjson.GetBytes(out, "task_id").String(); got != "task_public_1" {
		t.Errorf("task_id = %q, want 本站任务 ID", got)
	}
	if got := gjson.GetBytes(out, "model").String(); got != "minimax-h3-2k" {
		t.Errorf("model = %q, want 对外模型名", got)
	}
	if got := gjson.GetBytes(out, "object").String(); got != "video" {
		t.Errorf("object = %q, want 类型标记 video（不能被 URL 覆盖）", got)
	}
	// 三种成片字段都要在，且都指向代理
	for _, k := range []string{"video_url", "url", "metadata.url"} {
		if got := gjson.GetBytes(out, k).String(); !contains(got, "task_public_1") {
			t.Errorf("%s = %q, want 代理地址", k, got)
		}
	}
	// 上游私有字段不该出现在对外契约里
	if gjson.GetBytes(out, "quality").Exists() || gjson.GetBytes(out, "result_url").Exists() {
		t.Errorf("白名单外的上游字段被带出: %s", s)
	}
	// 进度/时间戳这类契约内字段要保留
	if gjson.GetBytes(out, "progress").Int() != 100 {
		t.Error("progress 丢失")
	}
	if gjson.GetBytes(out, "created_at").Int() != 1786335323 {
		t.Error("created_at 丢失")
	}
}

// TestConvertToOpenAIVideoFailure 失败任务不能带出成片字段，且理由要是翻译后的。
func TestConvertToOpenAIVideoFailure(t *testing.T) {
	a := &TaskAdaptor{}
	task := &model.Task{
		TaskID:     "task_public_2",
		Status:     model.TaskStatusFailure,
		FailReason: "参考素材无法下载；费用已退还",
		Data:       []byte(`{"id":"up_2","object":"video","status":"failed","progress":100}`),
	}
	task.Properties.OriginModelName = "minimax-h3-2k"

	out, err := a.ConvertToOpenAIVideo(task)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gjson.GetBytes(out, "video_url").Exists() {
		t.Error("失败任务不应带 video_url")
	}
	if got := gjson.GetBytes(out, "error.message").String(); got != task.FailReason {
		t.Errorf("error.message = %q, want %q", got, task.FailReason)
	}
	if got := gjson.GetBytes(out, "status").String(); !contains(got, "FAILED") {
		t.Errorf("status = %q", got)
	}
}

// TestFixedDurationOmitsSeconds 守住 260810 实测的坑：
// `Hailuo-H3` 传 seconds=3/5/6/10 与 duration=6 一律被上游拒
// （"seconds 参数取值不受支持"），**完全不传才成功**，输出固定约 5 秒 2560x1440。
// 请求里只要出现 seconds 字段就 100% 失败，所以必须整个字段不序列化。
func TestFixedDurationOmitsSeconds(t *testing.T) {
	if !IsFixedDuration("Hailuo-H3") {
		t.Fatal("Hailuo-H3 应被识别为固定时长模型")
	}
	if IsFixedDuration("sd-2.0-720p") {
		t.Error("按秒模型不应被识别为固定时长")
	}

	// 固定时长模型：seconds 必须整个字段消失，不能是 "seconds":""
	body := upstreamRequest{Model: "Hailuo-H3", Prompt: "x", Seconds: ""}
	b, err := common.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(b, "seconds").Exists() {
		t.Errorf("固定时长模型的请求体不应出现 seconds 字段: %s", string(b))
	}

	// 普通模型：seconds 必须保留
	body2 := upstreamRequest{Model: "sd-2.0-720p", Prompt: "x", Seconds: "5"}
	b2, _ := common.Marshal(body2)
	if gjson.GetBytes(b2, "seconds").String() != "5" {
		t.Errorf("按秒模型必须携带 seconds: %s", string(b2))
	}
}

// TestActionOf 消费日志的「操作」字段。不设它 service/task_billing.go
// 会写成 "操作 ，按次计费"，后台看不出这单有没有带素材。
func TestActionOf(t *testing.T) {
	cases := []struct {
		name string
		m    materials
		want string
	}{
		{"纯文生", materials{}, constant.TaskActionTextGenerate},
		{"参考图", materials{Images: []string{"a"}}, constant.TaskActionReferenceGenerate},
		{"首尾帧", materials{Images: []string{"a", "b"}, FirstLast: true}, constant.TaskActionFirstTailGenerate},
		{"参考视频", materials{Images: []string{"a"}, Videos: []string{"v"}}, constant.TaskActionRemix},
	}
	for _, tc := range cases {
		if got := actionOf(tc.m); got != tc.want {
			t.Errorf("%s: actionOf = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestValidateKeepsPipelineAction 守住 remix：ResolveOriginTask 在**进重试循环
// 之前**就把 /v1/videos/:id/remix 标成 TaskActionRemix（relay_task.go:42），
// 而 Validate 在那之后才跑。无条件赋值会把它覆盖掉。
func TestValidateKeepsPipelineAction(t *testing.T) {
	a := &TaskAdaptor{}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/videos",
		strings.NewReader(`{"model":"sd-mini-720p","prompt":"x","seconds":"5"}`))
	c.Request.Header.Set("Content-Type", "application/json")

	// Action 在内嵌的 *TaskRelayInfo 上（relay_info.go:180），必须显式初始化
	info := &relaycommon.RelayInfo{OriginModelName: "sd-mini-720p"}
	info.TaskRelayInfo = &relaycommon.TaskRelayInfo{Action: constant.TaskActionRemix}
	if terr := a.ValidateRequestAndSetAction(c, info); terr != nil {
		t.Fatalf("unexpected validate error: %+v", terr)
	}
	if info.Action != constant.TaskActionRemix {
		t.Errorf("Action = %q，管线定过的值被覆盖了", info.Action)
	}
}
