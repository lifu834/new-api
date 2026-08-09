package sudashui

import (
	"github.com/QuantumNous/new-api/common"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

func jsonUnmarshal(s string, v any) error {
	return common.Unmarshal([]byte(s), v)
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestParseTaskResultWrapped 守住本上游最容易踩的坑：查询响应**包了一层**
// {code, data:{status, data:{creations:[{url}]}}}。只读顶层 status 会永远拿到
// 空串，任务会永远卡在未知状态（260808 首次测试就是这么翻车的）。
func TestParseTaskResultWrapped(t *testing.T) {
	a := &TaskAdaptor{}

	done := `{"code":"success","message":"","data":{
	  "task_id":"task_x","status":"SUCCESS","progress":"100%","quota":1656000,
	  "data":{"state":"success","creations":[
	     {"id":"gf03:task_y","url":"https://files.sudashuiapi.com/proxy/outputs/gf03/task_y.mp4"}]}}}`
	info, err := a.ParseTaskResult([]byte(done))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Status != model.TaskStatusSuccess {
		t.Fatalf("status = %v, want success（说明没读到 data.status）", info.Status)
	}
	if info.Url != "https://files.sudashuiapi.com/proxy/outputs/gf03/task_y.mp4" {
		t.Errorf("url = %q, 成片必须从 data.data.creations[0].url 取", info.Url)
	}

	prog := `{"code":"success","data":{"task_id":"t","status":"IN_PROGRESS","progress":"30%"}}`
	info, _ = a.ParseTaskResult([]byte(prog))
	if info.Status != model.TaskStatusInProgress || info.Progress != "30%" {
		t.Errorf("IN_PROGRESS => %v %q", info.Status, info.Progress)
	}

	// 真人风控失败（wf 等便宜档实测形态）
	face := `{"code":"success","data":{"task_id":"t","status":"FAILURE",
	  "fail_reason":"检测到参考素材包含人脸，请更换不含人脸的图片后再试",
	  "data":{"state":"failed","err_code":"invalid_asset",
	          "error_message":"检测到参考素材包含人脸，请更换不含人脸的图片后再试"}}}`
	info, _ = a.ParseTaskResult([]byte(face))
	if info.Status != model.TaskStatusFailure {
		t.Fatalf("FAILURE => %v", info.Status)
	}
	if !contains(info.Reason, "不接受含人脸") || !contains(info.Reason, "费用已退还") {
		t.Errorf("Reason = %q，应翻译成换档位建议并注明退款", info.Reason)
	}
}

// TestNormalizeFetchResponseAvoidsSelfFormat 守住"上游也是 new-api"的坑：
//
// service/task_polling.go 会先试着按自家 dto.TaskResponse 解析，只要顶层
// code=="success" 就认定是自家格式，于是**完全跳过 adaptor.ParseTaskResult**，
// 改用 t.GetResultURL() 取 private_data.result_url —— 那是我方内部字段，上游
// 不会返回 ⇒ URL 为空 ⇒ ResultURL 退化成我们自己的代理地址 ⇒ /content 取自己
// ⇒ 永久 502。所以必须把顶层 code 改名，让流程回落到本适配器的解析器。
func TestNormalizeFetchResponseAvoidsSelfFormat(t *testing.T) {
	raw := `{"code":"success","message":"","data":{"task_id":"t","status":"SUCCESS",
	         "data":{"creations":[{"url":"https://files.sudashuiapi.com/x.mp4"}]}}}`
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(raw))}

	out := normalizeFetchResponse(resp)
	body, err := io.ReadAll(out.Body)
	if err != nil {
		t.Fatal(err)
	}
	if got := gjson.GetBytes(body, "code").String(); got == "success" {
		t.Error("顶层 code 仍是 success —— 会被误判为我方自家格式，跳过本适配器解析")
	}
	// 其余内容必须原样保留，ParseTaskResult 依赖 data.*
	if got := gjson.GetBytes(body, "data.status").String(); got != "SUCCESS" {
		t.Errorf("data.status 被破坏: %q", got)
	}
	if got := gjson.GetBytes(body, "data.data.creations.0.url").String(); got == "" {
		t.Error("成片地址被破坏")
	}

	// 改名后本适配器应能正常解析出成功与 URL
	a := &TaskAdaptor{}
	info, err := a.ParseTaskResult(body)
	if err != nil {
		t.Fatal(err)
	}
	if info.Status != model.TaskStatusSuccess || info.Url == "" {
		t.Errorf("解析结果 = %v %q, want success + url", info.Status, info.Url)
	}
}

