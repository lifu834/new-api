package adobe2api

// ModelList is the default catalogue shown in the channel UI.  Channels store
// their own `models` column, so this is only a convenience default.
//
// Resolution / audio / duration are all *parameters* — the per-second cost they
// imply is resolved in EstimateBilling, which is the whole reason this adaptor
// exists instead of reusing the Sora one (its size-ratio table is hardcoded to
// the OpenAI geometries and cannot express Adobe's tiers).
var ModelList = []string{
	"kling3",
	"kling-o3",
	"kling-v2v",
	"veo31",
	"veo31-fast",
	"veo31-ref",
}

var ChannelName = "adobe2api-video"
