package openai

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"

	"github.com/gin-gonic/gin"
)

func newImageEditContext(t *testing.T, contentType, body string) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/images/edits", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", contentType)
	return c
}

func convertImageEdit(t *testing.T, contentType, body, mappedModel string) any {
	t.Helper()
	c := newImageEditContext(t, contentType, body)
	adaptor := &Adaptor{}
	info := &relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeImagesEdits}
	// ModelMappedHelper 已经把映射后的模型名写进 request.Model
	request := dto.ImageRequest{Model: mappedModel, Prompt: "merge the two scenes"}
	converted, err := adaptor.ConvertImageRequest(c, info, request)
	if err != nil {
		t.Fatalf("ConvertImageRequest returned error: %v", err)
	}
	return converted
}

// Codex 内置 imagegen 的形态：application/json + `images` 数组(data URL)。
// 这些字段不在 dto.ImageRequest 里，必须原样透传，否则上游会报
// "image file or image_url is required"。
func TestConvertImageRequest_JSONPreservesUnknownImageFields(t *testing.T) {
	body := `{"model":"gpt-image-2-1k","prompt":"merge the two scenes","n":1,"size":"1024x1536","quality":"high","background":"opaque","images":["data:image/png;base64,iVBORw0KGgo=","data:image/jpeg;base64,/9j/4AAQ"],"input_fidelity":"high"}`

	converted := convertImageEdit(t, "application/json", body, "gpt-image-2-pool")

	raw, ok := converted.(json.RawMessage)
	if !ok {
		t.Fatalf("expected json.RawMessage passthrough, got %T", converted)
	}

	var got map[string]any
	if err := common.Unmarshal(raw, &got); err != nil {
		t.Fatalf("converted body is not valid JSON: %v", err)
	}

	images, ok := got["images"].([]any)
	if !ok || len(images) != 2 {
		t.Fatalf("images field lost or malformed: %#v", got["images"])
	}
	if images[0] != "data:image/png;base64,iVBORw0KGgo=" {
		t.Errorf("first reference image mangled: %v", images[0])
	}
	if got["input_fidelity"] != "high" {
		t.Errorf("input_fidelity lost: %#v", got["input_fidelity"])
	}
	if got["quality"] != "high" || got["background"] != "opaque" || got["size"] != "1024x1536" {
		t.Errorf("known passthrough fields altered: %#v", got)
	}
	// 渠道级 model_mapping 必须仍然生效
	if got["model"] != "gpt-image-2-pool" {
		t.Errorf("model not rewritten to mapped name, got %#v", got["model"])
	}
}

// 官方 JSON 形态的另外两种图片字段：image_url / mask 同样不能丢。
func TestConvertImageRequest_JSONPreservesImageURLAndMask(t *testing.T) {
	body := `{"model":"gpt-image-2","prompt":"replace the sky","image_url":"https://example.com/a.png","mask":"data:image/png;base64,iVBORw0KGgo="}`

	converted := convertImageEdit(t, "application/json; charset=utf-8", body, "gpt-image-2")

	raw, ok := converted.(json.RawMessage)
	if !ok {
		t.Fatalf("expected json.RawMessage passthrough, got %T", converted)
	}

	var got map[string]any
	if err := common.Unmarshal(raw, &got); err != nil {
		t.Fatalf("converted body is not valid JSON: %v", err)
	}
	if got["image_url"] != "https://example.com/a.png" {
		t.Errorf("image_url lost: %#v", got["image_url"])
	}
	if got["mask"] != "data:image/png;base64,iVBORw0KGgo=" {
		t.Errorf("mask lost: %#v", got["mask"])
	}
}

// 单数 image 字段(dto 已知字段)走同一条透传路径，行为不变。
func TestConvertImageRequest_JSONPreservesSingularImage(t *testing.T) {
	body := `{"model":"gpt-image-2","prompt":"x","image":"data:image/png;base64,iVBORw0KGgo="}`

	converted := convertImageEdit(t, "application/json", body, "gpt-image-2")

	raw, ok := converted.(json.RawMessage)
	if !ok {
		t.Fatalf("expected json.RawMessage passthrough, got %T", converted)
	}
	var got map[string]any
	if err := common.Unmarshal(raw, &got); err != nil {
		t.Fatalf("converted body is not valid JSON: %v", err)
	}
	if got["image"] != "data:image/png;base64,iVBORw0KGgo=" {
		t.Errorf("image lost: %#v", got["image"])
	}
}

// 非 JSON、非 multipart 的请求保持旧行为(返回结构体)，不受本次改动影响。
func TestConvertImageRequest_NonJSONFallsBackToStruct(t *testing.T) {
	converted := convertImageEdit(t, "application/x-www-form-urlencoded", "prompt=x", "gpt-image-2")

	if _, ok := converted.(dto.ImageRequest); !ok {
		t.Fatalf("expected dto.ImageRequest fallback, got %T", converted)
	}
}

// 空体 / 非 JSON 对象的 JSON 请求不应 panic，退回旧行为。
func TestConvertImageRequest_JSONNonObjectFallsBackToStruct(t *testing.T) {
	converted := convertImageEdit(t, "application/json", `["not","an","object"]`, "gpt-image-2")

	if _, ok := converted.(dto.ImageRequest); !ok {
		t.Fatalf("expected dto.ImageRequest fallback, got %T", converted)
	}
}
