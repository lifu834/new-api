package adobe2api

import "testing"

// Ground truth measured against Adobe's bks.adobe.io/v2/credits/cost endpoint.
// Cheapest tier per model is the ModelPrice baseline, so tier ratio =
// actualCreditsPerSecond / baselineCreditsPerSecond.
func TestTierRatioMatchesMeasuredCredits(t *testing.T) {
	cases := []struct {
		name      string
		model     string
		size      string
		audio     bool
		hasVideo  bool
		baseline  float64 // credits/s of the model's cheapest tier
		wantCosts float64 // credits/s Adobe actually charges
	}{
		// --- text/image to video ---
		{"kling3 720p mute", "kling3", "1280x720", false, false, 20, 20},
		{"kling3 720p audio", "kling3", "1280x720", true, false, 20, 25},
		{"kling3 1080p mute", "kling3", "1920x1080", false, false, 20, 25},
		{"kling3 1080p audio", "kling3", "1920x1080", true, false, 20, 35},
		{"kling3 vertical 1080p audio", "kling3", "1080x1920", true, false, 20, 35},
		{"kling-o3 720p mute", "kling-o3", "1280x720", false, false, 20, 20},
		{"kling-o3 1080p audio", "kling-o3", "1920x1080", true, false, 20, 35},
		// --- video to video (same model name, source clip supplied) ---
		{"kling-o3 v2v 720p", "kling-o3", "1280x720", true, true, 20, 30},
		{"kling-o3 v2v 720p mute", "kling-o3", "1280x720", false, true, 20, 30},
		{"kling-o3 v2v 1080p", "kling-o3", "1920x1080", true, true, 20, 40},
		{"kling-o3 v2v 1080p mute", "kling-o3", "1920x1080", false, true, 20, 40},
		// --- veo: flat, nothing scales ---
		{"veo31 flat 720p", "veo31", "1280x720", true, false, 50, 50},
		{"veo31 flat 1080p mute", "veo31", "1920x1080", false, false, 50, 50},
		{"veo31-fast flat", "veo31-fast", "1920x1080", true, false, 10, 10},
		{"veo31-ref flat", "veo31-ref", "1920x1080", false, false, 50, 50},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tierRatio(tc.model, tc.size, tc.audio, tc.hasVideo) * tc.baseline
			if got != tc.wantCosts {
				t.Fatalf("%s: billed %.4f credits/s, Adobe charges %.4f",
					tc.name, got, tc.wantCosts)
			}
		})
	}
}

func TestResolveSecondsSnapsAndClamps(t *testing.T) {
	cases := []struct {
		model    string
		in       int
		hasVideo bool
		want     int
	}{
		{"veo31", 7, false, 6},      // nearest of 4/6/8
		{"veo31", 8, false, 8},      //
		{"veo31-fast", 3, false, 4}, // below range -> 4
		{"veo31", 99, false, 8},     // above range -> 8
		{"kling3", 15, false, 15},
		{"kling3", 99, false, 15},  // clamp to 15
		{"kling3", 1, false, 3},    // clamp to 3
		{"kling-o3", 20, true, 10}, // v2v caps at 10
		{"kling-o3", 3, true, 3},
		{"kling-o3", 15, true, 10}, // v2v: 15 is fine for t2v but not v2v
	}
	for _, tc := range cases {
		if got := resolveSeconds(tc.model, "", tc.in, tc.hasVideo); got != tc.want {
			t.Fatalf("%s duration=%d hasVideo=%v -> %d, want %d",
				tc.model, tc.in, tc.hasVideo, got, tc.want)
		}
	}
	// the string form wins when present
	if got := resolveSeconds("kling3", "10", 5, false); got != 10 {
		t.Fatalf("seconds string ignored: got %d", got)
	}
}

// A caller must not be able to get v2v output at t2v prices by putting the
// clip somewhere billing does not look.
func TestHasSourceVideoCoversEverySpelling(t *testing.T) {
	positives := []string{
		`{"video_url":"https://x/a.mp4"}`,
		`{"videoUrl":"https://x/a.mp4"}`,
		`{"source_video":"https://x/a.mp4"}`,
		`{"sourceVideo":"https://x/a.mp4"}`,
		`{"input_video":"https://x/a.mp4"}`,
		`{"input_reference":"https://x/a.mp4"}`,
		`{"video_url":{"url":"https://x/a.mp4"}}`,
		`{"metadata":{"video_url":"https://x/a.mp4"}}`,
		`{"messages":[{"role":"user","content":[{"type":"video_url","video_url":{"url":"https://x/a.mp4"}}]}]}`,
	}
	for _, body := range positives {
		if !hasSourceVideo([]byte(body), nil) {
			t.Fatalf("missed source clip in: %s", body)
		}
	}

	negatives := []string{
		`{}`,
		`{"prompt":"a cat"}`,
		`{"video_url":""}`,
		`{"video_url":{"url":"  "}}`,
		`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`,
		`{"messages":[{"role":"user","content":"plain string"}]}`,
	}
	for _, body := range negatives {
		if hasSourceVideo([]byte(body), nil) {
			t.Fatalf("false positive on: %s", body)
		}
	}

	// metadata map form (parsed TaskSubmitReq path)
	if !hasSourceVideo([]byte(`{}`), map[string]any{"video_url": "https://x/a.mp4"}) {
		t.Fatal("metadata map form missed")
	}
	if hasSourceVideo([]byte(`{}`), map[string]any{"video_url": ""}) {
		t.Fatal("empty metadata value should not count")
	}
}

func TestWantsAudioDefaultsOn(t *testing.T) {
	if !wantsAudio(nil) {
		t.Fatal("nil metadata should default to audio on")
	}
	if !wantsAudio(map[string]any{}) {
		t.Fatal("empty metadata should default to audio on")
	}
	for _, v := range []any{false, "false", "0", "off", "no", float64(0)} {
		if wantsAudio(map[string]any{"generate_audio": v}) {
			t.Fatalf("value %v should disable audio", v)
		}
	}
	if !wantsAudio(map[string]any{"generateAudio": true}) {
		t.Fatal("camelCase key not honoured")
	}
}

func TestIsHDIgnoresOrientation(t *testing.T) {
	for _, s := range []string{"1920x1080", "1080x1920"} {
		if !isHD(s) {
			t.Fatalf("%s should be HD", s)
		}
	}
	for _, s := range []string{"1280x720", "720x1280", "", "garbage"} {
		if isHD(s) {
			t.Fatalf("%s should not be HD", s)
		}
	}
}
