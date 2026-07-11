package service

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/gin-gonic/gin"
)

// stubTaskAdaptor is a no-op TaskPollingAdaptor used only to return a non-nil
// value from a stubbed GetTaskAdaptorFunc.
type stubTaskAdaptor struct{}

func (stubTaskAdaptor) Init(info *relaycommon.RelayInfo) {}
func (stubTaskAdaptor) FetchTask(baseURL string, key string, body map[string]any, proxy string) (*http.Response, error) {
	return nil, nil
}
func (stubTaskAdaptor) ParseTaskResult(body []byte) (*relaycommon.TaskInfo, error) { return nil, nil }
func (stubTaskAdaptor) AdjustBillingOnComplete(task *model.Task, taskResult *relaycommon.TaskInfo) int {
	return 0
}

// withStubbedTaskAdaptors installs a GetTaskAdaptorFunc that mirrors the real
// relay.GetTaskAdaptor registry: non-nil for every channel type / platform that
// exposes a task adaptor, nil otherwise. Restores the original on cleanup.
func withStubbedTaskAdaptors(t *testing.T) {
	t.Helper()
	// Channel types that GetTaskAdaptor maps to a task adaptor (see
	// relay/relay_adaptor.go). Includes both task-only types (58/50/52/55/54)
	// and dual-capability types that ALSO have a synchronous adaptor
	// (1/24/17/45/35/41/51).
	taskTypes := map[string]bool{
		strconv.Itoa(constant.ChannelTypeChatGPT2ApiImage): true, // 58 task-only
		strconv.Itoa(constant.ChannelTypeKling):            true, // 50 task-only
		strconv.Itoa(constant.ChannelTypeVidu):             true, // 52 task-only
		strconv.Itoa(constant.ChannelTypeSora):             true, // 55 task-only
		strconv.Itoa(constant.ChannelTypeDoubaoVideo):      true, // 54 task-only
		strconv.Itoa(constant.ChannelTypeOpenAI):           true, // 1  dual (sora video)
		strconv.Itoa(constant.ChannelTypeGemini):           true, // 24 dual
		strconv.Itoa(constant.ChannelTypeAli):              true, // 17 dual
		strconv.Itoa(constant.ChannelTypeVolcEngine):       true, // 45 dual
		strconv.Itoa(constant.ChannelTypeMiniMax):          true, // 35 dual
		strconv.Itoa(constant.ChannelTypeVertexAi):         true, // 41 dual
		strconv.Itoa(constant.ChannelTypeJimeng):           true, // 51 dual
		string(constant.TaskPlatformSuno):                  true, // "suno"
	}
	orig := GetTaskAdaptorFunc
	t.Cleanup(func() { GetTaskAdaptorFunc = orig })
	GetTaskAdaptorFunc = func(platform constant.TaskPlatform) TaskPollingAdaptor {
		if taskTypes[string(platform)] {
			return stubTaskAdaptor{}
		}
		return nil
	}
}

func TestChannelTypeHasTaskAdaptor(t *testing.T) {
	withStubbedTaskAdaptors(t)
	cases := []struct {
		channelType int
		want        bool
	}{
		{constant.ChannelTypeChatGPT2ApiImage, true}, // 58
		{constant.ChannelTypeKling, true},            // 50
		{constant.ChannelTypeSunoAPI, true},          // 36 (special-cased, keyed on "suno")
		{constant.ChannelTypeOpenAI, true},           // 1 dual — has a task adaptor
		{constant.ChannelTypeGemini, true},           // 24 dual
		{constant.ChannelTypeAnthropic, false},       // 14 sync-only chat
		{constant.ChannelTypeMidjourney, false},      // 2 own relay, no task adaptor
		{constant.ChannelTypeUnknown, false},         // 0
	}
	for _, tc := range cases {
		if got := channelTypeHasTaskAdaptor(tc.channelType); got != tc.want {
			t.Errorf("channelTypeHasTaskAdaptor(%d) = %v, want %v", tc.channelType, got, tc.want)
		}
	}
}

func TestChannelTypeHasTaskAdaptor_NilFunc(t *testing.T) {
	orig := GetTaskAdaptorFunc
	t.Cleanup(func() { GetTaskAdaptorFunc = orig })
	GetTaskAdaptorFunc = nil
	// Suno is special-cased and must still report true even without injection.
	if !channelTypeHasTaskAdaptor(constant.ChannelTypeSunoAPI) {
		t.Errorf("channelTypeHasTaskAdaptor(suno) should be true even with nil GetTaskAdaptorFunc")
	}
	// Everything else must degrade safely to false (no panic).
	if channelTypeHasTaskAdaptor(constant.ChannelTypeOpenAI) {
		t.Errorf("channelTypeHasTaskAdaptor(openai) should be false with nil GetTaskAdaptorFunc")
	}
}

