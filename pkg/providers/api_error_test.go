package providers

import (
	"net/http"
	"strings"
	"testing"
	"time"
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

func TestAPIErrorParsesRetryAfter(t *testing.T) {
	now := time.Date(2026, time.September, 28, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		header http.Header
		want   time.Duration
	}{
		{"milliseconds", http.Header{"Retry-After-Ms": []string{"250"}}, 250 * time.Millisecond},
		{"milliseconds takes precedence", http.Header{"Retry-After-Ms": []string{"250"}, "Retry-After": []string{"3"}}, 250 * time.Millisecond},
		{"seconds", http.Header{"Retry-After": []string{"3"}}, 3 * time.Second},
		{"http date", http.Header{"Retry-After": []string{now.Add(10 * time.Second).Format(http.TimeFormat)}}, 10 * time.Second},
		{"invalid", http.Header{"Retry-After": []string{"soon"}}, 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := parseRetryAfter(test.header, now); got != test.want {
				t.Fatalf("parseRetryAfter() = %v, want %v", got, test.want)
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
