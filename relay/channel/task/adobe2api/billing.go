package adobe2api

import (
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// Cost model
//
// Adobe bills Firefly video in credits, strictly linear in duration with no
// base fee (measured against bks.adobe.io/v2/credits/cost):
//
//	kling3 / kling-o3   720p mute  20/s | 720p audio 25/s
//	                    1080p mute 25/s | 1080p audio 35/s
//	kling-v2v           720p       30/s | 1080p      40/s   (audio is free)
//	veo31 / veo31-ref   50/s                (resolution + audio irrelevant)
//	veo31-fast          10/s                (resolution + audio irrelevant)
//
// The 1080p+audio case is NOT the product of the two individual bumps
// (1.25 * 1.25 = 1.5625, but the real ratio is 1.75), so the tier cannot be
// decomposed into independent multipliers — it is emitted as a single `tier`
// ratio over the model's cheapest per-second price.
//
// Configure ModelPrice per model as the CHEAPEST per-second price; this
// multiplies it back up:
//
//	kling3 / kling-o3        -> price of 720p mute
//	kling-v2v                -> price of 720p
//	veo31 / -ref / -fast     -> flat (tier always 1)
//
// NOTE: the switch order in tierRatio/resolveSeconds matters — "kling-v2v"
// must be matched before the broader "kling" prefix.
// ---------------------------------------------------------------------------

const (
	defaultWidth  = 1280
	defaultHeight = 720
)

// parseSize turns "1920x1080" into its two dimensions, falling back to 720p.
func parseSize(size string) (int, int) {
	parts := strings.SplitN(strings.ToLower(strings.TrimSpace(size)), "x", 2)
	if len(parts) != 2 {
		return defaultWidth, defaultHeight
	}
	w, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	h, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil || w <= 0 || h <= 0 {
		return defaultWidth, defaultHeight
	}
	return w, h
}

// isHD reports whether the requested geometry lands in Adobe's 1080p tier.
// Orientation does not matter, so the *shorter* edge decides.
func isHD(size string) bool {
	w, h := parseSize(size)
	if h < w {
		w, h = h, w
	}
	return w >= 1080
}

// tierRatio returns the per-second cost multiplier over the model's cheapest
// tier, given the requested geometry and audio flag.
func tierRatio(model, size string, audio bool) float64 {
	m := strings.ToLower(strings.TrimSpace(model))
	hd := isHD(size)

	switch {
	case strings.HasPrefix(m, "kling-v2v"):
		// 720p 30/s, 1080p 40/s — audio does not move the price here.
		if hd {
			return 40.0 / 30.0
		}
		return 1
	case strings.HasPrefix(m, "kling"):
		// base = 720p mute = 20/s
		switch {
		case hd && audio:
			return 35.0 / 20.0
		case hd || audio:
			return 25.0 / 20.0
		default:
			return 1
		}
	default:
		// veo family: flat per second, nothing to scale.
		return 1
	}
}

// wantsAudio reads the audio flag out of the request body / metadata.
// Adobe defaults to generating audio when the caller says nothing.
func wantsAudio(metadata map[string]any) bool {
	if metadata == nil {
		return true
	}
	for _, k := range []string{"generate_audio", "generateAudio"} {
		v, ok := metadata[k]
		if !ok {
			continue
		}
		switch t := v.(type) {
		case bool:
			return t
		case string:
			s := strings.ToLower(strings.TrimSpace(t))
			return s != "false" && s != "0" && s != "no" && s != "off"
		case float64:
			return t != 0
		}
	}
	return true
}

// resolveSeconds picks the billable duration, mirroring what adobe2api will
// actually clamp/snap the request to, so the pre-charge matches the real cost.
func resolveSeconds(model, secondsStr string, duration int) int {
	seconds, _ := strconv.Atoi(strings.TrimSpace(secondsStr))
	if seconds == 0 {
		seconds = duration
	}
	if seconds <= 0 {
		seconds = 5
	}
	m := strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.HasPrefix(m, "veo31"):
		// upstream only accepts 4 / 6 / 8 and snaps to the nearest
		return snap(seconds, []int{4, 6, 8})
	case strings.HasPrefix(m, "kling-v2v"):
		return clamp(seconds, 3, 10)
	case strings.HasPrefix(m, "kling"):
		return clamp(seconds, 3, 15)
	}
	return clamp(seconds, 1, 60)
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func snap(v int, allowed []int) int {
	best := allowed[0]
	bestDiff := abs(v - best)
	for _, a := range allowed[1:] {
		if d := abs(v - a); d < bestDiff {
			best, bestDiff = a, d
		}
	}
	return best
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
