package relay

import (
	"testing"

	"github.com/QuantumNous/new-api/model"
)

// TestExtractImageRevisedPrompt covers best-effort revised_prompt extraction from
// the raw chatgpt2api poll response stored in task.Data.
func TestExtractImageRevisedPrompt(t *testing.T) {
	raw := `{"items":[{"data":[{"url":"u","revised_prompt":"a blue cup"}]}]}`
	if got := extractImageRevisedPrompt([]byte(raw)); got != "a blue cup" {
		t.Errorf("revised_prompt = %q, want %q", got, "a blue cup")
	}
	if got := extractImageRevisedPrompt([]byte(`{"items":[]}`)); got != "" {
		t.Errorf("expected empty for no items, got %q", got)
	}
	if got := extractImageRevisedPrompt([]byte(`{"items":[{"data":[]}]}`)); got != "" {
		t.Errorf("expected empty for no data, got %q", got)
	}
	if got := extractImageRevisedPrompt(nil); got != "" {
		t.Errorf("expected empty for nil, got %q", got)
	}
	if got := extractImageRevisedPrompt([]byte("not json")); got != "" {
		t.Errorf("expected empty for malformed, got %q", got)
	}
}

// TestMapTaskStatusToSimple covers the client-facing status strings used by the GET
// result endpoint (studio2 keys successValues/failureValues on these).
func TestMapTaskStatusToSimple(t *testing.T) {
	cases := map[model.TaskStatus]string{
		model.TaskStatusSuccess:    "succeeded",
		model.TaskStatusFailure:    "failed",
		model.TaskStatusQueued:     "queued",
		model.TaskStatusSubmitted:  "queued",
		model.TaskStatusInProgress: "processing",
	}
	for in, want := range cases {
		if got := mapTaskStatusToSimple(in); got != want {
			t.Errorf("mapTaskStatusToSimple(%q) = %q, want %q", in, got, want)
		}
	}
}