func TestChannelTypeIsTaskOnly(t *testing.T) {
	withStubbedTaskAdaptors(t)
	cases := []struct {
		name        string
		channelType int
		want        bool
	}{
		// Task-only: has a task adaptor AND no synchronous adaptor.
		{"chatgpt2api image (58)", constant.ChannelTypeChatGPT2ApiImage, true},
		{"kling (50)", constant.ChannelTypeKling, true},
		{"vidu (52)", constant.ChannelTypeVidu, true},
		{"sora (55)", constant.ChannelTypeSora, true},
		{"doubao video (54)", constant.ChannelTypeDoubaoVideo, true},
		{"suno (36)", constant.ChannelTypeSunoAPI, true},

		// Dual-capability: task adaptor + synchronous adaptor => NOT task-only.
		// These must remain selectable for chat/embeddings/sync-image requests.
		{"openai (1)", constant.ChannelTypeOpenAI, false},
		{"gemini (24)", constant.ChannelTypeGemini, false},
		{"ali (17)", constant.ChannelTypeAli, false},
		{"volcengine (45)", constant.ChannelTypeVolcEngine, false},
		{"minimax (35)", constant.ChannelTypeMiniMax, false},
		{"vertex (41)", constant.ChannelTypeVertexAi, false},
		{"jimeng (51)", constant.ChannelTypeJimeng, false},

		// No task adaptor at all => never task-only.
		{"anthropic (14)", constant.ChannelTypeAnthropic, false},
		{"midjourney (2)", constant.ChannelTypeMidjourney, false},
		{"unknown (0)", constant.ChannelTypeUnknown, false},
	}
	for _, tc := range cases {
		if got := channelTypeIsTaskOnly(tc.channelType); got != tc.want {
			t.Errorf("channelTypeIsTaskOnly(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestBuildChannelTypeFilter validates the wantTask determination per relay mode
// and the resulting keep/exclude decision on representative channel types.
func TestBuildChannelTypeFilter(t *testing.T) {
	withStubbedTaskAdaptors(t)

	newCtx := func(relayMode int, set bool) *gin.Context {
		c := &gin.Context{}
		if set {
			c.Set("relay_mode", relayMode)
		}
		return c
	}

	type keep struct {
		channelType int
		want        bool
	}
	cases := []struct {
		name      string
		relayMode int
		set       bool
		keeps     []keep
	}{
		{
			// Image-async is NARROWED to the image-async allow-set: type-1
			// (OpenAI/sora video) and Kling/Suno must be excluded so the submit
			// can only land on a genuinely image-async-capable channel (58).
			name:      "image-async submit narrows to image-async allow-set",
			relayMode: relayconstant.RelayModeImageAsyncSubmit,
			set:       true,
			keeps: []keep{
				{constant.ChannelTypeChatGPT2ApiImage, true}, // 58 async image target
				{constant.ChannelTypeOpenAI, false},          // 1 dual → sora video only, EXCLUDED
				{constant.ChannelTypeKling, false},           // 50 video task, EXCLUDED
				{constant.ChannelTypeSunoAPI, false},         // 36 suno task, EXCLUDED
				{constant.ChannelTypeAnthropic, false},       // 14 sync-only, EXCLUDED
				{constant.ChannelTypeMidjourney, false},      // 2, EXCLUDED
			},
		},
		{
			name:      "image-async fetch-by-id narrows to image-async allow-set",
			relayMode: relayconstant.RelayModeImageAsyncFetchByID,
			set:       true,
			keeps: []keep{
				{constant.ChannelTypeChatGPT2ApiImage, true},
				{constant.ChannelTypeOpenAI, false},
			},
		},
		{
			// Video stays BROAD: any task-adaptor channel is kept.
			name:      "video submit keeps task channels (broad)",
			relayMode: relayconstant.RelayModeVideoSubmit,
			set:       true,
			keeps: []keep{
				{constant.ChannelTypeKling, true},
				{constant.ChannelTypeOpenAI, true}, // sora video on openai kept
				{constant.ChannelTypeAnthropic, false},
			},
		},
		{
			// Suno stays BROAD.
			name:      "suno submit keeps suno task channel (broad)",
			relayMode: relayconstant.RelayModeSunoSubmit,
			set:       true,
			keeps: []keep{
				{constant.ChannelTypeSunoAPI, true},
				{constant.ChannelTypeAnthropic, false},
			},
		},
		{
			name:      "sync image excludes task-only, keeps sync channels",
			relayMode: relayconstant.RelayModeImagesGenerations,
			set:       true,
			keeps: []keep{
				{constant.ChannelTypeChatGPT2ApiImage, false}, // 58 excluded (task-only)
				{constant.ChannelTypeOpenAI, true},            // 1 sync image channel kept
				{constant.ChannelTypeAnthropic, true},         // sync channel kept
				{constant.ChannelTypeMidjourney, true},        // non-task kept
			},
		},
		{
			name:      "chat keeps all sync/dual channels, excludes task-only",
			relayMode: relayconstant.RelayModeChatCompletions,
			set:       true,
			keeps: []keep{
				{constant.ChannelTypeOpenAI, true},
				{constant.ChannelTypeGemini, true},
				{constant.ChannelTypeAnthropic, true},
				{constant.ChannelTypeKling, false}, // task-only excluded
			},
		},
		{
			name:      "unset relay mode defaults to non-task (excludes task-only)",
			relayMode: 0,
			set:       false,
			keeps: []keep{
				{constant.ChannelTypeOpenAI, true},
				{constant.ChannelTypeChatGPT2ApiImage, false},
			},
		},
	}

	for _, tc := range cases {
		filter := buildChannelTypeFilter(newCtx(tc.relayMode, tc.set))
		if filter == nil {
			t.Fatalf("%s: buildChannelTypeFilter returned nil", tc.name)
		}
		for _, k := range tc.keeps {
			if got := filter(k.channelType); got != k.want {
				t.Errorf("%s: filter(channelType=%d) = %v, want %v", tc.name, k.channelType, got, k.want)
			}
		}
	}
}
