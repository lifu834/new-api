package helper

// 260908：同步 /v1/images/edits 是 multipart，原始 body 读不出 JSON ⇒ param("size") 取到 nil。
// 实测后果：az-gpt-image-2 的按尺寸分档表达式把 2560x1440 的编辑请求算成"缺省档"，
// 收 ¥0.06 而成本 ¥0.22。本测试锁住修复：body 非 JSON 时改用已解析的请求对象当计费体。
import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

func newMultipartCtx() *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	req := httptest.NewRequest("POST", "/v1/images/edits", strings.NewReader("--x--\r\n"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=x")
	c.Request = req
	return c
}

func TestBillingInputFallsBackToParsedRequestForMultipart(t *testing.T) {
	c := newMultipartCtx()
	info := &relaycommon.RelayInfo{
		UserGroup: "image",
		Request:   &dto.ImageRequest{Model: "az-gpt-image-2", Prompt: "x", Size: "2560x1440"},
	}

	input, err := ResolveIncomingBillingExprRequestInput(c, info)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	got := gjson.GetBytes(input.Body, "size")
	if !got.Exists() || got.String() != "2560x1440" {
		t.Fatalf("param(\"size\") 应能读到 2560x1440，实际 body=%q", string(input.Body))
	}
	if input.Headers["x-user-group"] != "image" {
		t.Errorf("分组头丢失: %v", input.Headers)
	}
	t.Logf("ok multipart 回退到已解析请求，size=%s", got.String())
}

func TestBillingInputKeepsJSONBodyUntouched(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	req := httptest.NewRequest("POST", "/v1/images/generations", strings.NewReader(`{"size":"1024x1024"}`))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req
	// JSON 路径必须仍读原始 body（不能被已解析对象顶替，否则 param 语义变了）
	info := &relaycommon.RelayInfo{
		UserGroup: "image",
		Request:   &dto.ImageRequest{Model: "az-gpt-image-2", Size: "9999x9999"},
	}
	input, err := ResolveIncomingBillingExprRequestInput(c, info)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if got := gjson.GetBytes(input.Body, "size").String(); got != "1024x1024" {
		t.Fatalf("JSON 请求应读原始 body，期望 1024x1024，实际 %q (body=%q)", got, string(input.Body))
	}
	t.Log("ok JSON 路径未被改变")
}
