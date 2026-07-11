package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel/task/taskcommon"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

// ---------------------------------------------------------------------------
// Webhook (push) task-completion receiver.
//
// Upstream chatgpt2api POSTs a terminal task snapshot to new-api's own webhook
// endpoint (see controller.TaskCallback). This file translates that snapshot
// into the SAME terminal handling the poller performs in
// service/task_polling.go's updateVideoSingleTask — reusing the CAS transition
// (model.Task.UpdateWithStatus), settleTaskBillingOnComplete, and
// RefundTaskQuota. It does NOT reimplement any billing math.
//
// Idempotency: because both a webhook and a poll can race to settle the same
// task, the terminal transition is guarded by the CAS UpdateWithStatus(fromStatus).
// Only the caller that flips the DB row out of its non-terminal status wins and
// runs billing; the loser skips. A snapshot for an already-terminal task is a
// no-op. This is exactly the guarantee the poller relies on.
// ---------------------------------------------------------------------------

// TaskCallbackImage mirrors one element of the snapshot `data` array.
type TaskCallbackImage struct {
	URL           string `json:"url"`
	RevisedPrompt string `json:"revised_prompt,omitempty"`
}

// TaskCallbackUsage mirrors the snapshot `usage` object.
type TaskCallbackUsage struct {
	TotalTokens int `json:"total_tokens,omitempty"`
}

// TaskCallbackSnapshot is the chatgpt2api terminal task snapshot pushed to the
// webhook. `id` is the new-api PublicTaskID; `status ∈ queued|running|success|error`.
type TaskCallbackSnapshot struct {
	ID       string              `json:"id"`
	Status   string              `json:"status"`
	Mode     string              `json:"mode,omitempty"`
	Model    string              `json:"model,omitempty"`
	Size     string              `json:"size,omitempty"`
	Quality  string              `json:"quality,omitempty"`
	Progress string              `json:"progress,omitempty"`
	Data     []TaskCallbackImage `json:"data,omitempty"`
	Usage    *TaskCallbackUsage  `json:"usage,omitempty"`
	Error    string              `json:"error,omitempty"`
}

// MapCallbackStatus converts a chatgpt2api snapshot into a generic TaskInfo.
// Mirrors chatgpt2api TaskAdaptor.ParseTaskResult so webhook and poll agree.
func MapCallbackStatus(snap *TaskCallbackSnapshot) (*relaycommon.TaskInfo, error) {
	if snap == nil {
		return nil, fmt.Errorf("nil snapshot")
	}
	info := &relaycommon.TaskInfo{Code: 0, TaskID: snap.ID}
	switch snap.Status {
	case "queued":
		info.Status = string(model.TaskStatusQueued)
	case "running":
		info.Status = string(model.TaskStatusInProgress)
	case "success":
		info.Status = string(model.TaskStatusSuccess)
		if len(snap.Data) > 0 {
			info.Url = snap.Data[0].URL
		}
		if snap.Usage != nil && snap.Usage.TotalTokens > 0 {
			info.TotalTokens = snap.Usage.TotalTokens
			info.CompletionTokens = snap.Usage.TotalTokens
		}
	case "error":
		info.Status = string(model.TaskStatusFailure)
		if snap.Error != "" {
			info.Reason = snap.Error
		} else {
			info.Reason = "task failed"
		}
	default:
		// unknown upstream status — treat as still running (keep polling).
		info.Status = string(model.TaskStatusInProgress)
	}
	// progress is an upstream step-name string (not a percentage) — pass through.
	if snap.Progress != "" {
		info.Progress = snap.Progress
	}
	return info, nil
}

