// Package maitoken 适配 mai-token（api.mai-token.com）的 seedance 视频接口。
//
// 为什么不能继续用通用的 sora 适配器（260808 实测坐实的生产 bug）：
//
//	sora 适配器是**原样透传**（只替换 model 字段）。而 mai 的参考素材必须走
//	content[] 多模态数组，它**不认识** reference_image_urls 这类扁平字段。
//	于是客户按我们海外组的统一契约传参考图时：
//	  · 走 dimensio → 正常锁脸
//	  · 走 mai      → 字段被**静默忽略**，出一条与素材无关的纯文生视频，照常收费
//	实测：同一张死链参考图，content[] 格式立刻失败（"参考image下载失败"，说明
//	字段被识别），reference_image_urls 格式却 SUCCESS（说明被忽略）。
//	这让海外组的"双上游互备"变成假的——平时 dimensio 顶着看不出来，只在切到
//	mai 的那一刻集体失效。
//
// 好消息：mai 的 @image1/@audio1/@video1 编号与我们对外契约完全一致，
// 不需要像 dimensio/meaicc 那样重写提示词。
package maitoken

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

type mediaURL struct {
	URL string `json:"url"`
}

// contentItem 是 content[] 的一个元素。三种媒体各用自己的字段名
// （image_url / audio_url / video_url），不能合并成一个通用字段。
type contentItem struct {
	Type     string    `json:"type"`
	Role     string    `json:"role,omitempty"`
	Text     string    `json:"text,omitempty"`
	ImageURL *mediaURL `json:"image_url,omitempty"`
	AudioURL *mediaURL `json:"audio_url,omitempty"`
	VideoURL *mediaURL `json:"video_url,omitempty"`
}

type upstreamRequest struct {
	Model   string        `json:"model"`
	Prompt  string        `json:"prompt"` // 顶层 prompt 必填，接口据此校验
	Content []contentItem `json:"content"`
	Seconds string        `json:"seconds"` // 必须是字符串 "4"~"15"
	Ratio   string        `json:"ratio,omitempty"`
	// 注意：**不发 resolution**。档位由模型名决定，同时传会冲突（文档 §17.4）。
}

type upstreamError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type upstreamResponse struct {
	ID        string         `json:"id"`
	Object    string         `json:"object"`
	Model     string         `json:"model"`
	Status    string         `json:"status"`
	Progress  int            `json:"progress"`
	CreatedAt int64          `json:"created_at"`
	VideoURL  string         `json:"video_url"`
	Error     *upstreamError `json:"error"`
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
// mai 全线按秒计费、ModelPrice 配的是每秒单价，缺了这个每单都会少收。
// （原先走 sora 适配器时由它提供，换成本适配器后必须自己实现。）
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

// resolutionFromModelName 从对外模型名的档位后缀推断分辨率。
// 与 dimensio 适配器同理：对外名按档位定价，档位必须由名字决定。
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
	case "4k", "2160", "2160p", "uhd":
		return "4k"
	}
	return strings.ToLower(strings.TrimSpace(s))
}

// ============================
// 原始 body 读取
// ============================

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

