package constant

import "testing"

func TestIsTaskRelayMode(t *testing.T) {
	cases := []struct {
		name      string
		relayMode int
		want      bool
	}{
		// Task-submit / task-fetch modes served by controller.RelayTask(Fetch).
		{"suno submit", RelayModeSunoSubmit, true},
		{"suno fetch", RelayModeSunoFetch, true},
		{"suno fetch by id", RelayModeSunoFetchByID, true},
		{"video submit", RelayModeVideoSubmit, true},
		{"video fetch by id", RelayModeVideoFetchByID, true},
		{"image async submit", RelayModeImageAsyncSubmit, true},
		{"image async fetch by id", RelayModeImageAsyncFetchByID, true},

		// Synchronous relay modes — must NOT be treated as task.
		{"unknown/default", RelayModeUnknown, false},
		{"chat completions", RelayModeChatCompletions, false},
		{"completions", RelayModeCompletions, false},
		{"embeddings", RelayModeEmbeddings, false},
		{"moderations", RelayModeModerations, false},
		{"sync images generations", RelayModeImagesGenerations, false},
		{"sync images edits", RelayModeImagesEdits, false},
		{"responses", RelayModeResponses, false},
		{"gemini", RelayModeGemini, false},
		{"rerank", RelayModeRerank, false},
		{"audio speech", RelayModeAudioSpeech, false},
		// Midjourney has its own relay path and non-task channels; must stay false.
		{"midjourney imagine", RelayModeMidjourneyImagine, false},
	}
	for _, tc := range cases {
		if got := IsTaskRelayMode(tc.relayMode); got != tc.want {
			t.Errorf("IsTaskRelayMode(%s=%d) = %v, want %v", tc.name, tc.relayMode, got, tc.want)
		}
	}
}
