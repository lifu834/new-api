package types

import (
	"net/http"
	"testing"
)

// Mirrors RelayErrorHandler's path for unparseable upstream error bodies
// (e.g. Cloudflare 524 HTML pages): InitOpenAIError leaves the relayed
// message empty, then only e.Err carries the diagnostic. Clients should see
// that wrapped error instead of a bare "openai_error" label.
func TestToOpenAIErrorEmptyMessageFallback(t *testing.T) {
	e := InitOpenAIError(ErrorCodeBadResponseStatusCode, http.StatusServiceUnavailable)
	e.SetMessage("bad response status code 524")

	result := e.ToOpenAIError()

	if result.Message != "bad response status code 524" {
		t.Errorf("Message = %q, want wrapped error message", result.Message)
	}
	if result.Message == string(ErrorTypeOpenAIError) {
		t.Errorf("Message must not degrade to the bare error-type label")
	}
}

// When the upstream error parsed successfully, its message must pass through unchanged.
func TestToOpenAIErrorKeepsUpstreamMessage(t *testing.T) {
	upstream := OpenAIError{Message: "quota exceeded", Type: "insufficient_quota", Code: "insufficient_quota"}
	e := WithOpenAIError(upstream, http.StatusTooManyRequests)

	result := e.ToOpenAIError()

	if result.Message != "quota exceeded" {
		t.Errorf("Message = %q, want upstream message", result.Message)
	}
	if result.Type != "insufficient_quota" {
		t.Errorf("Type = %q, want upstream type", result.Type)
	}
}

func TestToClaudeErrorEmptyMessageFallback(t *testing.T) {
	e := InitOpenAIError(ErrorCodeBadResponseStatusCode, http.StatusServiceUnavailable)
	e.SetMessage("bad response status code 502")

	result := e.ToClaudeError()

	if result.Message != "bad response status code 502" {
		t.Errorf("Message = %q, want wrapped error message", result.Message)
	}
}
