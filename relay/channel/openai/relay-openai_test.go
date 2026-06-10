package openai

import "testing"

func TestImageResponseHasData(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"b64 item", `{"created":1,"data":[{"b64_json":"abc"}]}`, true},
		{"url item", `{"created":1,"data":[{"url":"https://example.com/a.png"}]}`, true},
		{"empty data", `{"created":1,"data":[]}`, false},
		{"null data", `{"created":1,"data":null}`, false},
		{"missing data", `{"created":1,"usage":{"total_tokens":10}}`, false},
		{"error in 200 body", `{"error":{"message":"no accounts available"}}`, false},
		{"data not array", `{"data":{"b64_json":"abc"}}`, false},
		{"empty body", ``, false},
		{"non-json body", `<!DOCTYPE html>`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := imageResponseHasData([]byte(tc.body)); got != tc.want {
				t.Errorf("imageResponseHasData(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}
