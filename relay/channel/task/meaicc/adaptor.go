// Package meaicc 适配 meaicc（api.meaicc.com）的 seedance 视频接口。
//
// 它不是 OpenAI 兼容格式，与 sora 适配器的三处关键差异：
//  1. prompt 与 media 必须嵌在 input{} 里，参数在 parameters{}；顶层也有一个
//     同名 prompt 字段但**不被上游使用**（曾据此误判为"上游不转发 prompt"）。
//  2. 状态是大写前缀 SUCCEEDED/FAILED/RUNNING/PENDING，失败原因直接拼在
//     status 串里（如 "FAILED: 内容未通过审核"），没有独立 error 对象。
//  3. 成片 URL 在 object 字段，只缓存 24 小时。
//
// 素材引用语法也不同：对外统一 @image1/@video1/@audio1，这里重写成上游认的
// 图1/视频1/音频1，避免用户 prompt 在不同上游间失效。
package meaicc

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"regexp"
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
	"github.com/tidwall/sjson"
)

// ============================
// 上游报文结构
// ============================

type upstreamMedia struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

type upstreamInput struct {
	Prompt string          `json:"prompt"`
	Media  []upstreamMedia `json:"media,omitempty"`
}

type upstreamParams struct {
	Resolution string `json:"resolution,omitempty"`
	Ratio      string `json:"ratio,omitempty"`
	Duration   int    `json:"duration"` // 必须是整数；上游对 seconds 才要字符串
}

type upstreamRequest struct {
	Model      string         `json:"model"`
	Input      upstreamInput  `json:"input"`
	Parameters upstreamParams `json:"parameters"`
}

type upstreamResponse struct {
	ID        string `json:"id"`
	TaskID    string `json:"task_id"`
	Object    string `json:"object"` // 完成时是成片 URL；进行中为空
	Model     string `json:"model"`
	Status    string `json:"status"`
	Progress  int    `json:"progress"`
	CreatedAt int64  `json:"created_at"`
}

// ============================
// Adaptor
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

func (a *TaskAdaptor) GetModelList() []string { return ModelList }
func (a *TaskAdaptor) GetChannelName() string { return ChannelName }

// ============================
// 请求校验
// ============================

// resolveDuration 统一取时长：优先 duration(int)，回退 seconds(string)，缺省 5。
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

// ValidateRequestAndSetAction 做**前置**校验。
// 上游除 sd-2-c6 外都是"先扣费、异步再校验"，非法参数会真建任务、白等一轮才失败
// （虽然会退款，但用户白等几十秒）。所以凡是能在本地判定的，一律在这里拦掉。
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

	// ratio 允许留空（上游有默认值），给了就必须合法
	if r := strings.TrimSpace(ratioFromRequest(c, &req)); r != "" && !validRatios[r] {
		return service.TaskErrorWrapperLocal(
			fmt.Errorf("ratio must be one of 1:1, 16:9, 9:16, 4:3, 3:4"),
			"invalid_request", http.StatusBadRequest)
	}

	// 该上游只出 720p —— resolution 参数会被静默忽略。用户明确要更高分辨率时
	// 直接报错而不是悄悄降级给他 720p（静默降级是最难排查的那类问题）。
	if size := strings.TrimSpace(req.Size); size != "" && isAboveHD(size) {
		return service.TaskErrorWrapperLocal(
			fmt.Errorf("this model only supports 720p output; request 1080p/4K via the per-second video group"),
			"invalid_request", http.StatusBadRequest)
	}

	// 该上游只接受公网 http(s) 素材地址，不收 base64。
	for _, u := range collectAllMediaURLs(c, &req) {
		if strings.HasPrefix(u, "data:") {
			return service.TaskErrorWrapperLocal(
				errors.New("reference materials must be public http(s) URLs; base64 is not supported by this model"),
				"invalid_request", http.StatusBadRequest)
		}
	}

	c.Set("task_request", req)
	return nil
}

// isAboveHD 判断用户是否明确要求高于 720p。仅识别常见写法，无法解析的一律放行
// （放行后上游给 720p，与不传 size 行为一致）。
func isAboveHD(size string) bool {
	s := strings.ToLower(strings.TrimSpace(size))
	if s == "1080p" || s == "4k" || s == "2k" || s == "1440p" {
		return true
	}
	if w, h, ok := parseWxH(s); ok {
		return w > 1280 || h > 720
	}
	return false
}

func parseWxH(s string) (int, int, bool) {
	parts := strings.Split(s, "x")
	if len(parts) != 2 {
		return 0, 0, false
	}
	w, e1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	h, e2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if e1 != nil || e2 != nil {
		return 0, 0, false
	}
	return w, h, true
}

