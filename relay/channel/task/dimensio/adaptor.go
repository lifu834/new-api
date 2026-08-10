// Package dimensio 适配 dimensio（jimeng.dimensio.cn）的 seedance 视频接口。
//
// 与 meaicc/secure-skill 的关键差异（260807 读官方文档 + 实测确认）：
//  1. 端点是**复数** POST /v1/videos/generations，查询 GET /v1/videos/tasks/{id}。
//     （单数 /v1/video/generations 是过时写法，会丢 ratio/resolution。）
//  2. 参考素材是**编号顶层字段** image_file_1…N / video_file_1…N / audio_file_1…N，
//     不是数组。用错字段名（images / reference_images）会被**静默忽略且照常扣费**——
//     曾据此误判"该渠道锁脸能力失效"。
//  3. functionMode 决定语义：omni_reference（多素材 @引用，默认）
//     / first_last_frames（0图=文生 1图=图生 2图=首尾帧）。
//  4. 全部**按秒**计费，没有按次档；成片 URL 在 result.url。
//
// 素材引用语法：对外统一 @image1/@video1/@audio1，这里重写成上游认的
// @image_file_1/@video_file_1/@audio_file_1。
package dimensio

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
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

// ============================
// 上游报文结构
// ============================

type upstreamResponse struct {
	TaskID          string `json:"task_id"`
	Status          string `json:"status"`
	Progress        int    `json:"progress"`
	RequestedModel  string `json:"requested_model"`
	EffectiveModel  string `json:"effective_model"`
	CreditsConsumed int    `json:"credits_consumed"`
	Created         int64  `json:"created"`
	Result          struct {
		URL string `json:"url"`
	} `json:"result"`
	// 错误有**两种**形态，都要认（260807 实测）：
	//  1. 同步拒绝（提交阶段）：{code:-2000, message:"...", data:null}
	//  2. 异步失败（轮询阶段）：{status:"failed", error:"...", failureType:"blocked"}
	//     —— 注意这里既没有 code 也没有 message，只读 code/message 会丢掉全部原因。
	Code        int    `json:"code"`
	Message     string `json:"message"`
	Error       string `json:"error"`
	FailureType string `json:"failureType"`
}

