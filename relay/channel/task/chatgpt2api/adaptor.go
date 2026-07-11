package chatgpt2api

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
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
	// isEdits records whether this submit targets the image EDITS endpoint
	// (/v1/images/async/edits) rather than generations. It is resolved once in
	// ValidateRequestAndSetAction (which runs before BuildRequestURL /
	// BuildRequestBody / BuildRequestHeader) and reused by those methods, which
	// do not all receive the gin context.
	isEdits bool
}

func (a *TaskAdaptor) Init(info *relaycommon.RelayInfo) {
	a.ChannelType = info.ChannelType
	a.baseURL = info.ChannelBaseUrl
	a.apiKey = info.ApiKey
}

// isEditsRequest reports whether the current async image request targets the
// edits endpoint (POST /v1/images/async/edits) rather than generations. The
// distributor sets image_async_op="edits" for that path; we also fall back to a
// path-suffix check so the adaptor stays correct even if the flag is absent.
func isEditsRequest(c *gin.Context) bool {
	if c == nil {
		return false
	}
	if c.GetString("image_async_op") == "edits" {
		return true
	}
	if c.Request != nil && c.Request.URL != nil {
		return strings.HasSuffix(c.Request.URL.Path, "/edits")
	}
	return false
}

// ValidateRequestAndSetAction parses the JSON body into a TaskSubmitReq, requires
// a non-empty prompt, and stashes the parsed request in the gin context. The
// generations submit endpoint is JSON (not multipart).
func (a *TaskAdaptor) ValidateRequestAndSetAction(c *gin.Context, info *relaycommon.RelayInfo) (taskErr *dto.TaskError) {
	a.isEdits = isEditsRequest(c)
	if a.isEdits {
		if taskErr := a.validateEditsRequest(c); taskErr != nil {
			return taskErr
		}
		// gpt-image-2 uses tiered_expr billing keyed off param("size"), but the
		// billing request-input reader (readIncomingBillingExprBody) only parses
		// JSON bodies — a multipart edits body is invisible to it. Surface the
		// parsed size to the expression via a synthesized JSON billing body so an
		// edits request is priced by its real size (generations already expose
		// size through the real JSON body and are left untouched).
		a.injectBillingSizeForEdits(c, info)
		return nil
	}
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

// validateEditsRequest parses the multipart/form-data edits submission, requires
// a non-empty prompt and at least one input image file part, and stashes the
// parsed scalar request in the gin context. The image bytes are NOT copied here
// — BuildRequestBody re-parses the reusable body storage on (re)submit.
func (a *TaskAdaptor) validateEditsRequest(c *gin.Context) *dto.TaskError {
	form, err := common.ParseMultipartFormReusable(c)
	if err != nil {
		return service.TaskErrorWrapperLocal(err, "invalid_request", http.StatusBadRequest)
	}

	prompt := ""
	if vals := form.Value["prompt"]; len(vals) > 0 {
		prompt = vals[0]
	}
	if strings.TrimSpace(prompt) == "" {
		return service.TaskErrorWrapperLocal(fmt.Errorf("field prompt is required"), "invalid_request", http.StatusBadRequest)
	}
	if !hasImageFilePart(form) {
		return service.TaskErrorWrapperLocal(fmt.Errorf("field image is required"), "invalid_request", http.StatusBadRequest)
	}

	req := relaycommon.TaskSubmitReq{Prompt: prompt}
	if vals := form.Value["model"]; len(vals) > 0 {
		req.Model = vals[0]
	}
	if vals := form.Value["size"]; len(vals) > 0 {
		req.Size = vals[0]
	}
	c.Set("task_request", req)
	return nil
}

// injectBillingSizeForEdits makes the multipart edits "size" discoverable to the
// tiered billing expression's param("size"). It stores a minimal synthesized
// JSON billing body ({"size":"<size>"}) on info.BillingRequestInput, which
// ResolveIncomingBillingExprRequestInput consumes in preference to reading the
// (multipart, hence unreadable) request body. Only the size is needed — the
// gpt-image-2 expression references no other request param. A missing size
// yields {"size":""}, matching the expression's empty-size branch.
func (a *TaskAdaptor) injectBillingSizeForEdits(c *gin.Context, info *relaycommon.RelayInfo) {
	if info == nil {
		return
	}
	size := ""
	if req, err := relaycommon.GetTaskRequest(c); err == nil {
		size = req.Size
	}
	body, err := common.Marshal(map[string]string{"size": size})
	if err != nil {
		return
	}
	info.BillingRequestInput = &billingexpr.RequestInput{Body: body}
}

// collectImageFiles returns the input-image file headers from a parsed edits
// form, checking (in order) the standard "image" field, the "image[]" array
// field, and any "image[...]"-prefixed field (multi-image edits). Client field
// names are preserved on rebuild — the upstream edits endpoint accepts them.
func collectImageFiles(form *multipart.Form) []*multipart.FileHeader {
	if form == nil || form.File == nil {
		return nil
	}
	if files := form.File["image"]; len(files) > 0 {
		return files
	}
	if files := form.File["image[]"]; len(files) > 0 {
		return files
	}
	var out []*multipart.FileHeader
	for name, files := range form.File {
		if strings.HasPrefix(name, "image[") && len(files) > 0 {
			out = append(out, files...)
		}
	}
	return out
}

func hasImageFilePart(form *multipart.Form) bool {
	return len(collectImageFiles(form)) > 0
}

// EstimateBilling returns OtherRatios for pre-charge based on the user request.
//
// gpt-image-2 is priced via the tiered-expression billing system (billing_mode
// tiered_expr), which keys off param("size") and the x-user-group header — not
// off OtherRatio multipliers. That evaluation happens inside
// ModelPriceHelperPerCall (which delegates to modelPriceHelperTiered for
// tiered_expr models), so there are no extra ratios to fold in here.
func (a *TaskAdaptor) EstimateBilling(c *gin.Context, info *relaycommon.RelayInfo) map[string]float64 {
	return nil
}

// BuildRequestURL constructs the upstream submit URL. Edits target the dedicated
// image-tasks/edits endpoint; generations keep the original path.
func (a *TaskAdaptor) BuildRequestURL(info *relaycommon.RelayInfo) (string, error) {
	if a.isEdits {
		return fmt.Sprintf("%s/api/image-tasks/edits", a.baseURL), nil
	}
	return fmt.Sprintf("%s/api/image-tasks/generations", a.baseURL), nil
}

// BuildRequestHeader sets required headers. Generations send JSON; edits send the
// rebuilt multipart body, whose boundary Content-Type was stashed on the context
// by BuildRequestBody.
func (a *TaskAdaptor) BuildRequestHeader(c *gin.Context, req *http.Request, info *relaycommon.RelayInfo) error {
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	if a.isEdits {
		if ct := c.GetString("image_async_ct"); ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		return nil
	}
	req.Header.Set("Content-Type", "application/json")
	return nil
}

// BuildRequestBody reads the cached client body, injects client_task_id (= public
// task id, upstream is idempotent on it) and the mapped upstream model, keeps
// prompt/size/quality, and drops fields the async task API does not accept
// (n / response_format).
func (a *TaskAdaptor) BuildRequestBody(c *gin.Context, info *relaycommon.RelayInfo) (io.Reader, error) {
	if a.isEdits {
		return a.buildEditsRequestBody(c, info)
	}
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

	// Push (webhook) completion: tell chatgpt2api where to POST the terminal task
	// snapshot. The secret rides in the URL path and is verified constant-time by
	// the receiver (POST /api/task-callback/:secret). If no base URL is configured
	// we omit callback_url entirely — pure polling still works (backward compatible).
	if base := constant.TaskCallbackBaseURL; base != "" {
		bodyMap["callback_url"] = fmt.Sprintf("%s/api/task-callback/%s", base, url.PathEscape(constant.TaskCallbackSecret))
	}

	// drop fields the async generations API does not accept
	delete(bodyMap, "n")
	delete(bodyMap, "response_format")

	newBody, err := common.Marshal(bodyMap)
	if err != nil {
		return nil, errors.Wrap(err, "marshal_request_body_failed")
	}
	return bytes.NewReader(newBody), nil
}

// buildEditsRequestBody rebuilds the multipart/form-data body for an async image
// edits submit. It re-parses the cached client body (retry-safe: the source is
// the reusable body storage, not a one-shot reader), copies the input image
// (image / image[] / any image[...] field) and optional mask (mask / mask[])
// file parts preserving the client's OpenAI-standard field names, forwards the
// prompt/size/quality (and any other) value fields, and injects the task fields
// the upstream needs (client_task_id, mapped model, callback_url). n and
// response_format are dropped (the upstream forces response_format=url). The
// generated boundary Content-Type is stashed on the context for
// BuildRequestHeader.
func (a *TaskAdaptor) buildEditsRequestBody(c *gin.Context, info *relaycommon.RelayInfo) (io.Reader, error) {
	form, err := common.ParseMultipartFormReusable(c)
	if err != nil {
		return nil, errors.Wrap(err, "parse_multipart_form_failed")
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	// Value fields we inject/override or deliberately drop.
	skipValues := map[string]bool{
		"model":           true, // injected below (mapped upstream model)
		"client_task_id":  true, // injected below
		"callback_url":    true, // injected below
		"n":               true, // dropped: async API does not accept
		"response_format": true, // dropped: upstream forces response_format=url
	}
	for key, values := range form.Value {
		if skipValues[key] {
			continue
		}
		for _, v := range values {
			if err := writer.WriteField(key, v); err != nil {
				return nil, errors.Wrap(err, "write_form_field_failed")
			}
		}
	}

	// Input image parts (required; validated in ValidateRequestAndSetAction).
	imageFiles := collectImageFiles(form)
	imageFieldName := "image"
	if len(imageFiles) > 1 {
		imageFieldName = "image[]"
	}
	for i, fh := range imageFiles {
		if err := copyFilePart(writer, imageFieldName, fh); err != nil {
			return nil, errors.Wrapf(err, "copy_image_part_failed_%d", i)
		}
	}

	// Optional mask parts (mask / mask[]).
	if err := copyMaskParts(writer, form); err != nil {
		return nil, err
	}

	// Inject task fields.
	if err := writer.WriteField("client_task_id", info.PublicTaskID); err != nil {
		return nil, errors.Wrap(err, "write_client_task_id_failed")
	}
	if err := writer.WriteField("model", info.UpstreamModelName); err != nil {
		return nil, errors.Wrap(err, "write_model_failed")
	}
	if base := constant.TaskCallbackBaseURL; base != "" {
		callbackURL := fmt.Sprintf("%s/api/task-callback/%s", base, url.PathEscape(constant.TaskCallbackSecret))
		if err := writer.WriteField("callback_url", callbackURL); err != nil {
			return nil, errors.Wrap(err, "write_callback_url_failed")
		}
	}

	if err := writer.Close(); err != nil {
		return nil, errors.Wrap(err, "close_multipart_writer_failed")
	}

	c.Set("image_async_ct", writer.FormDataContentType())
	return &body, nil
}

// copyFilePart copies a single uploaded file into the multipart writer under the
// given field name, setting a Content-Type derived from the filename extension.
func copyFilePart(writer *multipart.Writer, fieldName string, fh *multipart.FileHeader) error {
	f, err := fh.Open()
	if err != nil {
		return fmt.Errorf("open file failed: %w", err)
	}
	defer f.Close()

	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, fieldName, fh.Filename))
	h.Set("Content-Type", detectImageMimeType(fh.Filename))
	part, err := writer.CreatePart(h)
	if err != nil {
		return fmt.Errorf("create form part failed: %w", err)
	}
	if _, err := io.Copy(part, f); err != nil {
		return fmt.Errorf("copy file failed: %w", err)
	}
	return nil
}

// copyMaskParts copies the optional mask file(s) (mask / mask[]) into the writer,
// preserving the client field name.
func copyMaskParts(writer *multipart.Writer, form *multipart.Form) error {
	if form == nil || form.File == nil {
		return nil
	}
	maskFiles := form.File["mask"]
	fieldName := "mask"
	if len(maskFiles) == 0 {
		maskFiles = form.File["mask[]"]
		if len(maskFiles) > 0 {
			fieldName = "mask[]"
		}
	}
	if len(maskFiles) > 1 {
		fieldName = "mask[]"
	}
	for i, fh := range maskFiles {
		if err := copyFilePart(writer, fieldName, fh); err != nil {
			return errors.Wrapf(err, "copy_mask_part_failed_%d", i)
		}
	}
	return nil
}

// detectImageMimeType determines a MIME type from the file extension, defaulting
// to image/png (mirrors the synchronous image-edits path in relay/channel/openai).
func detectImageMimeType(filename string) string {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	default:
		return "image/png"
	}
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
