// Package sudashui 适配 sudashuiapi（api.sudashuiapi.com）的 seedance 视频接口。
//
// 三处与其它上游都不同、且都是 260808 实测确认过的：
//
//  1. **参数塞在 metadata.payload，而且是个 JSON 字符串**（不是嵌套对象）。
//     这是本适配器最主要的存在理由——直接透传我们的扁平契约上游一概不认。
//  2. **查询响应包了一层**：{code, message, data:{status, quota, data:{creations:[{url}]}}}。
//     状态在 data.status（大写 SUCCESS/FAILURE/IN_PROGRESS），成片 URL 在
//     data.data.creations[0].url。只读顶层 status 会永远拿到空串。
//  3. **sdas-gf- 官方模型的真人图必须用 officialAssetIndexes 标记**，
//     不标会被真人风控拦。本适配器默认把所有参考图都标上——宁可多标，
//     因为漏标的代价是任务失败，而多标目前未见副作用。
//
// 计费实测：quota 1656000 = 0.414(¥/秒) × 4 秒，确为按秒；该站额度单位是
// 1e6 = ¥1（与我方 5e5 = ¥1 不同，仅影响读它的余额，不影响我方计费）。
package sudashui

import (
	"bytes"
	"fmt"
	"io"
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
	"github.com/tidwall/sjson"
)

// ============================
// 上游报文结构
// ============================

// payload 是 metadata.payload 的内容。它会被序列化成**字符串**再放进请求。
type payload struct {
	AspectRatio string   `json:"aspectRatio"`
	Mode        string   `json:"mode"`
	ImageURLs   []string `json:"imageUrls,omitempty"`
	VideoURLs   []string `json:"videoUrls,omitempty"`
	AudioURLs   []string `json:"audioUrls,omitempty"`
	FirstFrame  string   `json:"firstFrameUrl,omitempty"`
	LastFrame   string   `json:"lastFrameUrl,omitempty"`
	// OfficialAssetIndexes 标记哪些 imageUrls 是真人/虚拟人像。
	// sdas-gf- 官方模型不标会被风控拦。
	OfficialAssetIndexes []int `json:"officialAssetIndexes,omitempty"`
}

type metadataField struct {
	Payload string `json:"payload"`
}

type upstreamRequest struct {
	Model    string        `json:"model"`
	Prompt   string        `json:"prompt"`
	Duration int           `json:"duration"`
	Metadata metadataField `json:"metadata"`
	// 不发 resolution：分辨率由模型名决定（官方文档明示）
}

type creation struct {
	ID       string `json:"id"`
	URL      string `json:"url"`
	CoverURL string `json:"cover_url"`
}

// innerData 是 data.data —— 上游任务的原始载荷。
type innerData struct {
	State        string     `json:"state"`
	Message      string     `json:"message"`
	FailReason   string     `json:"fail_reason"`
	ErrorMessage string     `json:"error_message"`
	ErrCode      string     `json:"err_code"`
	Creations    []creation `json:"creations"`
}

// taskData 是 data —— 我方视角的任务记录。
type taskData struct {
	TaskID     string    `json:"task_id"`
	Status     string    `json:"status"` // SUCCESS / FAILURE / IN_PROGRESS / QUEUED
	Progress   string    `json:"progress"`
	Quota      int64     `json:"quota"`
	FailReason string    `json:"fail_reason"`
	Data       innerData `json:"data"`
}

// upstreamResponse 同时覆盖**两种结构不同**的响应（260808 实测）：
//
//	提交：{"id":"task_x","task_id":"task_x",...}          ← 任务号在**顶层**
//	查询：{"code":"success","data":{"task_id":..,"status":..,"data":{creations}}}
//
// 只按查询的结构去读提交响应会得到空 task_id（本适配器首测就是这么失败的）。
type upstreamResponse struct {
	Code    string   `json:"code"` // "success" 或错误码
	Message string   `json:"message"`
	Data    taskData `json:"data"`
	// 提交阶段的顶层任务号
	ID     string `json:"id"`
	TaskID string `json:"task_id"`
}

