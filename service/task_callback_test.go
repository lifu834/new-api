package service

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTaskCallbackSnapshotParsing verifies the chatgpt2api snapshot JSON parses
// through common.Unmarshal into the expected fields (id/status/data/usage/error).
func TestTaskCallbackSnapshotParsing(t *testing.T) {
	raw := []byte(`{
		"id": "task_abc123",
		"status": "success",
		"mode": "generate",
		"model": "gpt-image-2",
		"size": "1024x1024",
		"quality": "high",
		"created_at": 1,
		"updated_at": 2,
		"progress": "image_stream_resolve_start",
		"data": [{"url": "https://cdn.example/img.png", "revised_prompt": "a cat"}],
		"usage": {"total_tokens": 4321},
		"error": ""
	}`)

	var snap TaskCallbackSnapshot
	require.NoError(t, common.Unmarshal(raw, &snap))
	assert.Equal(t, "task_abc123", snap.ID)
	assert.Equal(t, "success", snap.Status)
	assert.Equal(t, "gpt-image-2", snap.Model)
	assert.Equal(t, "image_stream_resolve_start", snap.Progress)
	require.Len(t, snap.Data, 1)
	assert.Equal(t, "https://cdn.example/img.png", snap.Data[0].URL)
	require.NotNil(t, snap.Usage)
	assert.Equal(t, 4321, snap.Usage.TotalTokens)
}

// TestMapCallbackStatus checks the status → model.TaskStatus mapping plus the
// success/error side fields (ResultURL source, token count, failure reason).
func TestMapCallbackStatus(t *testing.T) {
	cases := []struct {
		name       string
		snap       TaskCallbackSnapshot
		wantStatus string
		wantURL    string
		wantTokens int
		wantReason string
		wantProg   string
	}{
		{
			name:       "queued",
			snap:       TaskCallbackSnapshot{ID: "t1", Status: "queued"},
			wantStatus: string(model.TaskStatusQueued),
		},
		{
			name:       "running with progress",
			snap:       TaskCallbackSnapshot{ID: "t2", Status: "running", Progress: "getting_account"},
			wantStatus: string(model.TaskStatusInProgress),
			wantProg:   "getting_account",
		},
		{
			name: "success with url and usage",
			snap: TaskCallbackSnapshot{
				ID:     "t3",
				Status: "success",
				Data:   []TaskCallbackImage{{URL: "https://x/y.png"}},
				Usage:  &TaskCallbackUsage{TotalTokens: 100},
			},
			wantStatus: string(model.TaskStatusSuccess),
			wantURL:    "https://x/y.png",
			wantTokens: 100,
		},
		{
			name:       "error with reason",
			snap:       TaskCallbackSnapshot{ID: "t4", Status: "error", Error: "content policy"},
			wantStatus: string(model.TaskStatusFailure),
			wantReason: "content policy",
		},
		{
			name:       "error without reason falls back",
			snap:       TaskCallbackSnapshot{ID: "t5", Status: "error"},
			wantStatus: string(model.TaskStatusFailure),
			wantReason: "task failed",
		},
		{
			name:       "unknown status keeps running",
			snap:       TaskCallbackSnapshot{ID: "t6", Status: "weird"},
			wantStatus: string(model.TaskStatusInProgress),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info, err := MapCallbackStatus(&tc.snap)
			require.NoError(t, err)
			assert.Equal(t, tc.wantStatus, info.Status)
			assert.Equal(t, tc.wantURL, info.Url)
			assert.Equal(t, tc.wantTokens, info.TotalTokens)
			if tc.wantTokens > 0 {
				assert.Equal(t, tc.wantTokens, info.CompletionTokens)
			}
			assert.Equal(t, tc.wantReason, info.Reason)
			assert.Equal(t, tc.wantProg, info.Progress)
		})
	}
}
