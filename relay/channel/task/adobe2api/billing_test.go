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
		baseline  float64 // credits/s of the model's cheapest tier
		wantCosts float64 // credits/s Adobe actually charges
	}{
		{"kling3 720p mute", "kling3", "1280x720", false, 20, 20},
		{"kling3 720p audio", "kling3", "1280x720", true, 20, 25},
		{"kling3 1080p mute", "kling3", "1920x1080", false, 20, 25},
		{"kling3 1080p audio", "kling3", "1920x1080", true, 20, 35},
		{"kling3 vertical 1080p audio", "kling3", "1080x1920", true, 20, 35},
		{"kling-o3 720p mute", "kling-o3", "1280x720", false, 20, 20},
		{"kling-o3 1080p audio", "kling-o3", "1920x1080", true, 20, 35},
		{"v2v 720p (audio irrelevant)", "kling-v2v", "1280x720", true, 30, 30},
		{"v2v 720p mute", "kling-v2v", "1280x720", false, 30, 30},
		{"v2v 1080p", "kling-v2v", "1920x1080", true, 30, 40},
		{"veo31 flat 720p", "veo31", "1280x720", true, 50, 50},
		{"veo31 flat 1080p mute", "veo31", "1920x1080", false, 50, 50},
		{"veo31-fast flat", "veo31-fast", "1920x1080", true, 10, 10},
		{"veo31-ref flat", "veo31-ref", "1920x1080", false, 50, 50},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tierRatio(tc.model, tc.size, tc.audio) * tc.baseline
			if got != tc.wantCosts {
				t.Fatalf("%s: billed %.4f credits/s, Adobe charges %.4f",
					tc.name, got, tc.wantCosts)
			}
		})
	}
}

func TestResolveSecondsSnapsAndClamps(t *testing.T) {
	cases := []struct {
		model string
		in    int
		want  int
	}{
		{"veo31", 7, 6},      // nearest of 4/6/8
		{"veo31", 8, 8},      //
		{"veo31-fast", 3, 4}, // below range -> 4
		{"veo31", 99, 8},     // above range -> 8
		{"kling3", 15, 15},
		{"kling3", 99, 15}, // clamp to 15
		{"kling3", 1, 3},   // clamp to 3
		{"kling-v2v", 20, 10},
		{"kling-v2v", 3, 3},
	}
	for _, tc := range cases {
		if got := resolveSeconds(tc.model, "", tc.in); got != tc.want {
			t.Fatalf("%s duration=%d -> %d, want %d", tc.model, tc.in, got, tc.want)
		}
	}
	// the string form wins when present
	if got := resolveSeconds("kling3", "10", 5); got != 10 {
		t.Fatalf("seconds string ignored: got %d", got)
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
