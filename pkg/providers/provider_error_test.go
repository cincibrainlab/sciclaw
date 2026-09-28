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

func TestClassifyProviderErrorHandlesEOF(t *testing.T) {
	if info := ClassifyProviderError(errors.New("wrapped: " + io.EOF.Error())); info.Kind != ProviderErrorKindUnknown {
		t.Fatalf("unwrapped EOF kind = %q, want %q", info.Kind, ProviderErrorKindUnknown)
	}
	if info := ClassifyProviderError(fmt.Errorf("body: %w", io.EOF)); info.Kind != ProviderErrorKindInvalidResponse {
		t.Fatalf("wrapped EOF kind = %q, want %q", info.Kind, ProviderErrorKindInvalidResponse)
	}
}