// submitTaskID 取提交阶段的任务号，兼容顶层与嵌套两种放法。
func (u upstreamResponse) submitTaskID() string {
	return firstNonEmpty(u.TaskID, u.ID, u.Data.TaskID)
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

// EstimateBilling 提供按秒计费的时长乘数。
// 该站全线按秒（实测 quota 1656000 = 0.414 × 4 秒），缺了这个每单都会少收。
func (a *TaskAdaptor) EstimateBilling(c *gin.Context, info *relaycommon.RelayInfo) map[string]float64 {
	req, err := relaycommon.GetTaskRequest(c)
	if err != nil {
		return nil
	}
	return map[string]float64{"seconds": float64(resolveDuration(&req))}
}

// ============================
// 参数归一
// ============================

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

func rawBody(c *gin.Context) []byte {
	if c == nil || c.Request == nil {
		return nil // 无请求体时一律走 TaskSubmitReq 的兜底字段，不 panic
	}
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

func firstString(body []byte, paths ...string) string {
	for _, p := range paths {
		if v := gjson.GetBytes(body, p); v.Exists() {
			if s := strings.TrimSpace(v.String()); s != "" {
				return s
			}
		}
	}
	return ""
}

func urlsAt(body []byte, path string) []string {
	var out []string
	for _, v := range gjson.GetBytes(body, path).Array() {
		if s := strings.TrimSpace(v.String()); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func ratioFromRequest(c *gin.Context, req *relaycommon.TaskSubmitReq) string {
	if body := rawBody(c); len(body) > 0 {
		if s := firstString(body, "ratio", "aspect_ratio"); s != "" {
			return s
		}
	}
	if req.Metadata != nil {
		if v, ok := req.Metadata["ratio"].(string); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func resolutionFromRequest(c *gin.Context, req *relaycommon.TaskSubmitReq) string {
	if body := rawBody(c); len(body) > 0 {
		if s := firstString(body, "resolution"); s != "" {
			return s
		}
	}
	return ""
}

// resolutionFromModelName 从模型名后缀取档位。该站分辨率同样由模型名决定。
func resolutionFromModelName(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	for _, suf := range []string{"-2160p", "-1080p", "-720p", "-480p", "-4k", "-2k"} {
		if strings.HasSuffix(n, suf) {
			return normalizeResolution(strings.TrimPrefix(suf, "-"))
		}
	}
	return ""
}

func normalizeResolution(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "sd", "480", "480p":
		return "480p"
	case "hd", "720", "720p":
		return "720p"
	case "fhd", "1080", "1080p":
		return "1080p"
	case "2k", "1440", "1440p":
		return "2k"
	case "4k", "2160", "2160p", "uhd":
		return "4k"
	}
	return strings.ToLower(strings.TrimSpace(s))
}

// isOfficialModel 判断是否 sdas-gf 系列官方模型（真人图需 officialAssetIndexes 标记）。
func isOfficialModel(name string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(name)), "sdas-gf")
}

// ============================
// 素材
// ============================

type materials struct {
	Images     []string
	Videos     []string
	Audios     []string
	FirstFrame string
	LastFrame  string
}

func (m materials) empty() bool {
	return len(m.Images) == 0 && len(m.Videos) == 0 && len(m.Audios) == 0 &&
		m.FirstFrame == "" && m.LastFrame == ""
}

func (m materials) allURLs() []string {
	out := make([]string, 0, len(m.Images)+len(m.Videos)+len(m.Audios)+2)
	out = append(out, m.Images...)
	out = append(out, m.Videos...)
	out = append(out, m.Audios...)
	if m.FirstFrame != "" {
		out = append(out, m.FirstFrame)
	}
	if m.LastFrame != "" {
		out = append(out, m.LastFrame)
	}
	return out
}

// collectMaterials 收集素材。对外契约与 dimensio / mai 完全一致。
//
// 注意首尾帧在本上游是**独立字段**（firstFrameUrl / lastFrameUrl + mode=frames），
// 不像 dimensio/mai 那样混进图片数组，所以这里单独存放。
func collectMaterials(c *gin.Context, req *relaycommon.TaskSubmitReq) materials {
	var m materials
	body := rawBody(c)

	if len(body) > 0 {
		m.FirstFrame = firstString(body, "first_frame_url", "first_image_url")
		m.LastFrame = firstString(body, "last_frame_url", "last_image_url")
		m.Images = append(m.Images, urlsAt(body, "reference_image_urls")...)
		m.Videos = append(m.Videos, urlsAt(body, "reference_video_urls")...)
		m.Audios = append(m.Audios, urlsAt(body, "reference_audio_urls")...)
	}

	if m.empty() {
		m.Images = append(m.Images, req.Images...)
		if s := strings.TrimSpace(req.Image); s != "" {
			m.Images = append(m.Images, s)
		}
		if s := strings.TrimSpace(req.InputReference); s != "" {
			m.Images = append(m.Images, s)
		}
	}
	return m
}

// ============================
// 请求校验
// ============================

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

	if r := strings.TrimSpace(ratioFromRequest(c, &req)); r != "" && !validRatios[r] {
		return service.TaskErrorWrapperLocal(
			errors.New("ratio must be one of 16:9, 9:16, 1:1, 4:3, 3:4, 21:9"),
			"invalid_request", http.StatusBadRequest)
	}

	origin := strings.TrimSpace(info.OriginModelName)
	if origin == "" {
		origin = strings.TrimSpace(req.Model)
	}
	// 档位由模型名决定；显式传了不一致的 resolution 直接报错，不默默二选一。
	if tier := resolutionFromModelName(origin); tier != "" {
		if raw := strings.TrimSpace(resolutionFromRequest(c, &req)); raw != "" &&
			normalizeResolution(raw) != tier {
			return service.TaskErrorWrapperLocal(
				fmt.Errorf("model %s is billed at %s; requested resolution %s conflicts — use the model whose name matches the resolution you want",
					origin, tier, raw),
				"invalid_request", http.StatusBadRequest)
		}
	}

	mats := collectMaterials(c, &req)
	// 只收公网 URL，与 dimensio/mai 保持一致（该组统一拒 base64）
	for _, u := range mats.allURLs() {
		if strings.HasPrefix(u, "data:") {
			return service.TaskErrorWrapperLocal(
				errors.New("reference materials must be public http(s) URLs; base64 is not supported by this group"),
				"invalid_request", http.StatusBadRequest)
		}
	}
	// frames 模式上游要求首尾帧成对
	if (mats.FirstFrame == "") != (mats.LastFrame == "") {
		return service.TaskErrorWrapperLocal(
			errors.New("first/last frame mode requires both first_frame_url and last_frame_url"),
			"invalid_request", http.StatusBadRequest)
	}

	c.Set("task_request", req)
	return nil
}

// ============================
// 请求构造
// ============================

func (a *TaskAdaptor) BuildRequestURL(info *relaycommon.RelayInfo) (string, error) {
	return fmt.Sprintf("%s/v1/video/generations", a.baseURL), nil
}

func (a *TaskAdaptor) BuildRequestHeader(c *gin.Context, req *http.Request, info *relaycommon.RelayInfo) error {
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	req.Header.Set("Content-Type", "application/json")
	return nil
}

// buildPayload 组装 metadata.payload 的内容。
func buildPayload(c *gin.Context, req *relaycommon.TaskSubmitReq, upstreamModel string) payload {
	mats := collectMaterials(c, req)

	ratio := strings.TrimSpace(ratioFromRequest(c, req))
	if ratio == "" {
		ratio = "16:9"
	}

	p := payload{AspectRatio: ratio, Mode: modeReferences}
	if mats.FirstFrame != "" || mats.LastFrame != "" {
		p.Mode = modeFrames
		p.FirstFrame = mats.FirstFrame
		p.LastFrame = mats.LastFrame
		return p
	}

	p.ImageURLs = mats.Images
	p.VideoURLs = mats.Videos
	p.AudioURLs = mats.Audios

	// sdas-gf 官方模型：真人/虚拟人像图必须标记，否则被真人风控拦。
	// 我们无法在网关侧判断哪张含人脸，故**全部标上**——漏标会直接失败，
	// 而多标目前未见副作用（260808 实测标了全部，锁脸成功）。
	if isOfficialModel(upstreamModel) && len(p.ImageURLs) > 0 {
		idx := make([]int, 0, len(p.ImageURLs))
		for i := range p.ImageURLs {
			idx = append(idx, i)
		}
		p.OfficialAssetIndexes = idx
	}
	return p
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

	p := buildPayload(c, &req, info.UpstreamModelName)
	// 🔑 payload 必须是**字符串**，不是嵌套对象
	pJSON, err := common.Marshal(p)
	if err != nil {
		return nil, errors.Wrap(err, "marshal payload failed")
	}

	body := upstreamRequest{
		Model:    info.UpstreamModelName,
		Prompt:   req.Prompt,
		Duration: resolveDuration(&req),
		Metadata: metadataField{Payload: string(pJSON)},
	}
	data, err := common.Marshal(body)
	if err != nil {
		return nil, errors.Wrap(err, "marshal sudashui request failed")
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
	if up.Code != "" && up.Code != "success" {
		return "", nil, service.TaskErrorWrapper(errors.New(friendlyReason(up.Code, up.Message)),
			"upstream_rejected", http.StatusBadRequest)
	}
	upstreamID := up.submitTaskID()
	if upstreamID == "" {
		return "", nil, service.TaskErrorWrapper(errors.New("task_id is empty"),
			"invalid_response", http.StatusInternalServerError)
	}

	c.JSON(http.StatusOK, map[string]any{
		"id":      info.PublicTaskID,
		"task_id": info.PublicTaskID,
		"object":  "video",
		"model":   info.OriginModelName,
		"status":  "queued",
	})
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
	req, err := http.NewRequest(http.MethodGet,
		fmt.Sprintf("%s/v1/video/generations/%s", baseUrl, taskID), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)

	client, err := service.GetHttpClientWithProxy(proxy)
	if err != nil {
		return nil, fmt.Errorf("new proxy http client failed: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil || resp == nil {
		return resp, err
	}
	return normalizeFetchResponse(resp), nil
}

// normalizeFetchResponse 把上游响应的顶层 `code` 改名，使其**不被误认为我方自家格式**。
//
// 🔑 这是"上游也是 new-api"才有的坑（260809 实测）：
// service/task_polling.go 会先尝试 dto.TaskResponse[model.Task] 解析，只要顶层
// code == "success" 就认定是自家格式，于是 **完全跳过 adaptor.ParseTaskResult**，
// 转而用 t.GetResultURL() 取 private_data.result_url —— 那是我方内部字段，上游
// 当然不会返回，于是 URL 为空，ResultURL 退化成我们自己的代理地址，
// /content 去取自己 → 永久 502（成片其实是好的，只是取不到）。
//
// 改名后判定失败，流程会 fallback 到本适配器的 ParseTaskResult，一切正常。
// 只动顶层 code 这一个键，其余原样保留，ParseTaskResult 读的是 data.* 不受影响。
func normalizeFetchResponse(resp *http.Response) *http.Response {
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		resp.Body = io.NopCloser(bytes.NewReader(nil))
		return resp
	}
	if gjson.GetBytes(body, "code").String() == "success" {
		if patched, err := sjson.SetBytes(body, "code", "sudashui_success"); err == nil {
			body = patched
		}
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	return resp
}

func (a *TaskAdaptor) ParseTaskResult(respBody []byte) (*relaycommon.TaskInfo, error) {
	var up upstreamResponse
	if err := common.Unmarshal(respBody, &up); err != nil {
		return nil, errors.Wrap(err, "unmarshal task result failed")
	}

	info := &relaycommon.TaskInfo{Code: 0}
	d := up.Data

	switch strings.ToUpper(strings.TrimSpace(d.Status)) {
	case "SUCCESS", "SUCCEEDED", "COMPLETED":
		// 🔑 上游存在**时序窗口**：state 先翻成 success，creations 稍后才填上。
		// 若在这一瞬就报成功且不带 URL，task_polling 会把 ResultURL 退化成我们
		// 自己的代理地址（service/task_polling.go 的 else 分支），而 ResultURL
		// 只在 SUCCESS 那一次写入 —— 代理随后去取自己，永久 502。
		// 所以拿不到成片地址就**不算成功**，继续轮询。
		if len(d.Data.Creations) == 0 || strings.TrimSpace(d.Data.Creations[0].URL) == "" {
			info.Status = model.TaskStatusInProgress
			break
		}
		info.Status = model.TaskStatusSuccess
		info.Url = d.Data.Creations[0].URL
	case "FAILURE", "FAILED", "CANCELLED":
		info.Status = model.TaskStatusFailure
		info.Reason = friendlyReason(d.Data.ErrCode, firstNonEmpty(
			d.FailReason, d.Data.FailReason, d.Data.ErrorMessage, d.Data.Message))
	case "IN_PROGRESS", "PROCESSING", "RUNNING":
		info.Status = model.TaskStatusInProgress
	case "QUEUED", "NOT_START", "SUBMITTED", "PENDING":
		info.Status = model.TaskStatusQueued
	}

	if p := strings.TrimSpace(d.Progress); p != "" && p != "100%" {
		info.Progress = p
	}
	return info, nil
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if t := strings.TrimSpace(s); t != "" {
			return t
		}
	}
	return ""
}

// ============================
// 对外响应改写
// ============================

// ConvertToOpenAIVideo **重新构造**一个扁平响应，而不是在上游报文上打补丁。
//
// 🔑 上游本身也是个 new-api，它的查询响应是
// {code, data:{action, id, channel_id, quota, properties:{upstream_model_name}, data:{…}}}
// —— 整套内部结构。若沿用"改几个字段再原样返回"的做法（本适配器初版就是），
// 客户会拿到：
//   - 上游的内部字段（action / channel_id / properties.upstream_model_name…），既
//     泄露上游身份，也让人能算出我方成本（quota）；
//   - 与 dimensio / mai 完全不同的响应形状 —— 海外组承诺的"统一"就只统一了请求，
//     没统一响应，客户在主备切换时要写两套解析。
//
// 所以这里只输出白名单字段，形状与 dimensio / mai 对齐。
func (a *TaskAdaptor) ConvertToOpenAIVideo(task *model.Task) ([]byte, error) {
	status := "queued"
	switch task.Status {
	case model.TaskStatusSuccess:
		status = "completed"
	case model.TaskStatusFailure:
		status = "failed"
	case model.TaskStatusInProgress:
		status = "processing"
	}

	out := map[string]any{
		"id":      task.TaskID,
		"task_id": task.TaskID,
		"object":  "video", // 类型标记，不是 URL
		"model":   task.Properties.OriginModelName,
		"status":  status,
	}
	if p := strings.TrimSpace(task.Progress); p != "" {
		out["progress"] = p
	}
	if task.Status == model.TaskStatusSuccess {
		// 成片直链是 files.sudashuiapi.com/*，换成本站代理地址
		out["video_url"] = taskcommon.BuildProxyURL(task.TaskID)
	}
	if task.Status == model.TaskStatusFailure {
		reason := task.FailReason
		if reason == "" {
			reason = "任务失败，费用已退还"
		}
		out["status"] = "failed"
		out["error"] = map[string]any{"code": "generation_failed", "message": reason}
	}

	data, err := common.Marshal(out)
	if err != nil {
		return nil, errors.Wrap(err, "marshal video response failed")
	}
	return data, nil
}