// failureText 取失败原因，兼容同步/异步两种形态。
func (u upstreamResponse) failureText() string {
	if s := strings.TrimSpace(u.Error); s != "" {
		return s
	}
	return strings.TrimSpace(u.Message)
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
//
// 🔑 **必须实现，不能沿用 BaseBilling**（它返回 nil）：dimensio 全线按秒计费，
// 而 ModelPrice 配的是「每秒」单价。没有 seconds 乘数时，一条 4 秒的
// sd-2.0-720p 只会按 ¥0.88 收（而不是 ¥0.88×4=¥3.52），成本却是 ¥2.08 ——
// 每单直接亏钱。260808 上线自测时实扣 440000 quota 暴露了这一点。
//
// 分辨率不在这里乘：档位差价已经体现在各自的 ModelPrice 里
// （sd-2.0-720p ¥0.88/秒 vs sd-2.0-1080p ¥1.8/秒），再乘一次会重复计价。
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

// normalizeResolution 把常见写法归一到上游档位名。
// 4K 有两种叫法：dvc/hgf 认 "4k"，pxv-standard 认 "2160p" —— 由 fitResolution 按
// 目标模型选正确的那个，这里只负责统一成 "4k"。
func normalizeResolution(s string) string {
	v := strings.ToLower(strings.TrimSpace(s))
	switch v {
	case "", "hd", "720", "720p":
		return "720p"
	case "sd", "480", "480p":
		return "480p"
	case "fhd", "1080", "1080p":
		return "1080p"
	case "4k", "2160", "2160p", "uhd":
		return "4k"
	}
	if w, h, ok := parseWxH(v); ok {
		switch {
		case w >= 3840 || h >= 2160:
			return "4k"
		case w >= 1920 || h >= 1080:
			return "1080p"
		case w >= 1280 || h >= 720:
			return "720p"
		default:
			return "480p"
		}
	}
	return v // 交给能力表判定，无法识别的会被明确拒绝
}

// resolutionFromModelName 从对外模型名的分辨率后缀推断档位，如
// sd-2.0-1080p → 1080p、sd-mini-720p → 720p。
//
// 🔑 这是**计费正确性**问题，不是便利功能：对外名按档位定价
// （sd-2.0-1080p 卖 ¥1.8/秒 vs sd-2.0-720p ¥0.88/秒），而上游的分辨率
// 是靠 resolution 参数带的。若只看请求参数（缺省 720p），客户会按 1080p
// 付费却拿到 720p 的片子。故模型名里的档位是权威。
func resolutionFromModelName(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	// 后缀优先级：长的先匹配，避免 "-2160p" 被 "-4k" 之外的规则截断
	for _, suf := range []string{"-2160p", "-1080p", "-720p", "-480p", "-4k", "-2k"} {
		if strings.HasSuffix(n, suf) {
			return normalizeResolution(strings.TrimPrefix(suf, "-"))
		}
	}
	return ""
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

// fitResolution 把归一后的档位翻译成目标模型认识的名字。
// 唯一需要翻译的是 4k → 2160p（pxv-standard 的叫法）。
func fitResolution(res string, c modelCaps) (string, bool) {
	if c.Resolutions[res] {
		return res, true
	}
	if res == "4k" && c.Resolutions["2160p"] {
		return "2160p", true
	}
	if res == "2160p" && c.Resolutions["4k"] {
		return "4k", true
	}
	return res, false
}

func sortedKeys(m map[string]bool) string {
	order := []string{"480p", "720p", "1080p", "2160p", "4k", "auto", "1:1", "16:9", "9:16", "4:3", "3:4", "21:9"}
	var out []string
	for _, k := range order {
		if m[k] {
			out = append(out, k)
		}
	}
	return strings.Join(out, " / ")
}

// ============================
// 原始 body 读取
// ============================

// rawBody 取原始请求体。自定义顶层字段（reference_*_urls / ratio / resolution）
// 不在 TaskSubmitReq 里，用 typed struct 重序列化会丢掉，必须读原始 body。
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
		if s := firstString(body, "resolution", "size"); s != "" {
			return s
		}
	}
	if s := strings.TrimSpace(req.Size); s != "" {
		return s
	}
	if req.Metadata != nil {
		if v, ok := req.Metadata["resolution"].(string); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// ============================
// 素材
// ============================

// materials 是归一后的参考素材。上游按 image_file_1..N 这样的**编号字段**接收。
type materials struct {
	Images []string
	Videos []string
	Audios []string
	// FirstLast 为 true 表示用户给了首/尾帧，须走 first_last_frames 模式
	FirstLast bool
}

func (m materials) empty() bool {
	return len(m.Images) == 0 && len(m.Videos) == 0 && len(m.Audios) == 0
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

// collectMaterials 收集参考素材。
// 对外契约与 meaicc 保持一致（统一各上游差异）：
//
//	reference_image_urls / reference_video_urls / reference_audio_urls
//	first_frame_url / last_frame_url
//	images（OpenAI 风格别名，等同 reference_image_urls）
func collectMaterials(c *gin.Context, req *relaycommon.TaskSubmitReq) materials {
	var m materials
	body := rawBody(c)

	if len(body) > 0 {
		// 首尾帧必须排在最前：上游按 image_file_1/2 的顺序理解首帧、尾帧
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

	// images / image 作为参考图别名（仅在没给显式字段时生效，避免重复）
	if m.empty() {
		m.Images = append(m.Images, req.Images...)
		if s := strings.TrimSpace(req.Image); s != "" {
			m.Images = append(m.Images, s)
		}
	}
	return m
}

func (m materials) allURLs() []string {
	out := make([]string, 0, len(m.Images)+len(m.Videos)+len(m.Audios))
	out = append(out, m.Images...)
	out = append(out, m.Videos...)
	out = append(out, m.Audios...)
	return out
}

// refRewrites 把对外统一的 @image1 / @video1 / @audio1 重写成上游认的
// @image_file_1 / @video_file_1 / @audio_file_1。
// 顺序重要：先处理已经是 _file_ 形式的（幂等，避免二次改写），再处理短形式。
var refRewrites = []struct {
	re   *regexp.Regexp
	tmpl string
}{
	{regexp.MustCompile(`@image_file_(\d+)`), "@image_file_$1"},
	{regexp.MustCompile(`@video_file_(\d+)`), "@video_file_$1"},
	{regexp.MustCompile(`@audio_file_(\d+)`), "@audio_file_$1"},
	{regexp.MustCompile(`@image(\d+)`), "@image_file_$1"},
	{regexp.MustCompile(`@img(\d+)`), "@image_file_$1"},
	{regexp.MustCompile(`@video(\d+)`), "@video_file_$1"},
	{regexp.MustCompile(`@audio(\d+)`), "@audio_file_$1"},
	{regexp.MustCompile(`@voice(\d+)`), "@audio_file_$1"},
	// 中文写法（meaicc 风格）也一并支持，方便用户在渠道间迁移
	{regexp.MustCompile(`@?图(\d+)`), "@image_file_$1"},
	{regexp.MustCompile(`@?视频(\d+)`), "@video_file_$1"},
	{regexp.MustCompile(`@?音频(\d+)`), "@audio_file_$1"},
}

func rewriteReferences(prompt string) string {
	for _, r := range refRewrites {
		prompt = r.re.ReplaceAllString(prompt, r.tmpl)
	}
	return prompt
}

// ============================
// 模型名解析
// ============================

// originModelOf 取对外模型名。
// ValidateRequestAndSetAction 在模型映射之前执行（relay_task.go 步骤 1 vs 2.5），
// info.OriginModelName 此刻可能仍为空，故回退到请求体的 model 字段。
func originModelOf(info *relaycommon.RelayInfo, req *relaycommon.TaskSubmitReq) string {
	if s := strings.TrimSpace(info.OriginModelName); s != "" {
		return s
	}
	return strings.TrimSpace(req.Model)
}

// upstreamModelOf 解析真正会发给上游的模型名。
//
// 🔑 260808 实测踩到：校验发生在 ModelMappedHelper **之前**，此时
// info.UpstreamModelName 还是对外名（如 sd-2.0-720p），拿它查 modelCapTable
// 永远查不到 → capsFor 返回 not-known → **全部前置校验静默失效**。
// 这里复用与 ModelMappedHelper 相同的数据源自行解析一次，保证校验在
// 预扣费之前生效（能返回 400，而不是扣了钱再报 500）。
func upstreamModelOf(c *gin.Context, origin string) string {
	mapping := c.GetString("model_mapping")
	if mapping == "" || mapping == "{}" {
		return origin
	}
	m := map[string]string{}
	if err := common.UnmarshalJsonStr(mapping, &m); err != nil {
		return origin
	}
	// 链式重定向 + 防环，与 ModelMappedHelper 行为一致
	cur := origin
	seen := map[string]bool{cur: true}
	for {
		next, ok := m[cur]
		if !ok || next == "" || seen[next] {
			return cur
		}
		seen[next] = true
		cur = next
	}
}

// ============================
// 请求校验
// ============================

// ValidateRequestAndSetAction 做**前置**校验。
//
// 上游对越界参数的处理不一致：有的直接拒（-2000），有的**静默降级**
// （请 1080p 给 720p，用户付了钱还不知道拿到的是降级货）。静默降级是最难
// 排查的一类问题，所以凡是能在本地判定的，一律在这里拦掉并说清原因。
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

	mats := collectMaterials(c, &req)

	// 上游只收公网 https 素材地址，不收 base64（实测 base64 会 500/被忽略）。
	for _, u := range mats.allURLs() {
		if strings.HasPrefix(u, "data:") {
			return service.TaskErrorWrapperLocal(
				errors.New("reference materials must be public http(s) URLs; base64 is not supported by this model"),
				"invalid_request", http.StatusBadRequest)
		}
	}

	// 首尾帧模式上游要求正好 2 张图
	if mats.FirstLast && len(mats.Images) != 2 {
		return service.TaskErrorWrapperLocal(
			errors.New("first/last frame mode requires exactly 2 images (first_frame_url and last_frame_url)"),
			"invalid_request", http.StatusBadRequest)
	}

	origin := originModelOf(info, &req)

	// 对外名带档位后缀时以它为准（决定计费的是它）；若用户又显式传了不一致的
	// resolution，宁可报错也不能默默二选一——两种选法都会造成"付的钱和拿到的货不符"。
	res := normalizeResolution(resolutionFromRequest(c, &req))
	if tier := resolutionFromModelName(origin); tier != "" {
		if raw := strings.TrimSpace(resolutionFromRequest(c, &req)); raw != "" && normalizeResolution(raw) != tier {
			return service.TaskErrorWrapperLocal(
				fmt.Errorf("model %s is billed at %s; requested resolution %s conflicts — use the model whose name matches the resolution you want",
					origin, tier, raw),
				"invalid_request", http.StatusBadRequest)
		}
		res = tier
	}

	// 以下校验依赖目标上游模型的能力表；模型未知时放行（新模型不至于被误拦）
	c1, known := capsFor(upstreamModelOf(c, origin))
	if !known {
		// ⚠️ 只在管线没定过的时候才设：ResolveOriginTask 会在**进重试循环之前**
		// 把 /v1/videos/:id/remix 标成 TaskActionRemix（relay_task.go:42），
		// 而 Validate 在那之后才跑。无条件赋值会把 remix 覆盖成普通生成。
		if info.Action == "" {
			info.Action = actionOf(mats)
		}
		c.Set("task_request", req)
		return nil
	}

	if _, ok := fitResolution(res, c1); !ok {
		return service.TaskErrorWrapperLocal(
			fmt.Errorf("model %s does not support resolution %s; supported: %s",
				origin, res, sortedKeys(c1.Resolutions)),
			"invalid_request", http.StatusBadRequest)
	}

	if r := strings.TrimSpace(ratioFromRequest(c, &req)); r != "" && !c1.Ratios[r] {
		return service.TaskErrorWrapperLocal(
			fmt.Errorf("model %s does not support ratio %s; supported: %s",
				origin, r, sortedKeys(c1.Ratios)),
			"invalid_request", http.StatusBadRequest)
	}

	// dvc 系列只有 first_last_frames：收不了参考视频/音频，也做不了多图 @引用锁脸。
	// 让它静默丢掉素材是最坏的结果（照常扣费、成片与素材无关），必须明确报错。
	if !c1.OmniRef {
		if len(mats.Videos) > 0 || len(mats.Audios) > 0 {
			return service.TaskErrorWrapperLocal(
				fmt.Errorf("model %s does not accept reference video/audio; use a model that supports multi-material reference",
					origin),
				"invalid_request", http.StatusBadRequest)
		}
		if len(mats.Images) > 2 {
			return service.TaskErrorWrapperLocal(
				fmt.Errorf("model %s accepts at most 2 reference images (first/last frame); use a model that supports multi-material reference",
					origin),
				"invalid_request", http.StatusBadRequest)
		}
	}

	// ⚠️ 只在管线没定过的时候才设：ResolveOriginTask 会在**进重试循环之前**
	// 把 /v1/videos/:id/remix 标成 TaskActionRemix（relay_task.go:42），
	// 而 Validate 在那之后才跑。无条件赋值会把 remix 覆盖成普通生成。
	if info.Action == "" {
		info.Action = actionOf(mats)
	}
	c.Set("task_request", req)
	return nil
}

// actionOf 决定消费日志里的「操作」字段。不设它的话
// service/task_billing.go 会写成 "操作 ，..."，后台看不出这单有没有带素材
// —— 而同一个对外模型名在不同上游的素材支持度并不一样，排查时必须先知道这个。
func actionOf(mats materials) string {
	switch {
	case mats.FirstLast:
		return constant.TaskActionFirstTailGenerate
	case len(mats.Videos) > 0:
		return constant.TaskActionRemix
	case !mats.empty():
		return constant.TaskActionReferenceGenerate
	default:
		return constant.TaskActionTextGenerate
	}
}

// ============================
// 请求构造
// ============================

func (a *TaskAdaptor) BuildRequestURL(info *relaycommon.RelayInfo) (string, error) {
	return fmt.Sprintf("%s/v1/videos/generations", a.baseURL), nil
}

func (a *TaskAdaptor) BuildRequestHeader(c *gin.Context, req *http.Request, info *relaycommon.RelayInfo) error {
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	req.Header.Set("Content-Type", "application/json")
	return nil
}

// buildPayload 组请求体。上游的素材字段是**编号顶层键**，无法用固定 struct 表达，
// 故用 map 动态拼。
func buildPayload(c *gin.Context, req *relaycommon.TaskSubmitReq, upstreamModel, originModel string) map[string]any {
	mats := collectMaterials(c, req)
	c1, known := capsFor(upstreamModel)

	res := normalizeResolution(resolutionFromRequest(c, req))
	// 对外名的档位后缀是权威（它决定计费），见 resolutionFromModelName
	if tier := resolutionFromModelName(originModel); tier != "" {
		res = tier
	}
	if known {
		if fitted, ok := fitResolution(res, c1); ok {
			res = fitted
		}
	}

	ratio := strings.TrimSpace(ratioFromRequest(c, req))
	if ratio == "" {
		ratio = defaultRatio
		// rlm 只收 16:9/9:16，默认值恰好合法；其余模型也都支持 16:9
	}

	mode := modeOmni
	switch {
	case mats.FirstLast:
		mode = modeFirstLast
	case known && !c1.OmniRef:
		// dvc 系列没有 omni_reference，纯文生/图生也必须声明 first_last_frames
		mode = modeFirstLast
	}

	payload := map[string]any{
		"model":        upstreamModel,
		"prompt":       rewriteReferences(req.Prompt),
		"ratio":        ratio,
		"resolution":   res,
		"duration":     resolveDuration(req),
		"functionMode": mode,
	}

	for i, u := range mats.Images {
		payload[fmt.Sprintf("image_file_%d", i+1)] = u
	}
	for i, u := range mats.Videos {
		payload[fmt.Sprintf("video_file_%d", i+1)] = u
	}
	for i, u := range mats.Audios {
		payload[fmt.Sprintf("audio_file_%d", i+1)] = u
	}
	return payload
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

	data, err := common.Marshal(buildPayload(c, &req, info.UpstreamModelName, info.OriginModelName))
	if err != nil {
		return nil, errors.Wrap(err, "marshal dimensio request failed")
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

	// 错误形态：code 为负数
	if up.Code < 0 {
		return "", nil, service.TaskErrorWrapper(errors.New(friendlyReason(up.Code, up.failureText())),
			"upstream_rejected", http.StatusBadRequest)
	}
	if up.TaskID == "" {
		return "", nil, service.TaskErrorWrapper(errors.New("task_id is empty"),
			"invalid_response", http.StatusInternalServerError)
	}

	// 回给客户端的是公开任务号，且不暴露上游 provider 代号（effective_model 会
	// 泄露 dvc/rlm/pxv 这类内部来源）。
	out := map[string]any{
		"id":         info.PublicTaskID,
		"task_id":    info.PublicTaskID,
		"object":     "video",
		"model":      info.OriginModelName,
		"status":     up.Status,
		"created_at": up.Created,
	}
	c.JSON(http.StatusOK, out)
	return up.TaskID, responseBody, nil
}

// ============================
// 轮询
// ============================

func (a *TaskAdaptor) FetchTask(baseUrl, key string, body map[string]any, proxy string) (*http.Response, error) {
	taskID, ok := body["task_id"].(string)
	if !ok {
		return nil, errors.New("invalid task_id")
	}
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/v1/videos/tasks/%s", baseUrl, taskID), nil)
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

	// 查询阶段也可能直接返回错误对象
	if up.Code < 0 {
		info.Status = model.TaskStatusFailure
		info.Reason = friendlyReason(up.Code, up.failureText())
		return info, nil
	}

	switch strings.ToLower(strings.TrimSpace(up.Status)) {
	case "completed", "success", "succeeded":
		info.Status = model.TaskStatusSuccess
		// 成片直链交给 /v1/videos/{id}/content 代理按需拉取
		// （video_proxy 的 default 分支读 PrivateData.ResultURL）。
		info.Url = up.Result.URL
	case "failed", "cancelled", "canceled":
		info.Status = model.TaskStatusFailure
		info.Reason = friendlyReason(up.Code, up.failureText())
	case "processing", "running", "in_progress":
		info.Status = model.TaskStatusInProgress
	case "submitted", "pending", "queued":
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
	if data, err = sjson.SetBytes(data, "task_id", task.TaskID); err != nil {
		return nil, errors.Wrap(err, "set task_id failed")
	}

	// 成片直链是 storage.googleapis.com/davinciweb-*，会暴露上游身份——换成本站代理地址。
	proxyURL := taskcommon.BuildProxyURL(task.TaskID)
	for _, path := range []string{"result.url", "url", "video_url", "object", "metadata.url"} {
		if gjson.GetBytes(data, path).Exists() {
			if data, err = sjson.SetBytes(data, path, proxyURL); err != nil {
				return nil, errors.Wrapf(err, "set %s failed", path)
			}
		}
	}

	// requested_model / effective_model 会泄露上游 provider 代号（dvc/rlm/pxv…），
	// 一律抹掉；model 还原成用户请求的对外名。
	// ⚠️ 同一批字段上游有 snake_case 与 camelCase 两种写法（提交/查询两条链路不一致），
	// 必须都删。260808 实测只删了 snake_case，结果查询响应里 `creditsConsumed: 208`
	// 原样返给了客户——那是**我们的进货成本**，据此能直接算出毛利。
	for _, path := range []string{
		"requested_model", "effective_model", "credits_consumed",
		"requestedModel", "effectiveModel", "creditsConsumed", "creditsReserved",
		"upstreamTaskId", "jobId", "recordSource", "failureType",
	} {
		if gjson.GetBytes(data, path).Exists() {
			if data, err = sjson.DeleteBytes(data, path); err != nil {
				return nil, errors.Wrapf(err, "delete %s failed", path)
			}
		}
	}
	if origin := task.Properties.OriginModelName; origin != "" {
		if data, err = sjson.SetBytes(data, "model", origin); err != nil {
			return nil, errors.Wrap(err, "set model failed")
		}
	}

	// 失败时上游原文对用户没有意义；task.FailReason 已在 ParseTaskResult 翻译过。
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