// TestSuccessWithoutURLStaysInProgress 守住实测踩到的时序坑：
// 上游 state 先翻 success、creations 稍后才填。若此刻就报成功且不带 URL，
// task_polling 会把 ResultURL 退化成我们自己的代理地址，而它只在 SUCCESS
// 那一次写入 —— 代理随后去取自己，永久 502（成片其实是好的，只是取不到）。
func TestSuccessWithoutURLStaysInProgress(t *testing.T) {
	a := &TaskAdaptor{}

	// creations 还没填
	raw := `{"code":"success","data":{"task_id":"t","status":"SUCCESS",
	          "data":{"state":"success","creations":[]}}}`
	info, err := a.ParseTaskResult([]byte(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Status == model.TaskStatusSuccess {
		t.Error("没有成片地址时不应报成功——会导致 ResultURL 永久指向我们自己")
	}
	if info.Status != model.TaskStatusInProgress {
		t.Errorf("status = %v, want in-progress（继续轮询）", info.Status)
	}

	// creations 有但 url 是空串，同样不算成功
	raw2 := `{"code":"success","data":{"status":"SUCCESS",
	          "data":{"creations":[{"id":"x","url":""}]}}}`
	info2, _ := a.ParseTaskResult([]byte(raw2))
	if info2.Status == model.TaskStatusSuccess {
		t.Error("url 为空串时同样不应报成功")
	}
}

// TestSubmitTaskIDShape 守住 260808 实测踩到的坑：**提交响应与查询响应结构不同**。
// 提交把任务号放在顶层，查询才包进 data。只按查询结构读提交响应 → task_id 为空
// → 整条链路 500 "task_id is empty"。
func TestSubmitTaskIDShape(t *testing.T) {
	var flat upstreamResponse
	if err := jsonUnmarshal(`{"id":"task_x","task_id":"task_x","status":"queued"}`, &flat); err != nil {
		t.Fatal(err)
	}
	if got := flat.submitTaskID(); got != "task_x" {
		t.Errorf("顶层形态 submitTaskID = %q, want task_x", got)
	}

	var nested upstreamResponse
	if err := jsonUnmarshal(`{"code":"success","data":{"task_id":"task_y"}}`, &nested); err != nil {
		t.Fatal(err)
	}
	if got := nested.submitTaskID(); got != "task_y" {
		t.Errorf("嵌套形态 submitTaskID = %q, want task_y", got)
	}
}

// TestFriendlyReasonBadRequest 上游只回一句无信息量的 "Bad Request"。
// 实测这是**该上游渠道自身故障**（同一请求体打 wf 成功、打 hn 全失败），
// 提示用户去改参数是误导，应引导他换档位。
func TestFriendlyReasonBadRequest(t *testing.T) {
	got := friendlyReason("Bad Request", "Bad Request")
	if !contains(got, "改用其他档位") {
		t.Errorf("friendlyReason(Bad Request) = %q，应引导换档位而不是改参数", got)
	}
}

// TestBuildPayloadOfficialIndexes sdas-gf 官方模型的真人图必须用
// officialAssetIndexes 标记，漏标会被真人风控直接拦掉。
func TestBuildPayloadOfficialIndexes(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	req := relaycommon.TaskSubmitReq{
		Prompt: "@image1 微笑",
		Images: []string{"https://a/1.jpg", "https://a/2.jpg"},
	}

	p := buildPayload(c, &req, "sdas-gf3-seedance-2.0-720p")
	if len(p.OfficialAssetIndexes) != 2 ||
		p.OfficialAssetIndexes[0] != 0 || p.OfficialAssetIndexes[1] != 1 {
		t.Errorf("官方模型应标记全部图片下标, got %v", p.OfficialAssetIndexes)
	}
	if p.Mode != modeReferences {
		t.Errorf("mode = %q, want %q", p.Mode, modeReferences)
	}

	// 非官方模型不该带这个字段（上游可能不认）
	p2 := buildPayload(c, &req, "sdas-wf-sd2.0-fast-933-720p")
	if len(p2.OfficialAssetIndexes) != 0 {
		t.Errorf("非 gf 模型不应带 officialAssetIndexes, got %v", p2.OfficialAssetIndexes)
	}
}

func TestIsOfficialModel(t *testing.T) {
	for _, m := range []string{"sdas-gf-seedance-2.0-720p", "sdas-gf3-seedance-2.0-720p", "sdas-gf2-seedance-2.0-480p"} {
		if !isOfficialModel(m) {
			t.Errorf("%s 应判定为官方模型", m)
		}
	}
	for _, m := range []string{"sdas-wf-sd2.0-pro-933-720p", "sdas-ll-sd2.5-pro-720p", "ld-sdas-cvk-pro-933-720p"} {
		if isOfficialModel(m) {
			t.Errorf("%s 不应判定为官方模型", m)
		}
	}
}

// TestEstimateBillingSeconds 该站全线按秒（实测 quota 1656000 = 0.414 × 4 秒）。
func TestEstimateBillingSeconds(t *testing.T) {
	a := &TaskAdaptor{}
	for _, tc := range []struct {
		req  relaycommon.TaskSubmitReq
		want float64
	}{
		{relaycommon.TaskSubmitReq{Duration: 4}, 4},
		{relaycommon.TaskSubmitReq{Seconds: "29"}, 29},
		{relaycommon.TaskSubmitReq{}, 5},
	} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Set("task_request", tc.req)
		got := a.EstimateBilling(c, &relaycommon.RelayInfo{})
		if got == nil || got["seconds"] != tc.want {
			t.Errorf("%+v => %v, want seconds=%v", tc.req, got, tc.want)
		}
	}
}

func TestResolutionFromModelName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"sdas-gf3-seedance-2.0-720p", "720p"},
		{"sdas-gf-seedance-2.0-1080p", "1080p"},
		{"sdas-gf-seedance-2.0-4k", "4k"},
		{"sdas-gf-seedance-2.0-2k", "2k"},
		{"ld-sdas-2-cvk", ""},
	}
	for _, tc := range cases {
		if got := resolutionFromModelName(tc.in); got != tc.want {
			t.Errorf("resolutionFromModelName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestConvertToOpenAIVideoFlatShape 上游本身是个 new-api，查询响应带着它的整套
// 内部结构。必须**重新构造**扁平响应而不是打补丁，否则：
//   - 泄露上游身份与我方成本（channel_id / quota / properties.upstream_model_name）
//   - 响应形状与 dimensio / mai 不一致，海外组的"统一"就只统一了请求
func TestConvertToOpenAIVideoFlatShape(t *testing.T) {
	a := &TaskAdaptor{}
	task := &model.Task{
		TaskID:   "task_public_1",
		Status:   model.TaskStatusSuccess,
		Progress: "100%",
		Data: []byte(`{"code":"success","data":{
		  "action":"textGenerate","id":690033,
		  "task_id":"task_upstream","channel_id":27,"quota":1656000,"platform":"52",
		  "status":"SUCCESS",
		  "properties":{"upstream_model_name":"sdas-gf3-seedance-2.0-720p"},
		  "data":{"creations":[{"url":"https://files.sudashuiapi.com/proxy/outputs/x.mp4"}]}}}`),
	}
	task.Properties.OriginModelName = "sd-2.0-720p"

	out, err := a.ConvertToOpenAIVideo(task)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 上游内部结构一律不得出现
	for _, p := range []string{"code", "data", "data.channel_id", "data.quota",
		"data.properties.upstream_model_name", "action", "platform"} {
		if gjson.GetBytes(out, p).Exists() {
			t.Errorf("%s 不应出现在对外响应里（泄露上游/我方成本）", p)
		}
	}
	if got := gjson.GetBytes(out, "video_url").String(); contains(got, "sudashuiapi.com") || got == "" {
		t.Errorf("video_url = %q, 应为本站代理地址", got)
	}
	// object 是类型标记不是 URL（meaicc 才把成片放 object，别搞混）
	if got := gjson.GetBytes(out, "object").String(); got != "video" {
		t.Errorf("object = %q, want \"video\"", got)
	}
	if got := gjson.GetBytes(out, "status").String(); got != "completed" {
		t.Errorf("status = %q, want completed", got)
	}
	if got := gjson.GetBytes(out, "model").String(); got != "sd-2.0-720p" {
		t.Errorf("model = %q, want 对外名", got)
	}
	if got := gjson.GetBytes(out, "task_id").String(); got != "task_public_1" {
		t.Errorf("task_id = %q, want public id", got)
	}

	// 失败时给出可读原因，且不带成片地址
	fail := &model.Task{TaskID: "t2", Status: model.TaskStatusFailure,
		FailReason: "该档位不接受含人脸的参考图，费用已退还。请改用支持真人的档位",
		Data:       []byte(`{"code":"success","data":{"status":"FAILURE"}}`)}
	out2, _ := a.ConvertToOpenAIVideo(fail)
	if got := gjson.GetBytes(out2, "error.message").String(); !contains(got, "不接受含人脸") {
		t.Errorf("error.message = %q", got)
	}
	if gjson.GetBytes(out2, "video_url").Exists() {
		t.Error("失败任务不应带 video_url")
	}
}
