// Package secureskill 适配 secure-skill（token.secure-skill.com）的视频接口。
//
// 它是特价组唯一与 meaicc 独立的 ≥720p 按次来源，因此承担"异源兜底"角色：
// meaicc 整站漂移时由它接住。协议上有三处非常规设计：
//
//  1. 端点是 POST /api/generate-video，**必须 multipart/form-data**，
//     但参考素材要以 http(s) URL 字符串放在 files 字段里 —— 传真文件会被拒
//     （"file uploads are not supported"）。要 multipart 却不收文件。
//  2. 轮询 GET /api/video/{task_id}：未完成返回 **HTTP 409**，
//     完成后**直接返回 mp4 二进制**，没有 JSON、没有成片 URL。
//  3. 因为拿不到长期直链，成片由 /v1/videos/{id}/content 代理按需回源
//     （见 controller/video_proxy.go 的 SecureSkill 分支）。
package secureskill

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
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
	"github.com/tidwall/gjson"
)

type submitResponse struct {
	ID       string `json:"id"`
	TaskID   string `json:"task_id"`
	Object   string `json:"object"`
	Model    string `json:"model"`
	Status   string `json:"status"`
	Progress int    `json:"progress"`
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

func (a *TaskAdaptor) GetModelList() []string { return ModelList }
func (a *TaskAdaptor) GetChannelName() string { return ChannelName }

func resolveDuration(req *relaycommon.TaskSubmitReq) int {
	if req.Duration > 0 {
		return req.Duration
	}
	if s := strings.TrimSpace(req.Seconds); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			return n
		}
	}
	return 5
}

func (a *TaskAdaptor) ValidateRequestAndSetAction(c *gin.Context, info *relaycommon.RelayInfo) *dto.TaskError {
	var req relaycommon.TaskSubmitReq
	if err := common.UnmarshalBodyReusable(c, &req); err != nil {
		return service.TaskErrorWrapperLocal(err, "invalid_request", http.StatusBadRequest)
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return service.TaskErrorWrapperLocal(errors.New("field prompt is required"),
			"invalid_request", http.StatusBadRequest)
	}
	d := resolveDuration(&req)
	if d < minDuration || d > maxDuration {
		return service.TaskErrorWrapperLocal(
			fmt.Errorf("duration must be between %d and %d seconds", minDuration, maxDuration),
			"invalid_request", http.StatusBadRequest)
	}
	media := collectMediaURLs(c, &req)
	// 该上游的 files 字段**强制必填**（不传/传空串均报 "files is required"），
	// 即它只做图生视频/参考生视频，**不支持纯文生**。这里提前拦下，避免纯文生请求
	// 一路打到上游才拿到看不懂的报错；同时 LocalError 会中止后续重试（重试也没用）。
	if len(media) == 0 {
		return service.TaskErrorWrapperLocal(
			errors.New("this model requires at least one reference material (image/video/audio); "+
				"for text-to-video use the per-second video models"),
			"invalid_request", http.StatusBadRequest)
	}
	for _, u := range media {
		if strings.HasPrefix(u, "data:") {
			return service.TaskErrorWrapperLocal(
				errors.New("reference materials must be public http(s) URLs; base64 is not supported by this model"),
				"invalid_request", http.StatusBadRequest)
		}
	}
	c.Set("task_request", req)
	return nil
}

func rawBody(c *gin.Context) []byte {
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return nil
	}
	b, err := storage.Bytes()
	if err != nil {
		return nil
	}
	return b
}

