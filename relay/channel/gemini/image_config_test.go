package gemini

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

func TestImageSizeFromModel(t *testing.T) {
	cases := map[string]string{
		"nano-banana-pro":            "1K",
		"nano-banana-pro-2k":         "2K",
		"nano-banana-pro-4k":         "4K",
		"nano-banana-2":              "1K",
		"nano-banana-2-2K":           "2K", // 大小写不敏感
		"nano-banana-2-4K":           "4K",
		"gemini-3-pro-image-preview": "1K", // 上游原名不带后缀 → 1K
		"":                           "1K",
	}
	for model, want := range cases {
		if got := imageSizeFromModel(model); got != want {
			t.Errorf("imageSizeFromModel(%q) = %q, want %q", model, got, want)
		}
	}
}

func TestAspectRatioFromSize(t *testing.T) {
	cases := map[string]string{
		"1024x1024": "1:1",
		"2048x2048": "1:1",
		"4096x4096": "1:1",
		"1536x1024": "3:2",
		"1024x1536": "2:3",
		"1792x1024": "16:9", // 1.75 vs 1.778，2% 内
		"1024x1792": "9:16",
		"3840x2160": "16:9",
		"2160x3840": "9:16",
		"2048x1152": "16:9",
		"16:9":      "16:9", // 比例式直接透传
		"9:16":      "9:16",
		// 落不进白名单 / 非法输入 → 省略，交给上游默认
		"5:1":      "",
		"1000x100": "",
		"":         "",
		"1024":     "",
		"axb":      "",
		"0x1024":   "",
		"-10x1024": "",
		"1024x0":   "",
	}
	for size, want := range cases {
		if got := aspectRatioFromSize(size); got != want {
			t.Errorf("aspectRatioFromSize(%q) = %q, want %q", size, got, want)
		}
	}
}

func TestBuildGeminiImageConfig(t *testing.T) {
	// 4K 横版：两个字段都要出现
	var cfg geminiImageConfig
	if err := json.Unmarshal(buildGeminiImageConfig("nano-banana-pro-4k", "3840x2160"), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.ImageSize != "4K" || cfg.AspectRatio != "16:9" {
		t.Errorf("got %+v, want {4K 16:9}", cfg)
	}

	// size 落不进白名单时，aspectRatio 必须整个消失（omitempty），
	// 而不是写成空串——空串会被上游当成非法值拒掉。
	raw := string(buildGeminiImageConfig("nano-banana-2", "1000x100"))
	if raw != `{"imageSize":"1K"}` {
		t.Errorf("got %s, want {\"imageSize\":\"1K\"}", raw)
	}
}

// 按星桥文档 (doc.z5api.com/image-api.html) 校验我们实际发出的 body：
// responseModalities 是文档里唯一标"必填"的字段，漏掉会让模型合法地只回文字。
func TestConvertImageRequestMatchesDocumentedShape(t *testing.T) {
	a := &Adaptor{}
	info := &relaycommon.RelayInfo{
		OriginModelName: "nano-banana-pro-4k",
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "gemini-3-pro-image-preview"},
	}

	got, err := a.ConvertImageRequest(nil, info, dto.ImageRequest{
		Prompt: "a calico cat",
		Size:   "3840x2160",
	})
	if err != nil {
		t.Fatalf("ConvertImageRequest: %v", err)
	}
	raw, err := common.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var body struct {
		Contents []struct {
			Role  string `json:"role"`
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"contents"`
		GenerationConfig struct {
			ResponseModalities []string `json:"responseModalities"`
			ImageConfig        struct {
				ImageSize   string `json:"imageSize"`
				AspectRatio string `json:"aspectRatio"`
			} `json:"imageConfig"`
		} `json:"generationConfig"`
	}
	if err := common.Unmarshal(raw, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	gc := body.GenerationConfig
	if len(gc.ResponseModalities) != 1 || gc.ResponseModalities[0] != "IMAGE" {
		t.Errorf("responseModalities = %v, want [IMAGE]", gc.ResponseModalities)
	}
	if gc.ImageConfig.ImageSize != "4K" {
		t.Errorf("imageSize = %q, want 4K", gc.ImageConfig.ImageSize)
	}
	if gc.ImageConfig.AspectRatio != "16:9" {
		t.Errorf("aspectRatio = %q, want 16:9", gc.ImageConfig.AspectRatio)
	}
	if len(body.Contents) != 1 || body.Contents[0].Role != "user" ||
		len(body.Contents[0].Parts) != 1 || body.Contents[0].Parts[0].Text != "a calico cat" {
		t.Errorf("contents = %+v, want single user/text part", body.Contents)
	}
}