// HandleTaskCallback applies a pushed task snapshot to the local task, reusing
// the poller's terminal handling and billing. Safe to call concurrently with the
// poller for the same task (CAS-guarded). Returns an error only for lookup/parse
// problems; callers should still ack the webhook (best-effort, fires once).
func HandleTaskCallback(ctx context.Context, snap *TaskCallbackSnapshot) error {
	if snap == nil || snap.ID == "" {
		return fmt.Errorf("callback snapshot missing id")
	}

	taskResult, err := MapCallbackStatus(snap)
	if err != nil {
		return err
	}

	task, exist, err := model.GetByOnlyTaskId(snap.ID)
	if err != nil {
		return fmt.Errorf("lookup task %s failed: %w", snap.ID, err)
	}
	if !exist || task == nil {
		return fmt.Errorf("task %s not found", snap.ID)
	}

	// fromStatus is the DB status at fetch time; the CAS below transitions FROM it.
	fromStatus := task.Status

	// Already terminal (e.g. the poller settled first): nothing to do. This is the
	// first line of the double-settlement defense; the CAS is the second.
	if fromStatus == model.TaskStatusSuccess || fromStatus == model.TaskStatusFailure {
		logger.LogInfo(ctx, fmt.Sprintf("task callback: task %s already terminal (%s), skip", task.TaskID, fromStatus))
		return nil
	}

	now := time.Now().Unix()
	shouldSettle := false
	shouldRefund := false
	quota := task.Quota

	switch taskResult.Status {
	case string(model.TaskStatusQueued):
		task.Status = model.TaskStatusQueued
		task.Progress = taskcommon.ProgressQueued
	case string(model.TaskStatusInProgress):
		task.Status = model.TaskStatusInProgress
		task.Progress = taskcommon.ProgressInProgress
		if task.StartTime == 0 {
			task.StartTime = now
		}
	case string(model.TaskStatusSuccess):
		task.Status = model.TaskStatusSuccess
		task.Progress = taskcommon.ProgressComplete
		if task.FinishTime == 0 {
			task.FinishTime = now
		}
		if strings.HasPrefix(taskResult.Url, "data:") {
			// data: URI — keep in Data, expose via proxy URL
			task.PrivateData.ResultURL = taskcommon.BuildProxyURL(task.TaskID)
		} else if taskResult.Url != "" {
			task.PrivateData.ResultURL = taskResult.Url
		} else {
			task.PrivateData.ResultURL = taskcommon.BuildProxyURL(task.TaskID)
		}
		shouldSettle = true
	case string(model.TaskStatusFailure):
		task.Status = model.TaskStatusFailure
		task.Progress = taskcommon.ProgressComplete
		if task.FinishTime == 0 {
			task.FinishTime = now
		}
		task.FailReason = taskResult.Reason
		if quota != 0 {
			shouldRefund = true
		}
	default:
		// Unknown/unhandled status — ignore (keep waiting for a terminal push/poll).
		return nil
	}

	isDone := task.Status == model.TaskStatusSuccess || task.Status == model.TaskStatusFailure
	if !isDone {
		// Non-terminal progress update: opportunistic, CAS-guarded, no billing.
		if task.Status != fromStatus {
			if _, err := task.UpdateWithStatus(fromStatus); err != nil {
				logger.LogWarn(ctx, fmt.Sprintf("task callback: non-terminal update failed for %s: %s", task.TaskID, err.Error()))
			}
		}
		return nil
	}

	// Terminal transition: CAS guards against double-settlement with the poller.
	won, err := task.UpdateWithStatus(fromStatus)
	if err != nil {
		return fmt.Errorf("CAS update failed for task %s: %w", task.TaskID, err)
	}
	if !won {
		logger.LogInfo(ctx, fmt.Sprintf("task callback: task %s already transitioned by another process, skip billing", task.TaskID))
		return nil
	}

	if shouldSettle {
		// Reuse the poller's settlement path. It needs the adaptor for
		// AdjustBillingOnComplete; chatgpt2api uses BaseBilling (returns 0) and
		// therefore falls through to token-based recalculation.
		var adaptor TaskPollingAdaptor
		if GetTaskAdaptorFunc != nil {
			adaptor = GetTaskAdaptorFunc(task.Platform)
		}
		if adaptor != nil {
			settleTaskBillingOnComplete(ctx, adaptor, task, taskResult)
		} else if taskResult.TotalTokens > 0 {
			// Defensive: no adaptor registered (should not happen in prod). Fall
			// back to the same token recalculation settleTaskBillingOnComplete uses.
			RecalculateTaskQuotaByTokens(ctx, task, taskResult.TotalTokens)
		}
	}
	if shouldRefund {
		RefundTaskQuota(ctx, task, task.FailReason)
	}
	return nil
}
