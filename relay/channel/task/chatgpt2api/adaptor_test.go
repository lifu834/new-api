package chatgpt2api

import (
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"

	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
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

func newTestCtx(method, path string) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(method, path, nil)
	return c
}

// TestIsEditsRequest covers both the explicit context flag (set by the
// distributor) and the path-suffix fallback.
func TestIsEditsRequest(t *testing.T) {
	// flag set by distributor
	c := newTestCtx(http.MethodPost, "/v1/images/async/edits")
	c.Set("image_async_op", "edits")
	if !isEditsRequest(c) {
		t.Errorf("expected edits=true when image_async_op flag set")
	}

	// path-suffix fallback (flag absent)
	c = newTestCtx(http.MethodPost, "/v1/images/async/edits")
	if !isEditsRequest(c) {
		t.Errorf("expected edits=true from /edits path suffix")
	}

	// generations must NOT be edits
	c = newTestCtx(http.MethodPost, "/v1/images/async/generations")
	if isEditsRequest(c) {
		t.Errorf("expected edits=false for generations path")
	}

	// nil-safe
	if isEditsRequest(nil) {
		t.Errorf("expected edits=false for nil context")
	}
}

// TestBuildRequestURL_EditsVsGenerations proves the URL forks on a.isEdits.
func TestBuildRequestURL_EditsVsGenerations(t *testing.T) {
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: "https://up.example"}}

	gen := &TaskAdaptor{}
	gen.Init(info)
	gotGen, err := gen.BuildRequestURL(info)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "https://up.example/api/image-tasks/generations"; gotGen != want {
		t.Errorf("generations url = %q, want %q", gotGen, want)
	}

	edit := &TaskAdaptor{isEdits: true}
	edit.Init(info) // Init does not reset isEdits
	gotEdit, err := edit.BuildRequestURL(info)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "https://up.example/api/image-tasks/edits"; gotEdit != want {
		t.Errorf("edits url = %q, want %q", gotEdit, want)
	}
}

// TestDetectImageMimeType covers extension→MIME mapping incl. the png fallback.
func TestDetectImageMimeType(t *testing.T) {
	cases := map[string]string{
		"a.png":     "image/png",
		"a.PNG":     "image/png",
		"a.jpg":     "image/jpeg",
		"a.jpeg":    "image/jpeg",
		"a.webp":    "image/webp",
		"noext":     "image/png",
		"a.unknown": "image/png",
	}
	for name, want := range cases {
		if got := detectImageMimeType(name); got != want {
			t.Errorf("detectImageMimeType(%q) = %q, want %q", name, got, want)
		}
	}
}

// TestCollectImageFiles covers the image / image[] / image[i] resolution order.
func TestCollectImageFiles(t *testing.T) {
	mk := func(keys ...string) *multipart.Form {
		f := &multipart.Form{File: map[string][]*multipart.FileHeader{}}
		for _, k := range keys {
			f.File[k] = []*multipart.FileHeader{{Filename: k + ".png"}}
		}
		return f
	}
	if got := collectImageFiles(mk("image")); len(got) != 1 {
		t.Errorf(`"image" field: got %d files, want 1`, len(got))
	}
	if got := collectImageFiles(mk("image[]")); len(got) != 1 {
		t.Errorf(`"image[]" field: got %d files, want 1`, len(got))
	}
	if got := collectImageFiles(mk("image[0]", "image[1]")); len(got) != 2 {
		t.Errorf(`"image[i]" fields: got %d files, want 2`, len(got))
	}
	if got := collectImageFiles(mk("mask")); len(got) != 0 {
		t.Errorf("no image field: got %d files, want 0", len(got))
	}
	if got := collectImageFiles(nil); got != nil {
		t.Errorf("nil form: got %v, want nil", got)
	}
}