// ============================
// 素材与引用语法
// ============================

// rawBody 取原始请求体。自定义顶层字段（reference_*_urls / ratio）不在
// TaskSubmitReq 里，用 typed struct 重序列化会把它们丢掉，必须读原始 body。
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

func ratioFromRequest(c *gin.Context, req *relaycommon.TaskSubmitReq) string {
	if body := rawBody(c); len(body) > 0 {
		if v := gjson.GetBytes(body, "ratio"); v.Exists() {
			return v.String()
		}
		if v := gjson.GetBytes(body, "aspect_ratio"); v.Exists() {
			return v.String()
		}
	}
	if req.Metadata != nil {
		if v, ok := req.Metadata["ratio"].(string); ok {
			return v
		}
	}
	return ""
}

// urlsAt 读取一个字符串数组字段。
func urlsAt(body []byte, path string) []string {
	var out []string
	for _, v := range gjson.GetBytes(body, path).Array() {
		if s := strings.TrimSpace(v.String()); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// collectMedia 收集参考素材并转成上游的 media[]。
// 对外契约（统一各上游的差异）：
//
//	reference_image_urls / reference_video_urls / reference_audio_urls
//	first_frame_url / last_frame_url
//	images（OpenAI 风格的别名，等同 reference_image_urls）
func collectMedia(c *gin.Context, req *relaycommon.TaskSubmitReq) []upstreamMedia {
	body := rawBody(c)
	var media []upstreamMedia

	add := func(urls []string, typ string) {
		for _, u := range urls {
			media = append(media, upstreamMedia{Type: typ, URL: u})
		}
	}

	if len(body) > 0 {
		// 首尾帧先加：上游按 media 顺序理解 图1/图2，首帧应排在参考图之前
		if v := gjson.GetBytes(body, "first_frame_url"); v.Exists() && v.String() != "" {
			media = append(media, upstreamMedia{Type: mediaTypeFirst, URL: v.String()})
		}
		if v := gjson.GetBytes(body, "last_frame_url"); v.Exists() && v.String() != "" {
			media = append(media, upstreamMedia{Type: mediaTypeLast, URL: v.String()})
		}
		add(urlsAt(body, "reference_image_urls"), mediaTypeImage)
		add(urlsAt(body, "reference_video_urls"), mediaTypeVideo)
		add(urlsAt(body, "reference_audio_urls"), mediaTypeVoice)
	}

	// images / image 作为参考图别名（仅在没给显式字段时生效，避免重复）
	if len(media) == 0 {
		add(req.Images, mediaTypeImage)
		if s := strings.TrimSpace(req.Image); s != "" {
			media = append(media, upstreamMedia{Type: mediaTypeImage, URL: s})
		}
	}
	return media
}

func collectAllMediaURLs(c *gin.Context, req *relaycommon.TaskSubmitReq) []string {
	var out []string
	for _, m := range collectMedia(c, req) {
		out = append(out, m.URL)
	}
	return out
}

// refRewrites 把对外统一的 @image1 / @video1 / @audio1 重写成本上游认的
// 图1 / 视频1 / 音频1。不同上游语法不同（现役 #174 认 @image1），对外统一后
// 由各 adaptor 各自翻译，用户的 prompt 才能在 failover 时继续生效。
var refRewrites = []struct {
	re   *regexp.Regexp
	tmpl string
}{
	{regexp.MustCompile(`@image(\d+)`), "图$1"},
	{regexp.MustCompile(`@img(\d+)`), "图$1"},
	{regexp.MustCompile(`@video(\d+)`), "视频$1"},
	{regexp.MustCompile(`@audio(\d+)`), "音频$1"},
	{regexp.MustCompile(`@voice(\d+)`), "音频$1"},
}

func rewriteReferences(prompt string) string {
	for _, r := range refRewrites {
		prompt = r.re.ReplaceAllString(prompt, r.tmpl)
	}
	return prompt
}

// ============================
// 请求构造
// ============================

func (a *TaskAdaptor) BuildRequestURL(info *relaycommon.RelayInfo) (string, error) {
	return fmt.Sprintf("%s/v1/videos", a.baseURL), nil
}

func (a *TaskAdaptor) BuildRequestHeader(c *gin.Context, req *http.Request, info *relaycommon.RelayInfo) error {
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	req.Header.Set("Content-Type", "application/json")
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

	body := upstreamRequest{
		Model: info.UpstreamModelName,
		Input: upstreamInput{
			Prompt: rewriteReferences(req.Prompt),
			Media:  collectMedia(c, &req),
		},
		Parameters: upstreamParams{
			Resolution: "720p", // 上游只出 720p，显式带上以免默认档变化
			Ratio:      ratioFromRequest(c, &req),
			Duration:   resolveDuration(&req),
		},
	}
	if body.Parameters.Ratio == "" {
		body.Parameters.Ratio = "16:9"
	}

	data, err := common.Marshal(body)
	if err != nil {
		return nil, errors.Wrap(err, "marshal meaicc request failed")
	}
	return bytes.NewReader(data), nil
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

	var up upstreamResponse
	if err := common.Unmarshal(responseBody, &up); err != nil {
		return "", nil, service.TaskErrorWrapper(errors.Wrapf(err, "body: %s", responseBody),
			"unmarshal_response_body_failed", http.StatusInternalServerError)
	}

	// 提交阶段就 FAILED 的（如内容审核）：直接当失败返回，避免建一个注定失败的任务
	if strings.HasPrefix(strings.ToUpper(up.Status), "FAILED") {
		return "", nil, service.TaskErrorWrapper(errors.New(friendlyReason(up.Status)),
			"upstream_rejected", http.StatusBadRequest)
	}

	upstreamID := up.ID
	if upstreamID == "" {
		upstreamID = up.TaskID
	}
	if upstreamID == "" {
		return "", nil, service.TaskErrorWrapper(errors.New("task_id is empty"),
			"invalid_response", http.StatusInternalServerError)
	}

	// 回给客户端的是公开任务号，不暴露上游任务号
	up.ID = info.PublicTaskID
	up.TaskID = info.PublicTaskID
	up.Object = "video"
	up.Model = info.OriginModelName
	c.JSON(http.StatusOK, up)
	return upstreamID, responseBody, nil
}

// ============================
// 轮询
// ============================

func (a *TaskAdaptor) FetchTask(baseUrl, key string, body map[string]any, proxy string) (*http.Response, error) {
	taskID, ok := body["task_id"].(string)
	if !ok {
		return nil, errors.New("invalid task_id")
	}
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/v1/videos/%s", baseUrl, taskID), nil)
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

func (a *TaskAdaptor) ParseTaskResult(respBody []byte) (*relaycommon.TaskInfo, error) {
	var up upstreamResponse
	if err := common.Unmarshal(respBody, &up); err != nil {
		return nil, errors.Wrap(err, "unmarshal task result failed")
	}

	info := &relaycommon.TaskInfo{Code: 0}
	status := strings.ToUpper(strings.TrimSpace(up.Status))

	switch {
	case strings.HasPrefix(status, "SUCCEEDED"), status == "SUCCESS", status == "COMPLETED":
		info.Status = model.TaskStatusSuccess
		// 成片直链放进 Url，交由 /v1/videos/{id}/content 代理按需拉取
		// （video_proxy 的 default 分支读 PrivateData.ResultURL）。
		info.Url = up.Object
	case strings.HasPrefix(status, "FAILED"), status == "CANCELLED":
		info.Status = model.TaskStatusFailure
		info.Reason = friendlyReason(up.Status)
	case status == "RUNNING", status == "IN_PROGRESS", status == "PROCESSING":
		info.Status = model.TaskStatusInProgress
	case status == "PENDING", status == "QUEUED":
		info.Status = model.TaskStatusQueued
	}

	if up.Progress > 0 && up.Progress < 100 {
		info.Progress = fmt.Sprintf("%d%%", up.Progress)
	}
	return info, nil
}

// ============================
// 对外响应改写
// ============================

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
	// object 在完成时是上游成片直链，会暴露上游身份（minioapi.meaicc.com）——
	// 换成本站代理地址。metadata.* 一并覆盖，避免留下未改写的残留字段。
	proxyURL := taskcommon.BuildProxyURL(task.TaskID)
	for _, path := range []string{"object", "video_url", "url", "metadata.url", "metadata.video_url"} {
		if gjson.GetBytes(data, path).Exists() {
			if data, err = sjson.SetBytes(data, path, proxyURL); err != nil {
				return nil, errors.Wrapf(err, "set %s failed", path)
			}
		}
	}
	// 上游模型名（sd-2-c6 等）不能外泄，还原成用户请求的对外名
	if origin := task.Properties.OriginModelName; origin != "" {
		if data, err = sjson.SetBytes(data, "model", origin); err != nil {
			return nil, errors.Wrap(err, "set model failed")
		}
	}
	return data, nil
}
