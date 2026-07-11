package chatgpt2api

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel"
	taskcommon "github.com/QuantumNous/new-api/relay/channel/task/taskcommon"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
)

// ============================
// Upstream (chatgpt2api) request / response structures
//
// Submit:  POST {base}/api/image-tasks/generations
//   body  {client_task_id, prompt, model, size, quality, callback_url?}
//   resp  200 {id, status:"queued"|"running", ...}
//   NOTE: chatgpt2api is idempotent on client_task_id and echoes it back as `id`,
//         so we set client_task_id = info.PublicTaskID (upstream id == public id).
//
// Poll:    GET {base}/api/image-tasks?ids={task_id}
//   resp  {items:[{id, status, data:[{url,revised_prompt}], usage:{total_tokens,...},
//          error, progress}], missing_ids:[]}
//   status ∈ queued|running|success|error
// ============================

// submitResponse is the response of the generations submit endpoint.
type submitResponse struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// pollItem is a single item of the poll response.
// NOTE: chatgpt2api reports `progress` as a STRING step-name (e.g. "getting_account",
// "image_stream_resolve_start"), NOT a numeric percentage — so it is typed as string
// and passed through verbatim for display.
type pollItem struct {
	ID       string      `json:"id"`
	Status   string      `json:"status"`
	Progress string      `json:"progress,omitempty"`
	Data     []pollImage `json:"data,omitempty"`
	Usage    *pollUsage  `json:"usage,omitempty"`
	Error    string      `json:"error,omitempty"`
}

type pollImage struct {
	URL           string `json:"url"`
	RevisedPrompt string `json:"revised_prompt,omitempty"`
}

type pollUsage struct {
	TotalTokens int `json:"total_tokens,omitempty"`
}

// pollResponse is the response of the poll endpoint.
type pollResponse struct {
	Items      []pollItem `json:"items"`
	MissingIDs []string   `json:"missing_ids,omitempty"`
}

// ============================
// Adaptor implementation
// ============================

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

// ValidateRequestAndSetAction parses the JSON body into a TaskSubmitReq, requires
// a non-empty prompt, and stashes the parsed request in the gin context. The
// generations submit endpoint is JSON (not multipart).
func (a *TaskAdaptor) ValidateRequestAndSetAction(c *gin.Context, info *relaycommon.RelayInfo) (taskErr *dto.TaskError) {
	var req relaycommon.TaskSubmitReq
	if err := common.UnmarshalBodyReusable(c, &req); err != nil {
		return service.TaskErrorWrapperLocal(err, "invalid_request", http.StatusBadRequest)
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return service.TaskErrorWrapperLocal(fmt.Errorf("field prompt is required"), "invalid_request", http.StatusBadRequest)
	}
	c.Set("task_request", req)
	return nil
}

// EstimateBilling returns OtherRatios for pre-charge based on the user request.
//
// FIRST CUT: return nil so the task is billed at the flat base model price.
//
// TODO(pricing): to align with the sync-side tiered image pricing, extract size /
// quality from the parsed request and return them as OtherRatios multipliers, e.g.:
//
//	req, err := relaycommon.GetTaskRequest(c)
//	if err != nil {
//	    return nil
//	}
//	ratios := map[string]float64{"size": 1, "quality": 1}
//	switch req.Size {                 // mirror sora's size logic
//	case "1024x1536", "1536x1024":
//	    ratios["size"] = 1.5
//	case "2048x2048":
//	    ratios["size"] = 2
//	}
//	// quality (standard|hd|high|...) multipliers would be applied here too.
//	return ratios
//
// The multipliers are then folded into the pre-charge quota by RelayTaskSubmit and
// (optionally) re-settled on completion. Keep them consistent with the numbers used
// by the synchronous /v1/images/generations path.
func (a *TaskAdaptor) EstimateBilling(c *gin.Context, info *relaycommon.RelayInfo) map[string]float64 {
	return nil
}

// BuildRequestURL constructs the upstream submit URL.
func (a *TaskAdaptor) BuildRequestURL(info *relaycommon.RelayInfo) (string, error) {
	return fmt.Sprintf("%s/api/image-tasks/generations", a.baseURL), nil
}

// BuildRequestHeader sets required headers.
func (a *TaskAdaptor) BuildRequestHeader(c *gin.Context, req *http.Request, info *relaycommon.RelayInfo) error {
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	req.Header.Set("Content-Type", "application/json")
	return nil
}

