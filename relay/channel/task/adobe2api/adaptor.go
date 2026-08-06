// Package adobe2api relays video-generation tasks to an adobe2api backend
// (Adobe Firefly pool) over the OpenAI Videos API shape.
//
// It is deliberately a separate adaptor rather than a reuse of the Sora one:
// the wire protocol is identical, but Sora's EstimateBilling hardcodes a size
// ratio table for OpenAI's geometries (only 1792x1024 gets a bump).  Firefly
// prices move with BOTH resolution and audio, so billing has to be derived
// from the request parameters — see billing.go.
package adobe2api

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel"
	taskcommon "github.com/QuantumNous/new-api/relay/channel/task/taskcommon"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// responseTask mirrors the OpenAI Videos task object adobe2api returns.
type responseTask struct {
	ID          string `json:"id"`
	TaskID      string `json:"task_id,omitempty"`
	Object      string `json:"object"`
	Model       string `json:"model"`
	Status      string `json:"status"`
	Progress    int    `json:"progress"`
	CreatedAt   int64  `json:"created_at"`
	CompletedAt int64  `json:"completed_at,omitempty"`
	Seconds     string `json:"seconds,omitempty"`
	Size        string `json:"size,omitempty"`
	Error       *struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	} `json:"error,omitempty"`
}

type TaskAdaptor struct {
	taskcommon.BaseBilling
	ChannelType int
	apiKey      string
	baseURL     string
}

func (a *TaskAdaptor) Init(info *relaycommon.RelayInfo) {
	a.ChannelType = info.ChannelType
	a.baseURL = info.ChannelBaseUrl
	a.apiKey = info.ApiKey
}

func (a *TaskAdaptor) ValidateRequestAndSetAction(c *gin.Context, info *relaycommon.RelayInfo) (taskErr *dto.TaskError) {
	return relaycommon.ValidateMultipartDirect(c, info)
}

// EstimateBilling turns the request parameters into ratio multipliers.
//
//	seconds — linear in duration (Adobe charges per second, no base fee)
//	tier    — resolution/audio multiplier over the model's cheapest tier
//
// Both are logged back to the user as "计算参数：seconds: N, tier: R".
func (a *TaskAdaptor) EstimateBilling(c *gin.Context, info *relaycommon.RelayInfo) map[string]float64 {
	req, err := relaycommon.GetTaskRequest(c)
	if err != nil {
		return nil
	}
	modelName := info.OriginModelName
	if modelName == "" {
		modelName = req.Model
	}

	// A source clip turns a kling request into video-to-video, which is priced
	// differently -- read the raw body so a top-level `video_url` (not a
	// TaskSubmitReq field, but passed through verbatim) still bills correctly.
	var rawBody []byte
	if storage, err := common.GetBodyStorage(c); err == nil {
		if b, err := storage.Bytes(); err == nil {
			rawBody = b
		}
	}
	hasVideo := strings.TrimSpace(req.InputReference) != "" ||
		hasSourceVideo(rawBody, req.Metadata)

	seconds := resolveSeconds(modelName, req.Seconds, req.Duration, hasVideo)
	return map[string]float64{
		"seconds": float64(seconds),
		"tier":    tierRatio(modelName, req.Size, wantsAudio(req.Metadata), hasVideo),
	}
}

func (a *TaskAdaptor) BuildRequestURL(info *relaycommon.RelayInfo) (string, error) {
	return fmt.Sprintf("%s/v1/videos", a.baseURL), nil
}

func (a *TaskAdaptor) BuildRequestHeader(c *gin.Context, req *http.Request, info *relaycommon.RelayInfo) error {
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	req.Header.Set("Content-Type", "application/json")
	return nil
}

// BuildRequestBody passes the caller's body through untouched apart from the
// model name, so every adobe2api-specific knob (shots, video_url,
// negative_prompt, ...) survives the relay.
func (a *TaskAdaptor) BuildRequestBody(c *gin.Context, info *relaycommon.RelayInfo) (io.Reader, error) {
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return nil, errors.Wrap(err, "get_request_body_failed")
	}
	cachedBody, err := storage.Bytes()
	if err != nil {
		return nil, errors.Wrap(err, "read_body_bytes_failed")
	}
	var bodyMap map[string]interface{}
	if err := common.Unmarshal(cachedBody, &bodyMap); err == nil {
		bodyMap["model"] = info.UpstreamModelName
		if newBody, err := common.Marshal(bodyMap); err == nil {
			return bytes.NewReader(newBody), nil
		}
	}
	return bytes.NewReader(cachedBody), nil
}

