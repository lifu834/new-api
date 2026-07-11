package service

import (
	"errors"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
)

// channelTypeHasTaskAdaptor reports whether a channel type exposes an
// asynchronous task adaptor (i.e. it can serve /v1/.../async task-submit
// endpoints via controller.RelayTask).
//
// It reuses the injected GetTaskAdaptorFunc (set in main.go to
// relay.GetTaskAdaptor) to avoid importing the relay package here. The task
// adaptor registry keys most platforms on the numeric channel type as a string
// ("58", "50", ...), except Suno which is keyed on the platform string "suno";
// that one is handled explicitly because its numeric type would otherwise miss.
func channelTypeHasTaskAdaptor(channelType int) bool {
	if channelType == constant.ChannelTypeSunoAPI {
		return true
	}
	if GetTaskAdaptorFunc == nil {
		return false
	}
	return GetTaskAdaptorFunc(constant.TaskPlatform(strconv.Itoa(channelType))) != nil
}

// channelTypeIsTaskOnly reports whether a channel type is a task-only channel:
// it has a task adaptor but NO synchronous relay adaptor. Such channels (e.g.
// ChatGPT2ApiImage=58, Kling, Vidu, Sora, Suno) can only serve async task
// endpoints and would break a synchronous request.
//
// Channel types that expose BOTH a task adaptor and a synchronous adaptor
// (e.g. OpenAI=1, Gemini=24, Ali=17, VolcEngine=45, MiniMax=35, which also back
// Sora/other video-submit tasks) are deliberately NOT considered task-only:
// they must remain selectable for synchronous requests. common.ChannelType2APIType
// returns ok=true exactly for channel types that have a synchronous adaptor.
func channelTypeIsTaskOnly(channelType int) bool {
	if !channelTypeHasTaskAdaptor(channelType) {
		return false
	}
	_, hasSyncAdaptor := common.ChannelType2APIType(channelType)
	return !hasSyncAdaptor
}

// imageAsyncChannelTypes is the allow-set of channel types whose task adaptor
// actually implements the async image-generation endpoints
// (/v1/images/async/*). This is deliberately an extensible set rather than a
// bare "== 58" so that adding another image-async provider later is a one-line
// change.
//
// Note: several dual-capability channel types (OpenAI=1, Gemini=24, ...) DO
// expose a task adaptor, but theirs handle *video* (sora) or other task kinds,
// not image-async — so they must NOT appear here.
var imageAsyncChannelTypes = map[int]bool{
	constant.ChannelTypeChatGPT2ApiImage: true, // 58
}

// channelTypeSupportsImageAsync reports whether a channel type can serve async
// image-generation task requests.
func channelTypeSupportsImageAsync(channelType int) bool {
	return imageAsyncChannelTypes[channelType]
}

// buildChannelTypeFilter derives a channel-type predicate from the request's
// relay mode so that channel selection matches the request's endpoint category.
//
//   - Image-async task relay mode => keep only channels whose task adaptor
//     actually handles image-async (channelTypeSupportsImageAsync). This
//     excludes video/suno task channels AND dual channels like OpenAI (type 1),
//     whose only task adaptor is Sora video — so an image-async submit can never
//     be routed to a sync/video channel that shares the same model.
//   - Other task relay mode (video, suno) => keep any channel exposing a task
//     adaptor (channelTypeHasTaskAdaptor). Deliberately broad: type-1→sora video,
//     Kling, Jimeng, etc. must all stay selectable.
//   - Any non-task relay mode => exclude task-only channels
//     (channels with a task adaptor but no synchronous adaptor).
//
// The video/suno and non-task branches never exclude a channel that is
// currently able to serve the request, so existing routing for every model on a
// single channel-type is unchanged; only the both-types-serve-the-same-model
// case is disambiguated. If relay_mode is unset/unknown the mode is treated as
// non-task (safe default).
func buildChannelTypeFilter(ctx *gin.Context) func(channelType int) bool {
	relayMode := 0
	if ctx != nil {
		relayMode = ctx.GetInt("relay_mode")
	}
	if relayconstant.IsTaskRelayMode(relayMode) {
		if isImageAsyncRelayMode(relayMode) {
			return channelTypeSupportsImageAsync
		}
		return channelTypeHasTaskAdaptor
	}
	return func(channelType int) bool {
		return !channelTypeIsTaskOnly(channelType)
	}
}

