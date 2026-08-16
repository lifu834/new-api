package secureskill

import (
	"net/http/httptest"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/model"
)

// mp4Head 构造一个最小的 ISO BMFF 文件头：第 5-8 字节是 "ftyp"。
func mp4Head() []byte {
	b := make([]byte, 32)
	copy(b[0:4], []byte{0, 0, 0, 24}) // box size
	copy(b[4:8], []byte("ftyp"))
	copy(b[8:12], []byte("isom"))
	return b
}

// TestParseTaskResult 锁住三种上游形态的判别。该上游完成时**不返回任何 JSON**，
// 只有 mp4 二进制；未完成是 409 错误体。判错会导致任务永远卡住或误判失败退款。
func TestParseTaskResult(t *testing.T) {
	a := &TaskAdaptor{}

	t.Run("FetchTask 合成的完成标记", func(t *testing.T) {
		info, err := a.ParseTaskResult([]byte(`{"status":"SUCCEEDED"}`))
		if err != nil {
			t.Fatal(err)
		}
		if info.Status != model.TaskStatusSuccess {
			t.Errorf("status = %v, want success", info.Status)
		}
		if info.Url != "" {
			t.Errorf("Url 应留空交由 video_proxy 回源，实际 = %q", info.Url)
		}
	})

	t.Run("真二进制兜底也要认", func(t *testing.T) {
		info, err := a.ParseTaskResult(mp4Head())
		if err != nil {
			t.Fatal(err)
		}
		if info.Status != model.TaskStatusSuccess {
			t.Errorf("mp4 二进制未被识别为完成: %v", info.Status)
		}
	})

	t.Run("409 未完成", func(t *testing.T) {
		info, err := a.ParseTaskResult(
			[]byte(`{"error":{"code":"task_not_completed","message":"Task is not completed"}}`))
		if err != nil {
			t.Fatal(err)
		}
		if info.Status != model.TaskStatusInProgress {
			t.Errorf("status = %v, want in_progress（误判失败会错误退款并终结任务）", info.Status)
		}
	})

	t.Run("真失败要翻译成人话", func(t *testing.T) {
		info, err := a.ParseTaskResult(
			[]byte(`{"error":{"code":"billing_error","message":"insufficient balance"}}`))
		if err != nil {
			t.Fatal(err)
		}
		if info.Status != model.TaskStatusFailure {
			t.Errorf("status = %v, want failure", info.Status)
		}
		if !strings.Contains(info.Reason, "余额不足") || !strings.Contains(info.Reason, "已退还") {
			t.Errorf("Reason = %q, 未翻译或未告知退款", info.Reason)
		}
	})

	t.Run("读不懂的响应当进行中而非失败", func(t *testing.T) {
		info, err := a.ParseTaskResult([]byte(`{"weird":"payload"}`))
		if err != nil {
			t.Fatal(err)
		}
		if info.Status != model.TaskStatusInProgress {
			t.Errorf("未知响应应保守判为进行中（误判失败会白退款），实际 %v", info.Status)
		}
	})
}

// TestConvertToOpenAIVideoSynthesizes 锁住"不回显 task.Data"。
// 该上游轮询响应是 409 错误体或二进制，会覆盖 task.Data；若照抄就会把
// "Task is not completed" 当成任务对象返回给用户。
func TestConvertToOpenAIVideoSynthesizes(t *testing.T) {
	a := &TaskAdaptor{}
	task := &model.Task{
		TaskID: "task_pub999",
		Status: model.TaskStatusSuccess,
		Data:   []byte(`{"error":{"code":"task_not_completed"}}`), // 被轮询污染的数据
	}
	task.Properties.OriginModelName = "seedance-2.0-mini"

	out, err := a.ConvertToOpenAIVideo(task)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if strings.Contains(s, "task_not_completed") {
		t.Errorf("把轮询污染的数据回显给了用户: %s", s)
	}
	if !strings.Contains(s, "task_pub999") || !strings.Contains(s, "seedance-2.0-mini") {
		t.Errorf("对外字段缺失: %s", s)
	}
	for _, leak := range []string{"video-2.0-pro", "secure-skill"} {
		if strings.Contains(s, leak) {
			t.Errorf("泄露上游信息 %q: %s", leak, s)
		}
	}
}

// TestEstimateBillingSeconds 守住"换组即亏损"这类静默错误。
//
// 本上游**按秒**计价（实测 10 秒扣 ¥6.00 = ¥0.60/秒）。它最初挂在按次的对外名
// 下，沿用 BaseBilling（返回 nil，无乘数）没问题；但一旦挂到按秒的对外名
// （海外组 sd-2.0-720p ¥0.88/秒）上，缺乘数就变成**按 1 秒收费** ——
// 4 秒成本 ¥2.40、我们只收 ¥0.88，每单倒贴，且完全没有报错。
func TestEstimateBillingSeconds(t *testing.T) {
	a := &TaskAdaptor{}
	cases := []struct {
		req  relaycommon.TaskSubmitReq
		want float64
	}{
		{relaycommon.TaskSubmitReq{Duration: 4}, 4},
		{relaycommon.TaskSubmitReq{Duration: 15}, 15},
		{relaycommon.TaskSubmitReq{Seconds: "10"}, 10},
	}
	for _, tc := range cases {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Set("task_request", tc.req)
		got := a.EstimateBilling(c, &relaycommon.RelayInfo{})
		if got == nil {
			t.Fatal("EstimateBilling 返回 nil —— 挂到按秒对外名下会按 1 秒收费，每单倒贴")
		}
		if got["seconds"] != tc.want {
			t.Errorf("duration=%d seconds=%q => %v, want %v",
				tc.req.Duration, tc.req.Seconds, got["seconds"], tc.want)
		}
	}
}

// TestBuildRequestBodyPureText 260816 换成海外组 key（per_second）后纯文生放行：
// 无素材时必须带 functionMode=first_last_frames（海外组纯文生惯例，直连实测），
// 不带的话上游默认 omni_reference 会拒无素材请求；有素材时不发 functionMode，
// 素材以 files=URL 重复字段发（特价时代验证过的形态，海外组沿用）。
func TestBuildRequestBodyPureText(t *testing.T) {
	gin.SetMode(gin.TestMode)
	a := &TaskAdaptor{}

	build := func(req relaycommon.TaskSubmitReq) string {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/v1/videos", strings.NewReader("{}"))
		c.Set("task_request", req)
		r, err := a.BuildRequestBody(c, &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "video-2.0-pro"}})
		if err != nil {
			t.Fatalf("BuildRequestBody: %v", err)
		}
		var sb strings.Builder
		buf := make([]byte, 64*1024)
		for {
			n, e := r.Read(buf)
			sb.Write(buf[:n])
			if e != nil {
				break
			}
		}
		return sb.String()
	}

	pure := build(relaycommon.TaskSubmitReq{Prompt: "a cat", Duration: 4})
	if !strings.Contains(pure, "first_last_frames") {
		t.Error("纯文生须带 functionMode=first_last_frames，否则上游按 omni 拒无素材请求")
	}
	if strings.Contains(pure, "name=\"files\"") {
		t.Error("纯文生不应出现 files 字段")
	}

	ref := build(relaycommon.TaskSubmitReq{Prompt: "a cat", Duration: 4,
		Images: []string{"https://example.com/a.png"}})
	if strings.Contains(ref, "functionMode") {
		t.Error("带素材时不应发 functionMode（沿用验证过的 files 形态）")
	}
	if !strings.Contains(ref, "https://example.com/a.png") {
		t.Error("素材 URL 未写入 files 字段")
	}
}
