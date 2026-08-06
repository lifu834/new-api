package adobe2api

import (
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

// ---------------------------------------------------------------------------
// Cost model
//
// Adobe bills Firefly video in credits, strictly linear in duration with no
// base fee (measured against bks.adobe.io/v2/credits/cost):
//
//	kling3 / kling-o3   t2v & i2v:  720p mute  20/s | 720p audio 25/s
//	                                1080p mute 25/s | 1080p audio 35/s
//	kling-o3            v2v:        720p       30/s | 1080p      40/s
//	                                (audio does NOT affect v2v pricing)
//	veo31 / veo31-ref   50/s                (resolution + audio irrelevant)
//	veo31-fast          10/s                (resolution + audio irrelevant)
//
// v2v is a *mode* of Kling 3.0 Omni, not a separate model: sending a source
// clip to kling-o3 switches the upstream version to kling_o3_*_v2v_*.  It is
// priced differently, so the tier ratio has to know whether a clip was sent.
//
// The 1080p+audio case is NOT the product of the two individual bumps
// (1.25 * 1.25 = 1.5625, but the real ratio is 1.75), so the tier cannot be
// decomposed into independent multipliers — it is emitted as a single `tier`
// ratio over the model's cheapest per-second price.
//
// Configure ModelPrice per model as the CHEAPEST per-second price; this
// multiplies it back up:
//
//	kling3 / kling-o3        -> price of 720p mute t2v (20/s)
//	veo31 / -ref / -fast     -> flat (tier always 1)
//
// NOTE: the switch order in tierRatio/resolveSeconds matters — "kling-v2v"
// must be matched before the broader "kling" prefix.
// ---------------------------------------------------------------------------

const (
	defaultWidth  = 1280
	defaultHeight = 720
)

// sourceVideoKeys are every spelling adobe2api accepts for the v2v source clip.
// Billing must look in all of them or a caller could get v2v output at t2v
// prices.
var sourceVideoKeys = []string{
	"video_url", "videoUrl", "source_video", "sourceVideo",
	"input_video", "input_reference", "inputReference",
}

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
// tier, given the requested geometry, audio flag and whether a source clip
// was supplied (which turns a kling request into video-to-video).
func tierRatio(model, size string, audio, hasVideo bool) float64 {
	m := strings.ToLower(strings.TrimSpace(model))
	hd := isHD(size)

	switch {
	case strings.HasPrefix(m, "kling"):
		// base = 720p mute t2v = 20/s
		if hasVideo {
			// v2v: 720p 30/s, 1080p 40/s — audio is not billed here.
			if hd {
				return 40.0 / 20.0
			}
			return 30.0 / 20.0
		}
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

// hasSourceVideo reports whether the caller supplied a v2v source clip.
//
// It inspects the RAW body rather than the parsed TaskSubmitReq, because a
// top-level `video_url` is not a TaskSubmitReq field — it survives to
// adobe2api via the verbatim body passthrough but would be invisible to
// billing, letting a caller buy v2v at t2v prices.
func hasSourceVideo(body []byte, metadata map[string]any) bool {
	for _, k := range sourceVideoKeys {
		if v, ok := metadata[k]; ok && nonEmptyValue(v) {
			return true
		}
		if r := gjson.GetBytes(body, k); r.Exists() && nonEmptyString(r) {
			return true
		}
		if r := gjson.GetBytes(body, "metadata."+k); r.Exists() && nonEmptyString(r) {
			return true
		}
	}
	// OpenAI-style content part: {"type":"video_url","video_url":{"url":...}}
	for _, m := range gjson.GetBytes(body, "messages").Array() {
		for _, part := range m.Get("content").Array() {
			if part.Get("type").String() == "video_url" {
				return true
			}
		}
	}
	return false
}

func nonEmptyString(r gjson.Result) bool {
	if r.IsObject() {
		return strings.TrimSpace(r.Get("url").String()) != ""
	}
	return strings.TrimSpace(r.String()) != ""
}

func nonEmptyValue(v any) bool {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t) != ""
	case map[string]any:
		if u, ok := t["url"].(string); ok {
			return strings.TrimSpace(u) != ""
		}
		return len(t) > 0
	case nil:
		return false
	}
	return true
}

// resolveSeconds picks the billable duration, mirroring what adobe2api will
// actually clamp/snap the request to, so the pre-charge matches the real cost.
func resolveSeconds(model, secondsStr string, duration int, hasVideo bool) int {
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
	case strings.HasPrefix(m, "kling"):
		if hasVideo {
			// v2v source clips are bounded to 3..10s upstream
			return clamp(seconds, 3, 10)
		}
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