func urlsAt(body []byte, path string) []string {
	var out []string
	for _, v := range gjson.GetBytes(body, path).Array() {
		if s := strings.TrimSpace(v.String()); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// ============================
// 素材
// ============================

type materials struct {
	Images    []string // 顺序即 @image1..N；首尾帧排在最前
	Videos    []string
	Audios    []string
	FirstLast bool
}

func (m materials) empty() bool {
	return len(m.Images) == 0 && len(m.Videos) == 0 && len(m.Audios) == 0
}

func (m materials) allURLs() []string {
	out := make([]string, 0, len(m.Images)+len(m.Videos)+len(m.Audios))
	out = append(out, m.Images...)
	out = append(out, m.Videos...)
	out = append(out, m.Audios...)
	return out
}

// collectMaterials 收集参考素材。对外契约与 dimensio/meaicc 保持一致，
// 且**顺序也保持一致**（首帧、尾帧、再参考图）——否则同一句
// "@image1 中的人物" 在两个上游会指向不同的图，failover 时结果会变。
func collectMaterials(c *gin.Context, req *relaycommon.TaskSubmitReq) materials {
	var m materials
	body := rawBody(c)

	if len(body) > 0 {
		first := firstString(body, "first_frame_url", "first_image_url")
		last := firstString(body, "last_frame_url", "last_image_url")
		if first != "" {
			m.Images = append(m.Images, first)
			m.FirstLast = true
		}
		if last != "" {
			m.Images = append(m.Images, last)
			m.FirstLast = true
		}
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
	// 档位由模型名决定；显式传了不一致的 resolution 直接报错，不默默二选一
	// （两种选法都会造成"付的钱和拿到的货不符"）。
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

	// 上游虽然支持 Data URI，但 dimensio 明确拒绝 base64。海外组两家必须行为
	// 一致，故向严格的一方对齐——否则又会出现"在 mai 上能用、切到 dimensio 就挂"。
	for _, u := range mats.allURLs() {
		if strings.HasPrefix(u, "data:") {
			return service.TaskErrorWrapperLocal(
				errors.New("reference materials must be public http(s) URLs; base64 is not supported by this group"),
				"invalid_request", http.StatusBadRequest)
		}
	}

	if len(mats.Images) > maxImages {
		return service.TaskErrorWrapperLocal(
			fmt.Errorf("at most %d reference images are allowed (first/last frame count toward this limit)", maxImages),
			"invalid_request", http.StatusBadRequest)
	}
	if len(mats.Audios) > maxAudios {
		return service.TaskErrorWrapperLocal(
			fmt.Errorf("at most %d reference audio files are allowed", maxAudios),
			"invalid_request", http.StatusBadRequest)
	}
	if len(mats.Videos) > maxVideos {
		return service.TaskErrorWrapperLocal(
			fmt.Errorf("at most %d reference videos are allowed", maxVideos),
			"invalid_request", http.StatusBadRequest)
	}
	// 文档 §5.4：多模态参考以图片作为人物/场景锚点，纯音频或纯视频参考会失败。
	// 上游对此是**先建任务再失败**，所以在这里拦掉，省用户一轮等待。
	if (len(mats.Audios) > 0 || len(mats.Videos) > 0) && len(mats.Images) == 0 {
		return service.TaskErrorWrapperLocal(
			errors.New("reference audio/video requires at least one reference image as an anchor"),
			"invalid_request", http.StatusBadRequest)
	}
	if mats.FirstLast && len(mats.Images) != 2 {
		return service.TaskErrorWrapperLocal(
			errors.New("first/last frame mode requires exactly 2 images (first_frame_url and last_frame_url)"),
			"invalid_request", http.StatusBadRequest)
	}

	c.Set("task_request", req)
	return nil
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

// buildContent 把统一契约的扁平素材字段转成 mai 的 content[] 多模态数组。
// 这就是本适配器存在的理由——sora 透传会让这些字段被上游静默丢弃。
func buildContent(prompt string, m materials) []contentItem {
	items := []contentItem{{Type: "text", Text: prompt}}

	// 图片顺序即 @image1..N。首尾帧已在 collectMaterials 里排到最前。
	for i, u := range m.Images {
		role := roleRefImage
		if m.FirstLast {
			switch i {
			case 0:
				role = roleFirst
			case 1:
				role = roleLast
			}
		}
		items = append(items, contentItem{Type: "image_url", Role: role, ImageURL: &mediaURL{URL: u}})
	}
	for _, u := range m.Audios {
		items = append(items, contentItem{Type: "audio_url", Role: roleRefAudio, AudioURL: &mediaURL{URL: u}})
	}
	for _, u := range m.Videos {
		items = append(items, contentItem{Type: "video_url", Role: roleRefVideo, VideoURL: &mediaURL{URL: u}})
	}
	return items
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

	mats := collectMaterials(c, &req)
	body := upstreamRequest{
		Model:   info.UpstreamModelName,
		Prompt:  req.Prompt, // 顶层与 content[].text 必须同时给（文档 §17.1）
		Content: buildContent(req.Prompt, mats),
		Seconds: strconv.Itoa(resolveDuration(&req)),
		Ratio:   strings.TrimSpace(ratioFromRequest(c, &req)),
	}

	data, err := common.Marshal(body)
	if err != nil {
		return nil, errors.Wrap(err, "marshal mai-token request failed")
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
	if up.Error != nil && up.Error.Message != "" {
		return "", nil, service.TaskErrorWrapper(errors.New(friendlyReason(up.Error.Code, up.Error.Message)),
			"upstream_rejected", http.StatusBadRequest)
	}
	if up.ID == "" {
		return "", nil, service.TaskErrorWrapper(errors.New("task id is empty"),
			"invalid_response", http.StatusInternalServerError)
	}

	upstreamID := up.ID
	// 回给客户端的是公开任务号，且模型名还原成对外名
	up.ID = info.PublicTaskID
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
	switch strings.ToLower(strings.TrimSpace(up.Status)) {
	case "completed", "succeeded", "success":
		info.Status = model.TaskStatusSuccess
		info.Url = up.VideoURL
	case "failed", "cancelled", "canceled":
		info.Status = model.TaskStatusFailure
		if up.Error != nil {
			info.Reason = friendlyReason(up.Error.Code, up.Error.Message)
		} else {
			info.Reason = friendlyReason("", "")
		}
	case "in_progress", "processing", "running":
		info.Status = model.TaskStatusInProgress
	case "queued", "pending", "submitted":
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

	// 成片直链是 cdn.mai-token.com/*，会暴露上游身份——换成本站代理地址
	proxyURL := taskcommon.BuildProxyURL(task.TaskID)
	for _, path := range []string{"video_url", "url", "object", "metadata.url", "metadata.video_url"} {
		if gjson.GetBytes(data, path).Exists() {
			if data, err = sjson.SetBytes(data, path, proxyURL); err != nil {
				return nil, errors.Wrapf(err, "set %s failed", path)
			}
		}
	}
	if origin := task.Properties.OriginModelName; origin != "" {
		if data, err = sjson.SetBytes(data, "model", origin); err != nil {
			return nil, errors.Wrap(err, "set model failed")
		}
	}
	if task.Status == model.TaskStatusFailure && task.FailReason != "" {
		if data, err = sjson.SetBytes(data, "status", "FAILED: "+task.FailReason); err != nil {
			return nil, errors.Wrap(err, "set status failed")
		}
		if data, err = sjson.SetBytes(data, "error.message", task.FailReason); err != nil {
			return nil, errors.Wrap(err, "set error.message failed")
		}
	}
	return data, nil
}