// collectMediaURLs 收集参考素材 URL。对外契约与 meaicc adaptor 保持一致，
// 这样同一份用户请求可以在两家之间 failover 而不用改写。
func collectMediaURLs(c *gin.Context, req *relaycommon.TaskSubmitReq) []string {
	var out []string
	if body := rawBody(c); len(body) > 0 {
		for _, p := range []string{"first_frame_url", "last_frame_url"} {
			if v := gjson.GetBytes(body, p); v.Exists() && v.String() != "" {
				out = append(out, v.String())
			}
		}
		for _, p := range []string{"reference_image_urls", "reference_video_urls", "reference_audio_urls"} {
			for _, v := range gjson.GetBytes(body, p).Array() {
				if s := strings.TrimSpace(v.String()); s != "" {
					out = append(out, s)
				}
			}
		}
	}
	if len(out) == 0 {
		for _, s := range req.Images {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		if s := strings.TrimSpace(req.Image); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func ratioFromRequest(c *gin.Context) string {
	if body := rawBody(c); len(body) > 0 {
		for _, p := range []string{"ratio", "aspect_ratio"} {
			if v := gjson.GetBytes(body, p); v.Exists() && v.String() != "" {
				return v.String()
			}
		}
	}
	return "16:9"
}

func (a *TaskAdaptor) BuildRequestURL(info *relaycommon.RelayInfo) (string, error) {
	return fmt.Sprintf("%s/api/generate-video", a.baseURL), nil
}

// BuildRequestHeader 不设 Content-Type —— multipart 的 boundary 由
// BuildRequestBody 写进 c.Request.Header，DoTaskApiRequest 会沿用它。
func (a *TaskAdaptor) BuildRequestHeader(c *gin.Context, req *http.Request, info *relaycommon.RelayInfo) error {
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	req.Header.Set("Content-Type", c.Request.Header.Get("Content-Type"))
	return nil
}

func (a *TaskAdaptor) BuildRequestBody(c *gin.Context, info *relaycommon.RelayInfo) (io.Reader, error) {
	reqAny, ok := c.Get("task_request")
	if !ok {
		return nil, errors.New("task_request not found in context")
	}
	req, ok := reqAny.(relaycommon.TaskSubmitReq)
	if !ok {
		return nil, errors.New("task_request has unexpected type")
	}

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("model", info.UpstreamModelName)
	_ = w.WriteField("prompt", req.Prompt)
	_ = w.WriteField("duration", strconv.Itoa(resolveDuration(&req)))
	_ = w.WriteField("resolution", "720p")
	_ = w.WriteField("aspect_ratio", ratioFromRequest(c))
	// files 是 URL 字符串（不是文件上传）。多个素材重复写同名字段。
	for _, u := range collectMediaURLs(c, &req) {
		_ = w.WriteField("files", u)
	}
	if err := w.Close(); err != nil {
		return nil, errors.Wrap(err, "close multipart writer failed")
	}
	c.Request.Header.Set("Content-Type", w.FormDataContentType())
	return &buf, nil
}

func (a *TaskAdaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (*http.Response, error) {
	return channel.DoTaskApiRequest(a, c, info, requestBody)
}

func (a *TaskAdaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (string, []byte, *dto.TaskError) {
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", nil, service.TaskErrorWrapper(err, "read_response_body_failed", http.StatusInternalServerError)
	}
	_ = resp.Body.Close()

	var up submitResponse
	if err := common.Unmarshal(responseBody, &up); err != nil {
		return "", nil, service.TaskErrorWrapper(errors.Wrapf(err, "body: %s", responseBody),
			"unmarshal_response_body_failed", http.StatusInternalServerError)
	}
	upstreamID := up.TaskID
	if upstreamID == "" {
		upstreamID = up.ID
	}
	if upstreamID == "" {
		return "", nil, service.TaskErrorWrapper(errors.New("task_id is empty"),
			"invalid_response", http.StatusInternalServerError)
	}

	up.ID = info.PublicTaskID
	up.TaskID = info.PublicTaskID
	up.Object = "video"
	up.Model = info.OriginModelName
	c.JSON(http.StatusOK, up)
	return upstreamID, responseBody, nil
}

// FetchTask 轮询。未完成时上游返回 409 + JSON；**完成时直接返回 mp4 二进制**。
//
// 轮询循环会把响应体整个存进 task.Data（[task_polling.go] 的
// `task.Data = redactVideoResponseBody(responseBody)`，非 JSON 时原样保留），
// 若把二进制透传上去，就会把整部视频（数 MB）写进任务记录。
//
// 因此这里只读文件头 12 字节判别：认出 mp4 就**立刻断开、不下载正片**，
// 换成一个合成的小 JSON 交给上层。正片留到用户调 /v1/videos/{id}/content
// 时由 video_proxy 按需回源。
func (a *TaskAdaptor) FetchTask(baseUrl, key string, body map[string]any, proxy string) (*http.Response, error) {
	taskID, ok := body["task_id"].(string)
	if !ok {
		return nil, errors.New("invalid task_id")
	}
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/api/video/%s", baseUrl, taskID), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)

	client, err := service.GetHttpClientWithProxy(proxy)
	if err != nil {
		return nil, fmt.Errorf("new proxy http client failed: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}

	head := make([]byte, 12)
	n, _ := io.ReadFull(resp.Body, head)
	if n >= 12 && bytes.Equal(head[4:8], []byte("ftyp")) {
		_ = resp.Body.Close() // 正片不在这里下载
		synth := []byte(`{"status":"SUCCEEDED"}`)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewReader(synth)),
		}, nil
	}
	// 不是 mp4：把已读的头部拼回去，原样交给上层解析
	resp.Body = io.NopCloser(io.MultiReader(bytes.NewReader(head[:n]), resp.Body))
	return resp, nil
}

func (a *TaskAdaptor) ParseTaskResult(respBody []byte) (*relaycommon.TaskInfo, error) {
	info := &relaycommon.TaskInfo{Code: 0}

	// FetchTask 认出 mp4 后会换成这个合成体；也兼容极端情况下透传上来的真二进制。
	if gjson.GetBytes(respBody, "status").String() == "SUCCEEDED" ||
		(len(respBody) > 12 && bytes.Equal(respBody[4:8], []byte("ftyp"))) {
		info.Status = model.TaskStatusSuccess
		// Url 留空：video_proxy 的 SecureSkill 分支会用上游 task_id 回源拉取，
		// 因为上游根本不提供可保存的成片直链。
		return info, nil
	}

	// 其余即 JSON：要么"未完成"(409)，要么真失败
	code := gjson.GetBytes(respBody, "error.code").String()
	msg := gjson.GetBytes(respBody, "error.message").String()
	switch {
	case code == "task_not_completed", strings.Contains(strings.ToLower(msg), "not completed"):
		info.Status = model.TaskStatusInProgress
	case strings.Contains(strings.ToLower(gjson.GetBytes(respBody, "status").String()), "queued"):
		info.Status = model.TaskStatusQueued
	case msg != "" || code != "":
		info.Status = model.TaskStatusFailure
		info.Reason = friendlyReason(msg, code)
	default:
		// 读不懂就当仍在进行，交给上层超时机制处理，别误判成失败白退款
		info.Status = model.TaskStatusInProgress
	}
	return info, nil
}

// ConvertToOpenAIVideo 合成对外响应，**不回显 task.Data**。
//
// 该上游的轮询响应要么是 409 错误体、要么是 mp4 二进制，两者都不是可回显的任务
// 对象（轮询循环会用响应体覆盖 task.Data）。所以这里完全用任务记录本身重建，
// 顺带天然免疫上游身份泄露。
func (a *TaskAdaptor) ConvertToOpenAIVideo(task *model.Task) ([]byte, error) {
	out := map[string]any{
		"id":         task.TaskID,
		"task_id":    task.TaskID,
		"object":     "video",
		"model":      task.Properties.OriginModelName,
		"status":     string(task.Status),
		"progress":   task.Progress,
		"created_at": task.CreatedAt,
	}
	if task.Status == model.TaskStatusSuccess {
		out["object"] = taskcommon.BuildProxyURL(task.TaskID)
	}
	if task.FailReason != "" {
		out["error"] = map[string]any{"message": task.FailReason}
	}
	data, err := common.Marshal(out)
	if err != nil {
		return nil, errors.Wrap(err, "marshal video response failed")
	}
	return data, nil
}