// BuildRequestBody reads the cached client body, injects client_task_id (= public
// task id, upstream is idempotent on it) and the mapped upstream model, keeps
// prompt/size/quality, and drops fields the async task API does not accept
// (n / response_format).
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
	if err := common.Unmarshal(cachedBody, &bodyMap); err != nil {
		return nil, errors.Wrap(err, "unmarshal_request_body_failed")
	}
	if bodyMap == nil {
		bodyMap = map[string]interface{}{}
	}

	// upstream id == public id (chatgpt2api is idempotent on client_task_id)
	bodyMap["client_task_id"] = info.PublicTaskID
	bodyMap["model"] = info.UpstreamModelName

	// drop fields the async generations API does not accept
	delete(bodyMap, "n")
	delete(bodyMap, "response_format")

	newBody, err := common.Marshal(bodyMap)
	if err != nil {
		return nil, errors.Wrap(err, "marshal_request_body_failed")
	}
	return bytes.NewReader(newBody), nil
}

// DoRequest delegates to the common task request helper.
func (a *TaskAdaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (*http.Response, error) {
	return channel.DoTaskApiRequest(a, c, info, requestBody)
}

// DoResponse parses the submit response and returns the upstream task id.
func (a *TaskAdaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (taskID string, taskData []byte, taskErr *dto.TaskError) {
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		taskErr = service.TaskErrorWrapper(err, "read_response_body_failed", http.StatusInternalServerError)
		return
	}
	_ = resp.Body.Close()

	var sResp submitResponse
	if err := common.Unmarshal(responseBody, &sResp); err != nil {
		taskErr = service.TaskErrorWrapper(errors.Wrapf(err, "body: %s", responseBody), "unmarshal_response_body_failed", http.StatusInternalServerError)
		return
	}

	upstreamID := sResp.ID
	if upstreamID == "" {
		taskErr = service.TaskErrorWrapper(fmt.Errorf("task id is empty"), "invalid_response", http.StatusInternalServerError)
		return
	}

	// return the public task id to the client (never expose the upstream id)
	c.JSON(http.StatusOK, gin.H{
		"task_id": info.PublicTaskID,
		"status":  sResp.Status,
	})
	return upstreamID, responseBody, nil
}

// FetchTask polls the upstream for the current status of a task.
func (a *TaskAdaptor) FetchTask(baseUrl, key string, body map[string]any, proxy string) (*http.Response, error) {
	taskID, ok := body["task_id"].(string)
	if !ok {
		return nil, fmt.Errorf("invalid task_id")
	}

	uri := fmt.Sprintf("%s/api/image-tasks?ids=%s", baseUrl, taskID)

	req, err := http.NewRequest(http.MethodGet, uri, nil)
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

func (a *TaskAdaptor) GetModelList() []string {
	return ModelList
}

func (a *TaskAdaptor) GetChannelName() string {
	return ChannelName
}

// ParseTaskResult converts an upstream poll response into a generic TaskInfo.
func (a *TaskAdaptor) ParseTaskResult(respBody []byte) (*relaycommon.TaskInfo, error) {
	var resp pollResponse
	if err := common.Unmarshal(respBody, &resp); err != nil {
		return nil, errors.Wrap(err, "unmarshal task result failed")
	}

	taskResult := &relaycommon.TaskInfo{Code: 0}

	// No item yet — the task is not visible upstream, treat as still running.
	if len(resp.Items) == 0 {
		taskResult.Status = model.TaskStatusInProgress
		return taskResult, nil
	}

	item := resp.Items[0]
	switch item.Status {
	case "queued":
		taskResult.Status = model.TaskStatusQueued
	case "running":
		taskResult.Status = model.TaskStatusInProgress
	case "success":
		taskResult.Status = model.TaskStatusSuccess
		if len(item.Data) > 0 {
			taskResult.Url = item.Data[0].URL
		}
		if item.Usage != nil && item.Usage.TotalTokens > 0 {
			taskResult.TotalTokens = item.Usage.TotalTokens
			taskResult.CompletionTokens = item.Usage.TotalTokens
		}
	case "error":
		taskResult.Status = model.TaskStatusFailure
		if item.Error != "" {
			taskResult.Reason = item.Error
		} else {
			taskResult.Reason = "task failed"
		}
	default:
		// unknown status — keep polling
		taskResult.Status = model.TaskStatusInProgress
	}

	// progress is an upstream step-name string (not a percentage) — pass it through.
	if item.Progress != "" {
		taskResult.Progress = item.Progress
	}

	return taskResult, nil
}