func (a *TaskAdaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (*http.Response, error) {
	return channel.DoTaskApiRequest(a, c, info, requestBody)
}

func (a *TaskAdaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (taskID string, taskData []byte, taskErr *dto.TaskError) {
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		taskErr = service.TaskErrorWrapper(err, "read_response_body_failed", http.StatusInternalServerError)
		return
	}
	_ = resp.Body.Close()

	var dResp responseTask
	if err := common.Unmarshal(responseBody, &dResp); err != nil {
		taskErr = service.TaskErrorWrapper(errors.Wrapf(err, "body: %s", responseBody),
			"unmarshal_response_body_failed", http.StatusInternalServerError)
		return
	}

	upstreamID := dResp.ID
	if upstreamID == "" {
		upstreamID = dResp.TaskID
	}
	if upstreamID == "" {
		taskErr = service.TaskErrorWrapper(fmt.Errorf("task_id is empty"),
			"invalid_response", http.StatusInternalServerError)
		return
	}

	// hand the client our public task id, never the upstream one
	dResp.ID = info.PublicTaskID
	dResp.TaskID = info.PublicTaskID
	c.JSON(http.StatusOK, dResp)
	return upstreamID, responseBody, nil
}

func (a *TaskAdaptor) FetchTask(baseUrl, key string, body map[string]any, proxy string) (*http.Response, error) {
	taskID, ok := body["task_id"].(string)
	if !ok {
		return nil, fmt.Errorf("invalid task_id")
	}
	req, err := http.NewRequest(http.MethodGet,
		fmt.Sprintf("%s/v1/videos/%s", baseUrl, taskID), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)

	client, err := service.GetHttpClientWithProxy(proxy)
	if err != nil {
		return nil, fmt.Errorf("new proxy http client failed: %w", err)
	}
	return client.Do(req)
}

func (a *TaskAdaptor) GetModelList() []string { return ModelList }

func (a *TaskAdaptor) GetChannelName() string { return ChannelName }

func (a *TaskAdaptor) ParseTaskResult(respBody []byte) (*relaycommon.TaskInfo, error) {
	resTask := responseTask{}
	if err := common.Unmarshal(respBody, &resTask); err != nil {
		return nil, errors.Wrap(err, "unmarshal task result failed")
	}

	taskResult := relaycommon.TaskInfo{Code: 0}
	switch strings.ToLower(resTask.Status) {
	case "queued", "pending":
		taskResult.Status = model.TaskStatusQueued
	case "processing", "in_progress", "running":
		taskResult.Status = model.TaskStatusInProgress
	case "completed", "succeeded":
		taskResult.Status = model.TaskStatusSuccess
		// Url stays empty on purpose — the caller builds the /content proxy URL
	case "failed", "cancelled":
		taskResult.Status = model.TaskStatusFailure
		if resTask.Error != nil {
			taskResult.Reason = resTask.Error.Message
		} else {
			taskResult.Reason = "task failed"
		}
	}
	if resTask.Progress > 0 && resTask.Progress < 100 {
		taskResult.Progress = fmt.Sprintf("%d%%", resTask.Progress)
	}
	return &taskResult, nil
}

// ConvertToOpenAIVideo rewrites upstream identifiers so nothing about the
// backend leaks to the client.
func (a *TaskAdaptor) ConvertToOpenAIVideo(task *model.Task) ([]byte, error) {
	data := task.Data
	var err error
	if data, err = sjson.SetBytes(data, "id", task.TaskID); err != nil {
		return nil, errors.Wrap(err, "set id failed")
	}
	if gjson.GetBytes(data, "task_id").Exists() {
		if data, err = sjson.SetBytes(data, "task_id", task.TaskID); err != nil {
			return nil, errors.Wrap(err, "set task_id failed")
		}
	}
	proxyURL := taskcommon.BuildProxyURL(task.TaskID)
	for _, path := range []string{"video_url", "metadata.url"} {
		if gjson.GetBytes(data, path).Exists() {
			if data, err = sjson.SetBytes(data, path, proxyURL); err != nil {
				return nil, errors.Wrapf(err, "set %s failed", path)
			}
		}
	}
	if gjson.GetBytes(data, "download_url").Exists() {
		if data, err = sjson.SetBytes(data, "download_url",
			fmt.Sprintf("/v1/videos/%s/content", task.TaskID)); err != nil {
			return nil, errors.Wrap(err, "set download_url failed")
		}
	}
	// restore the customer-facing model name (channel-level mapping may have
	// rewritten it on the way up)
	if origin := task.Properties.OriginModelName; origin != "" && gjson.GetBytes(data, "model").Exists() {
		if data, err = sjson.SetBytes(data, "model", origin); err != nil {
			return nil, errors.Wrap(err, "set model failed")
		}
	}
	return data, nil
}

var _ = constant.TaskActionGenerate
