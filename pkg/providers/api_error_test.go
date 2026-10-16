package providers

import (
	"net/http"
	"strings"
	"testing"
)

func TestNewAPIErrorParsesErrorEnvelopes(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		code    string
		message string
	}{
		{"nested object", `{"error":{"code":"model_not_found","message":"internal model name"}}`, "model_not_found", "internal model name"},
		{"string error", `{"error":"model 'missing' not found"}`, "", "model 'missing' not found"},
		{"root fields", `{"code":"bad_request","detail":"invalid input"}`, "bad_request", "invalid input"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := &http.Response{StatusCode: http.StatusBadRequest, Header: http.Header{"X-Request-Id": []string{"req-123"}}}
			apiErr := NewAPIError("OpenAI", response, []byte(test.body)).(*APIError)
			if apiErr.Code != test.code || apiErr.Message != test.message || apiErr.RequestID != "req-123" {
				t.Fatalf("APIError = %#v", apiErr)
			}
		})
	}
}

func TestErrorResponseBodiesAreBoundedAndMarked(t *testing.T) {
	body := strings.Repeat("x", maxErrorResponseBytes+1)
	apiErr := NewAPIError("HTTP", &http.Response{StatusCode: http.StatusBadRequest, Header: make(http.Header)}, []byte(body)).(*APIError)
	if len(apiErr.Body) != maxErrorResponseBytes || !strings.HasSuffix(apiErr.Body, errorBodyTruncationMarker) {
		t.Fatalf("APIError body not bounded and marked: length=%d", len(apiErr.Body))
	}
}
