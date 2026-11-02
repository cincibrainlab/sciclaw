package providers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"syscall"
	"testing"
	"time"
)

type testNetworkError struct{ timeout bool }

func (err testNetworkError) Error() string   { return "network failure" }
func (err testNetworkError) Timeout() bool   { return err.timeout }
func (err testNetworkError) Temporary() bool { return false }

func TestClassifyProviderError(t *testing.T) {
	apiErr := NewAPIError("Test", &http.Response{
		StatusCode: http.StatusServiceUnavailable,
		Header:     http.Header{"Retry-After": []string{"2"}, "X-Request-Id": []string{"req-123"}},
	}, []byte(`{"error":{"code":"model_not_found","message":"model not found"}}`))
	tests := []struct {
		name string
		err  error
		want ProviderErrorKind
	}{
		{"semantic code precedes 5xx", apiErr, ProviderErrorKindModelUnavailable},
		{"context canceled", fmt.Errorf("request: %w", context.Canceled), ProviderErrorKindCanceled},
		{"context deadline", fmt.Errorf("request: %w", context.DeadlineExceeded), ProviderErrorKindTimeout},
		{"network timeout", testNetworkError{timeout: true}, ProviderErrorKindTimeout},
		{"network error", testNetworkError{}, ProviderErrorKindNetwork},
		{"unexpected eof", fmt.Errorf("body: %w", io.ErrUnexpectedEOF), ProviderErrorKindInvalidResponse},
		{"network syscall", fmt.Errorf("write: %w", syscall.ECONNRESET), ProviderErrorKindNetwork},
		{"underscore context code", &APIError{Code: "context_window_exceeded"}, ProviderErrorKindContextLimit},
		{"generic invalid request with context message", &APIError{StatusCode: 400, Code: "invalid_request_error", Message: "maximum context length exceeded"}, ProviderErrorKindContextLimit},
		{"generic invalid request code with context message", &APIError{StatusCode: 400, Code: "invalid_request", Message: "maximum context length exceeded"}, ProviderErrorKindContextLimit},
		{"specific invalid request code precedes conflicting message", &APIError{StatusCode: 400, Code: "invalid_request_parameter", Message: "maximum context length exceeded"}, ProviderErrorKindInvalidRequest},
		{"generic invalid request with model message", &APIError{StatusCode: 400, Code: "invalid_request_error", Message: "model 'missing' not found"}, ProviderErrorKindModelUnavailable},
		{"generic invalid request fallback", &APIError{Code: "invalid_request_error", Message: "invalid parameter"}, ProviderErrorKindInvalidRequest},
		{"generic request ignores raw body", NewAPIError("Test", &http.Response{StatusCode: 400}, []byte(`{"error":{"type":"invalid_request_error","message":"unsupported parameter","param":"context_length"}}`)), ProviderErrorKindInvalidRequest},
		{"wrapped generic request ignores raw body", fmt.Errorf("context_length: %w", &APIError{StatusCode: 400, Code: "invalid_request_error", Body: `{"param":"context_length"}`}), ProviderErrorKindInvalidRequest},
		{"plain text body model unavailable", NewAPIError("Test", &http.Response{StatusCode: 404}, []byte("model missing not found")), ProviderErrorKindModelUnavailable},
		{"plain text body context limit", NewAPIError("Test", &http.Response{StatusCode: 400}, []byte(" maximum context length exceeded ")), ProviderErrorKindContextLimit},
		{"parsed message takes precedence over body", &APIError{StatusCode: 400, Message: "unsupported parameter", Body: "maximum context length exceeded"}, ProviderErrorKindInvalidRequest},
		{"html body ignored", NewAPIError("Test", &http.Response{StatusCode: 400}, []byte("<html>maximum context length exceeded</html>")), ProviderErrorKindInvalidRequest},
		{"json string body ignored", NewAPIError("Test", &http.Response{StatusCode: 400}, []byte(`"maximum context length exceeded"`)), ProviderErrorKindInvalidRequest},
		{"structured body without message ignored", NewAPIError("Test", &http.Response{StatusCode: 400}, []byte(`{"error":{"type":"invalid_request_error","param":"context_length"}}`)), ProviderErrorKindInvalidRequest},
		{"malformed structured body ignored", NewAPIError("Test", &http.Response{StatusCode: 400}, []byte(`{"param":"context_length"`)), ProviderErrorKindInvalidRequest},
		{"generic request model unavailable for authentication method", &APIError{StatusCode: 400, Code: "invalid_request_error", Message: "model missing not found for this authentication method"}, ProviderErrorKindModelUnavailable},
		{"context limit for authentication method", &APIError{StatusCode: 400, Code: "invalid_request_error", Message: "maximum context length exceeded for this authentication method"}, ProviderErrorKindContextLimit},
		{"authentication failure message", &APIError{StatusCode: 400, Code: "invalid_request_error", Message: "authentication failed"}, ProviderErrorKindAuthentication},
		{"authentication status precedes model message", &APIError{StatusCode: 401, Code: "invalid_request_error", Message: "model missing not found for this authentication method"}, ProviderErrorKindAuthentication},
		{"specific code precedes conflicting message", &APIError{StatusCode: 400, Code: "unsupported_parameter", Message: "maximum context length exceeded"}, ProviderErrorKindInvalidRequest},
		{"specific model code precedes conflicting message", &APIError{StatusCode: 400, Code: "model_not_found", Message: "maximum context length exceeded"}, ProviderErrorKindModelUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ClassifyProviderError(test.err); got.Kind != test.want {
				t.Fatalf("ClassifyProviderError().Kind = %q, want %q", got.Kind, test.want)
			}
		})
	}

	info := ClassifyProviderError(apiErr)
	if info.Provider != "Test" || info.StatusCode != http.StatusServiceUnavailable || info.ProviderCode != "model_not_found" || info.RequestID != "req-123" || info.RetryAfter != 2*time.Second {
		t.Fatalf("ClassifyProviderError() diagnostics = %#v", info)
	}
}

func TestClassifyProviderErrorNarrowModelMessageFallback(t *testing.T) {
	for _, code := range []string{"", "invalid_parameter"} {
		t.Run("parameter code="+code, func(t *testing.T) {
			err := &APIError{StatusCode: http.StatusBadRequest, Code: code, Message: "The temperature parameter is not supported for this model"}
			for _, wrapped := range []error{err, fmt.Errorf("wrapped: %w", err)} {
				if got := ClassifyProviderError(wrapped).Kind; got != ProviderErrorKindInvalidRequest {
					t.Fatalf("ClassifyProviderError().Kind = %q, want %q", got, ProviderErrorKindInvalidRequest)
				}
			}
		})
	}

	if got := ClassifyProviderError(&APIError{StatusCode: http.StatusBadRequest, Message: "model 'missing' not found"}).Kind; got != ProviderErrorKindModelUnavailable {
		t.Fatalf("ClassifyProviderError().Kind = %q, want %q", got, ProviderErrorKindModelUnavailable)
	}
}

func TestClassifyProviderErrorHandlesEOF(t *testing.T) {
	if info := ClassifyProviderError(errors.New("wrapped: " + io.EOF.Error())); info.Kind != ProviderErrorKindUnknown {
		t.Fatalf("unwrapped EOF kind = %q, want %q", info.Kind, ProviderErrorKindUnknown)
	}
	if info := ClassifyProviderError(fmt.Errorf("body: %w", io.EOF)); info.Kind != ProviderErrorKindInvalidResponse {
		t.Fatalf("wrapped EOF kind = %q, want %q", info.Kind, ProviderErrorKindInvalidResponse)
	}
}
