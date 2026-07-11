package constant

var StreamingTimeout int
var DifyDebug bool
var MaxFileDownloadMB int
var StreamScannerMaxBufferMB int
var ForceStreamOption bool
var CountToken bool
var GetMediaToken bool
var GetMediaTokenNotStream bool
var UpdateTask bool
var MaxRequestBodyMB int
var AzureDefaultAPIVersion string
var NotifyLimitCount int
var NotificationLimitDurationMinute int
var GenerateDefaultToken bool
var ErrorLogEnabled bool
var TaskQueryLimit int
var TaskTimeoutMinutes int

// TaskPollIntervalSeconds 异步任务轮询循环的休眠间隔（秒）。默认 15。
// 轮询是 webhook 回调的兜底机制；部署 webhook 后可调大（如 60）以降压。
var TaskPollIntervalSeconds int

// TaskCallbackBaseURL 是 new-api 自身对外可达的 webhook 基址（供上游 chatgpt2api
// 通过内网/tailnet 回调）。为空时不下发 callback_url，保持纯轮询的向后兼容。
var TaskCallbackBaseURL string

// TaskCallbackSecret 是 webhook 回调的共享密钥，随 callback_url 下发并在接收端做
// 常量时间比对。为空时接收端 fail closed（拒绝所有回调）。
var TaskCallbackSecret string

// temporary variable for sora patch, will be removed in future
var TaskPricePatches []string

// TrustedRedirectDomains is a list of trusted domains for redirect URL validation.
// Domains support subdomain matching (e.g., "example.com" matches "sub.example.com").
var TrustedRedirectDomains []string
