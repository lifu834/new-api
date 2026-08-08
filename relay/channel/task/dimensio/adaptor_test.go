package dimensio

import (
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/tidwall/gjson"
)

func TestRewriteReferences(t *testing.T) {
	cases := []struct{ in, want string }{
		// 对外短形式 → 上游 _file_ 形式
		{"@image1 中的男子微笑", "@image_file_1 中的男子微笑"},
		{"用 @img2 的构图", "用 @image_file_2 的构图"},
		{"参考 @video1 的运镜和 @audio1 的节奏", "参考 @video_file_1 的运镜和 @audio_file_1 的节奏"},
		{"@voice3 的音色", "@audio_file_3 的音色"},
		// 已是上游形式的必须幂等，不能被二次改写成 @image_file_file_1
		{"@image_file_1 保持不变", "@image_file_1 保持不变"},
		{"@video_file_2", "@video_file_2"},
		// meaicc 的中文写法也兼容，方便用户跨渠道迁移
		{"图1 里的人物转身", "@image_file_1 里的人物转身"},
		{"@图2 的服装", "@image_file_2 的服装"},
		{"视频1 的镜头语言", "@video_file_1 的镜头语言"},
		// 无引用时原样返回
		{"一只橘猫在窗台伸懒腰", "一只橘猫在窗台伸懒腰"},
	}
	for _, tc := range cases {
		if got := rewriteReferences(tc.in); got != tc.want {
			t.Errorf("rewriteReferences(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeResolution(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "720p"},
		{"720P", "720p"},
		{"hd", "720p"},
		{"1080", "1080p"},
		{"4K", "4k"},
		{"2160p", "4k"},
		{"3840x2160", "4k"},
		{"1920x1080", "1080p"},
		{"1280x720", "720p"},
		{"640x480", "480p"},
	}
	for _, tc := range cases {
		if got := normalizeResolution(tc.in); got != tc.want {
			t.Errorf("normalizeResolution(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestFitResolution 覆盖 4k / 2160p 的叫法差异：dvc/hgf 认 "4k"，
// pxv-standard 只认 "2160p"，翻译错会被上游拒。
func TestFitResolution(t *testing.T) {
	dvc := modelCapTable["dvc-seedance-2.0"]
	if got, ok := fitResolution("4k", dvc); !ok || got != "4k" {
		t.Errorf("dvc 4k => %q,%v; want 4k,true", got, ok)
	}
	pxv := modelCapTable["pxv-seedance-2.0-standard"]
	if got, ok := fitResolution("4k", pxv); !ok || got != "2160p" {
		t.Errorf("pxv-standard 4k => %q,%v; want 2160p,true", got, ok)
	}
	// 720p-only 的模型必须明确拒绝 1080p，绝不能静默降级
	mini := modelCapTable["hgf-seedance-2.0-mini"]
	if _, ok := fitResolution("1080p", mini); ok {
		t.Error("hgf-mini 不支持 1080p，应判定为不可用（静默降级是本适配器要防的核心问题）")
	}
}

// TestCapTableInvariants 守住能力表里最容易记错、且记错就会造成
// "扣了钱拿到降级货" 的几条硬约束。
func TestCapTableInvariants(t *testing.T) {
	// rlm 只收 16:9 / 9:16
	for _, m := range []string{"rlm-seedance-2.0", "rlm-seedance-2.0-fast", "rlm-seedance-2.0-mini"} {
		c := modelCapTable[m]
		if c.Ratios["1:1"] || c.Ratios["4:3"] {
			t.Errorf("%s 只应支持 16:9/9:16", m)
		}
		if !c.Ratios["16:9"] || !c.Ratios["9:16"] {
			t.Errorf("%s 应支持 16:9 与 9:16", m)
		}
	}
	// dvc 系列没有 omni_reference（收不了参考视频/音频，做不了多图锁脸）
	for _, m := range []string{"dvc-seedance-2.0", "dvc-seedance-2.0-fast"} {
		if modelCapTable[m].OmniRef {
			t.Errorf("%s 不应标记为支持 omni_reference", m)
		}
	}
	// 其余 provider 都支持 omni_reference
	for _, m := range []string{"hgf-seedance-2.0", "rlm-seedance-2.0-fast", "pxv-seedance-2.0-standard", "jmg-video-seedance-2.0-vip"} {
		if !modelCapTable[m].OmniRef {
			t.Errorf("%s 应支持 omni_reference", m)
		}
	}
	// ModelList 里不能混进裸名路由器（会导致静默降级）
	for _, m := range ModelList {
		if _, ok := modelCapTable[m]; !ok {
			t.Errorf("ModelList 含未登记能力的模型 %s", m)
		}
		if len(m) < 4 || (m[:4] != "dvc-" && m[:4] != "hgf-" && m[:4] != "jmg-" && m[:4] != "pxv-" && m[:4] != "rlm-") {
			t.Errorf("ModelList 含非 provider 直连模型 %s（裸名路由器会静默降级分辨率）", m)
		}
	}
}

// TestResolutionFromModelName 守住计费正确性：对外名按档位定价
// （sd-2.0-1080p ¥1.8/秒 vs sd-2.0-720p ¥0.88/秒），而上游分辨率靠
// resolution 参数带。名字里的档位必须能被解析出来，否则客户按 1080p
// 付费却拿到 720p。
func TestResolutionFromModelName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"sd-2.0-1080p", "1080p"},
		{"sd-mini-720p", "720p"},
		{"sd-fast-720p", "720p"},
		{"sd-2.0-4k", "4k"},
		{"sd-2.0-480p", "480p"},
		{"seedance-2.0-2160p", "4k"},
		// 不带档位后缀的返回空，交由请求参数决定
		{"sd-2.0", ""},
		{"hgf-seedance-2.0", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := resolutionFromModelName(tc.in); got != tc.want {
			t.Errorf("resolutionFromModelName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFriendlyReason(t *testing.T) {
	cases := []struct {
		code int
		raw  string
		want string // 期望包含的关键片段
	}{
		{-2006, "blocked", "内容未通过上游审核"},
		{-2003, "bad url", "参考素材无法下载"},
		{-2009, "", "上游账号额度不足"},
		{-2012, "", "上游平台额度不足"},
		{-2013, "", "上游并发已满"},
		{-2000, "首尾帧模式必须正好 2 张图片", "首尾帧模式"},
		{0, "reference image contains a real person", "不接受含真人的参考图"},
	}
	for _, tc := range cases {
		got := friendlyReason(tc.code, tc.raw)
		if !contains(got, tc.want) {
			t.Errorf("friendlyReason(%d,%q) = %q, want contains %q", tc.code, tc.raw, got, tc.want)
		}
	}
	// 失败一律退款，文案必须说明，避免客诉
	for _, code := range []int{-2003, -2006, -2009, -2013} {
		if !contains(friendlyReason(code, ""), "费用已退还") {
			t.Errorf("code %d 的文案应包含「费用已退还」", code)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

func TestParseTaskResult(t *testing.T) {
	a := &TaskAdaptor{}

	done := `{"task_id":"c1m1","status":"completed","progress":100,
	          "result":{"url":"https://storage.googleapis.com/x.mp4"},
	          "effective_model":"dvc-seedance-2.0","credits_consumed":480}`
	info, err := a.ParseTaskResult([]byte(done))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Status != model.TaskStatusSuccess {
		t.Errorf("status = %v, want success", info.Status)
	}
	if info.Url != "https://storage.googleapis.com/x.mp4" {
		t.Errorf("url = %q, 成片直链必须落到 Url（video_proxy 的 default 分支据此回源）", info.Url)
	}

	running := `{"task_id":"c1m1","status":"processing","progress":50}`
	info, _ = a.ParseTaskResult([]byte(running))
	if info.Status != model.TaskStatusInProgress || info.Progress != "50%" {
		t.Errorf("processing => %v %q", info.Status, info.Progress)
	}

	queued := `{"task_id":"c1m1","status":"submitted"}`
	info, _ = a.ParseTaskResult([]byte(queued))
	if info.Status != model.TaskStatusQueued {
		t.Errorf("submitted => %v, want queued", info.Status)
	}

	// 查询阶段返回错误对象
	failed := `{"code":-2006,"message":"blocked","data":null}`
	info, _ = a.ParseTaskResult([]byte(failed))
	if info.Status != model.TaskStatusFailure || !contains(info.Reason, "审核") {
		t.Errorf("error object => %v %q", info.Status, info.Reason)
	}
}

// TestParseAsyncFailureShape 守住 260807 实测发现的那个坑：
// 异步失败的报文是 {status:"failed", error:"...", failureType:"..."}，
// **既没有 code 也没有 message**。只读 code/message 会让失败原因变成空串，
// 用户只看到一句没头没尾的 "FAILED:"。
func TestParseAsyncFailureShape(t *testing.T) {
	a := &TaskAdaptor{}
	raw := `{"task_id":"9857b4aa","status":"failed",
	         "error":"Input file contains unsupported image format",
	         "failureType":"blocked","creditsConsumed":null}`
	info, err := a.ParseTaskResult([]byte(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Status != model.TaskStatusFailure {
		t.Fatalf("status = %v, want failure", info.Status)
	}
	if info.Reason == "" {
		t.Fatal("失败原因为空——说明只读了 code/message，漏掉了 error 字段")
	}
	if !contains(info.Reason, "参考素材") {
		t.Errorf("Reason = %q, 应翻译成可行动的素材问题说明", info.Reason)
	}
	if !contains(info.Reason, "费用已退还") {
		t.Errorf("Reason = %q, 应说明不扣费（实测 creditsConsumed=null）", info.Reason)
	}
}

// TestConvertToOpenAIVideoScrubsUpstream 确认不外泄上游身份：
// 成片直链（storage.googleapis.com/davinciweb-*）、provider 代号（dvc/rlm/pxv）
// 都必须被抹掉，否则客户能直接绕过我们找上游。
func TestConvertToOpenAIVideoScrubsUpstream(t *testing.T) {
	a := &TaskAdaptor{}
	task := &model.Task{
		TaskID: "task_public_1",
		Data: []byte(`{"task_id":"c1m_upstream","status":"completed",
		  "result":{"url":"https://storage.googleapis.com/davinciweb-b8892.appspot.com/x.mp4"},
		  "requested_model":"rlm-seedance-2.0","effective_model":"dvc-seedance-2.0",
		  "credits_consumed":480}`),
	}
	task.Properties.OriginModelName = "seedance-2.0-1080p"

	out, err := a.ConvertToOpenAIVideo(task)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := gjson.GetBytes(out, "task_id").String(); got != "task_public_1" {
		t.Errorf("task_id = %q, want public id", got)
	}
	if got := gjson.GetBytes(out, "result.url").String(); contains(got, "googleapis") {
		t.Errorf("成片直链未被代理化: %q", got)
	}
	for _, p := range []string{"requested_model", "effective_model", "credits_consumed"} {
		if gjson.GetBytes(out, p).Exists() {
			t.Errorf("%s 泄露了上游信息，应被删除", p)
		}
	}
	if got := gjson.GetBytes(out, "model").String(); got != "seedance-2.0-1080p" {
		t.Errorf("model = %q, want 对外名", got)
	}
}

// TestConvertToOpenAIVideoSurfacesFriendlyFailure 失败时用户应看到翻译过的
// 原因，而不是上游原始串。
func TestConvertToOpenAIVideoSurfacesFriendlyFailure(t *testing.T) {
	a := &TaskAdaptor{}
	task := &model.Task{
		TaskID:     "task_public_2",
		Status:     model.TaskStatusFailure,
		FailReason: "内容未通过上游审核，费用已退还。请调整提示词",
		Data:       []byte(`{"task_id":"c1m","status":"failed","code":-2006,"message":"blocked"}`),
	}
	out, err := a.ConvertToOpenAIVideo(task)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := gjson.GetBytes(out, "error.message").String(); !contains(got, "审核") {
		t.Errorf("error.message = %q, want 翻译后的原因", got)
	}
	if got := gjson.GetBytes(out, "status").String(); !contains(got, "审核") {
		t.Errorf("status = %q, want 含可读原因", got)
	}
}
