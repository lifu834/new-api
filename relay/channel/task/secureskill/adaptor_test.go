package secureskill

import (
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
