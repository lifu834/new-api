package chatgpt2api

// ChannelName is the display name of this task channel.
var ChannelName = "chatgpt2api-image"

// ModelList enumerates the async image-generation models served by this channel.
var ModelList = []string{
	"gpt-image-2",
	"ex-gpt-image-2",
}

// DefaultBaseURL is the fallback base URL for the upstream chatgpt2api service.
// This is only a placeholder default — the real endpoint MUST be configured
// per-channel in the admin dashboard (chatgpt2api is an internal / self-hosted
// OpenAI-compatible service, so there is no canonical public host).
const DefaultBaseURL = "https://chatgpt.com"