// isImageAsyncRelayMode reports whether the relay mode is one of the async
// image-generation task modes.
func isImageAsyncRelayMode(relayMode int) bool {
	switch relayMode {
	case relayconstant.RelayModeImageAsyncSubmit,
		relayconstant.RelayModeImageAsyncFetchByID:
		return true
	default:
		return false
	}
}

type RetryParam struct {
	Ctx          *gin.Context
	TokenGroup   string
	ModelName    string
	Retry        *int
	resetNextTry bool
}

func (p *RetryParam) GetRetry() int {
	if p.Retry == nil {
		return 0
	}
	return *p.Retry
}

func (p *RetryParam) SetRetry(retry int) {
	p.Retry = &retry
}

func (p *RetryParam) IncreaseRetry() {
	if p.resetNextTry {
		p.resetNextTry = false
		return
	}
	if p.Retry == nil {
		p.Retry = new(int)
	}
	*p.Retry++
}

func (p *RetryParam) ResetRetryNextTry() {
	p.resetNextTry = true
}

// CacheGetRandomSatisfiedChannel tries to get a random channel that satisfies the requirements.
// 尝试获取一个满足要求的随机渠道。
//
// For "auto" tokenGroup with cross-group Retry enabled:
// 对于启用了跨分组重试的 "auto" tokenGroup：
//
//   - Each group will exhaust all its priorities before moving to the next group.
//     每个分组会用完所有优先级后才会切换到下一个分组。
//
//   - Uses ContextKeyAutoGroupIndex to track current group index.
//     使用 ContextKeyAutoGroupIndex 跟踪当前分组索引。
//
//   - Uses ContextKeyAutoGroupRetryIndex to track the global Retry count when current group started.
//     使用 ContextKeyAutoGroupRetryIndex 跟踪当前分组开始时的全局重试次数。
//
//   - priorityRetry = Retry - startRetryIndex, represents the priority level within current group.
//     priorityRetry = Retry - startRetryIndex，表示当前分组内的优先级级别。
//
//   - When GetRandomSatisfiedChannel returns nil (priorities exhausted), moves to next group.
//     当 GetRandomSatisfiedChannel 返回 nil（优先级用完）时，切换到下一个分组。
//
// Example flow (2 groups, each with 2 priorities, RetryTimes=3):
// 示例流程（2个分组，每个有2个优先级，RetryTimes=3）：
//
//	Retry=0: GroupA, priority0 (startRetryIndex=0, priorityRetry=0)
//	         分组A, 优先级0
//
//	Retry=1: GroupA, priority1 (startRetryIndex=0, priorityRetry=1)
//	         分组A, 优先级1
//
//	Retry=2: GroupA exhausted → GroupB, priority0 (startRetryIndex=2, priorityRetry=0)
//	         分组A用完 → 分组B, 优先级0
//
//	Retry=3: GroupB, priority1 (startRetryIndex=2, priorityRetry=1)
//	         分组B, 优先级1
func CacheGetRandomSatisfiedChannel(param *RetryParam) (*model.Channel, string, error) {
	var channel *model.Channel
	var err error
	selectGroup := param.TokenGroup
	userGroup := common.GetContextKeyString(param.Ctx, constant.ContextKeyUserGroup)

	// Relay-mode-aware candidate filter: ensures a task-submit request is routed
	// to a task-type channel and every other request to a synchronous channel.
	// Computed once from the request's relay mode; passed to both selection paths
	// (auto-group loop and single-group) below.
	channelFilter := buildChannelTypeFilter(param.Ctx)

	if param.TokenGroup == "auto" {
		if len(setting.GetAutoGroups()) == 0 {
			return nil, selectGroup, errors.New("auto groups is not enabled")
		}
		autoGroups := GetUserAutoGroup(userGroup)

		// startGroupIndex: the group index to start searching from
		// startGroupIndex: 开始搜索的分组索引
		startGroupIndex := 0
		crossGroupRetry := common.GetContextKeyBool(param.Ctx, constant.ContextKeyTokenCrossGroupRetry)

		if lastGroupIndex, exists := common.GetContextKey(param.Ctx, constant.ContextKeyAutoGroupIndex); exists {
			if idx, ok := lastGroupIndex.(int); ok {
				startGroupIndex = idx
			}
		}

		for i := startGroupIndex; i < len(autoGroups); i++ {
			autoGroup := autoGroups[i]
			// Calculate priorityRetry for current group
			// 计算当前分组的 priorityRetry
			priorityRetry := param.GetRetry()
			// If moved to a new group, reset priorityRetry and update startRetryIndex
			// 如果切换到新分组，重置 priorityRetry 并更新 startRetryIndex
			if i > startGroupIndex {
				priorityRetry = 0
			}
			logger.LogDebug(param.Ctx, "Auto selecting group: %s, priorityRetry: %d", autoGroup, priorityRetry)

			channel, _ = model.GetRandomSatisfiedChannel(autoGroup, param.ModelName, priorityRetry, channelFilter)
			if channel == nil {
				// Current group has no available channel for this model, try next group
				// 当前分组没有该模型的可用渠道，尝试下一个分组
				logger.LogDebug(param.Ctx, "No available channel in group %s for model %s at priorityRetry %d, trying next group", autoGroup, param.ModelName, priorityRetry)
				// 重置状态以尝试下一个分组
				common.SetContextKey(param.Ctx, constant.ContextKeyAutoGroupIndex, i+1)
				common.SetContextKey(param.Ctx, constant.ContextKeyAutoGroupRetryIndex, 0)
				// Reset retry counter so outer loop can continue for next group
				// 重置重试计数器，以便外层循环可以为下一个分组继续
				param.SetRetry(0)
				continue
			}
			common.SetContextKey(param.Ctx, constant.ContextKeyAutoGroup, autoGroup)
			selectGroup = autoGroup
			logger.LogDebug(param.Ctx, "Auto selected group: %s", autoGroup)

			// Prepare state for next retry
			// 为下一次重试准备状态
			if crossGroupRetry && priorityRetry >= common.RetryTimes {
				// Current group has exhausted all retries, prepare to switch to next group
				// This request still uses current group, but next retry will use next group
				// 当前分组已用完所有重试次数，准备切换到下一个分组
				// 本次请求仍使用当前分组，但下次重试将使用下一个分组
				logger.LogDebug(param.Ctx, "Current group %s retries exhausted (priorityRetry=%d >= RetryTimes=%d), preparing switch to next group for next retry", autoGroup, priorityRetry, common.RetryTimes)
				common.SetContextKey(param.Ctx, constant.ContextKeyAutoGroupIndex, i+1)
				// Reset retry counter so outer loop can continue for next group
				// 重置重试计数器，以便外层循环可以为下一个分组继续
				param.SetRetry(0)
				param.ResetRetryNextTry()
			} else {
				// Stay in current group, save current state
				// 保持在当前分组，保存当前状态
				common.SetContextKey(param.Ctx, constant.ContextKeyAutoGroupIndex, i)
			}
			break
		}
	} else {
		channel, err = model.GetRandomSatisfiedChannel(param.TokenGroup, param.ModelName, param.GetRetry(), channelFilter)
		if err != nil {
			return nil, param.TokenGroup, err
		}
	}
	return channel, selectGroup, nil
}
