package chatgpt2api

import (
	"testing"

	"github.com/QuantumNous/new-api/model"
)

// TestParseTaskResult_StatusMapping covers the upstream→internal status mapping,
// url/token extraction on success, and the empty-items / unknown-status fallbacks.
func TestParseTaskResult_StatusMapping(t *testing.T) {
	a := &TaskAdaptor{}

	cases := []struct {
		name        string
		body        string
		wantStatus  model.TaskStatus
		wantUrl     string
		wantTokens  int
		wantReason  string
		wantProgress string
	}{
		{
			name:       "queued",
			body:       `{"items":[{"id":"t1","status":"queued"}],"missing_ids":[]}`,
			wantStatus: model.TaskStatusQueued,
		},
		{
			name:         "running with step-name progress (string, passed through)",
			body:         `{"items":[{"id":"t1","status":"running","progress":"getting_account"}]}`,
			wantStatus:   model.TaskStatusInProgress,
			wantProgress: "getting_account",
		},
		{
			name:       "success with url and usage",
			body:       `{"items":[{"id":"t1","status":"success","data":[{"url":"https://img.nycatai.com/images/2026/07/11/x.png","revised_prompt":"a red star"}],"usage":{"total_tokens":1059}}]}`,
			wantStatus: model.TaskStatusSuccess,
			wantUrl:    "https://img.nycatai.com/images/2026/07/11/x.png",
			wantTokens: 1059,
		},
		{
			name:       "error with reason",
			body:       `{"items":[{"id":"t1","status":"error","error":"号池无可用账号"}]}`,
			wantStatus: model.TaskStatusFailure,
			wantReason: "号池无可用账号",
		},
		{
			name:       "empty items -> in progress (not yet visible)",
			body:       `{"items":[],"missing_ids":["t1"]}`,
			wantStatus: model.TaskStatusInProgress,
		},
		{
			name:       "unknown status -> keep polling",
			body:       `{"items":[{"id":"t1","status":"weird"}]}`,
			wantStatus: model.TaskStatusInProgress,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := a.ParseTaskResult([]byte(tc.body))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Status != string(tc.wantStatus) {
				t.Errorf("status = %q, want %q", got.Status, tc.wantStatus)
			}
			if got.Url != tc.wantUrl {
				t.Errorf("url = %q, want %q", got.Url, tc.wantUrl)
			}
			if got.TotalTokens != tc.wantTokens {
				t.Errorf("tokens = %d, want %d", got.TotalTokens, tc.wantTokens)
			}
			if got.Reason != tc.wantReason {
				t.Errorf("reason = %q, want %q", got.Reason, tc.wantReason)
			}
			if got.Progress != tc.wantProgress {
				t.Errorf("progress = %q, want %q", got.Progress, tc.wantProgress)
			}
		})
	}
}

// TestParseTaskResult_MalformedBody ensures a non-JSON body returns an error
// (so the poller keeps the task pending rather than silently succeeding).
func TestParseTaskResult_MalformedBody(t *testing.T) {
	a := &TaskAdaptor{}
	if _, err := a.ParseTaskResult([]byte("not json")); err == nil {
		t.Fatalf("expected error on malformed body, got nil")
	}
}

// (extractImageRevisedPrompt / mapTaskStatusToSimple live in package relay and are
// covered by relay/relay_task_chatgpt2api_test.go.)
