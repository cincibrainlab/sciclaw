package providers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

type retryTestProvider struct {
	errors []error
	calls  int
}

func (p *retryTestProvider) Chat(_ context.Context, _ []Message, _ []ToolDefinition, _ string, _ map[string]interface{}) (*LLMResponse, error) {
	p.calls++
	if len(p.errors) > 0 {
		err := p.errors[0]
		p.errors = p.errors[1:]
		return nil, err
	}
	return &LLMResponse{Content: "ok"}, nil
}

func (p *retryTestProvider) GetDefaultModel() string { return "test" }

func TestRetryChat(t *testing.T) {
	tests := []struct {
		name   string
		errors []error
		calls  int
	}{
		{"retries rate limit", []error{&APIError{StatusCode: http.StatusTooManyRequests, RetryAfter: time.Millisecond}}, 2},
		{"does not retry unavailable", []error{&APIError{StatusCode: http.StatusServiceUnavailable}}, 1},
		{"does not retry authentication", []error{&APIError{StatusCode: http.StatusUnauthorized}}, 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := &retryTestProvider{errors: test.errors}
			_, _, _, _ = RetryChat(context.Background(), provider, nil, nil, "test", nil, nil)
			if provider.calls != test.calls {
				t.Fatalf("calls=%d, want %d", provider.calls, test.calls)
			}
		})
	}
}

func TestDecideRetryHonorsBoundedRetryAfter(t *testing.T) {
	decision := DecideRetry(ProviderErrorInfo{StatusCode: http.StatusTooManyRequests, Kind: ProviderErrorKindRateLimited, RetryAfter: time.Second}, 1)
	if decision.Action != RetryActionRetry || decision.Delay != time.Second {
		t.Fatalf("decision=%#v", decision)
	}
	if decision := DecideRetry(ProviderErrorInfo{Kind: ProviderErrorKindRateLimited, RetryAfter: retryMaxDelay + time.Second}, 1); decision.Action != RetryActionFail {
		t.Fatalf("decision=%#v", decision)
	}
}

func TestDecideRetryOnlyRetriesBoundedRateLimits(t *testing.T) {
	tests := []struct {
		name    string
		info    ProviderErrorInfo
		attempt int
	}{
		{"rate limit without retry-after", ProviderErrorInfo{Kind: ProviderErrorKindRateLimited}, 1},
		{"rate limit over bound", ProviderErrorInfo{Kind: ProviderErrorKindRateLimited, RetryAfter: retryMaxDelay + time.Millisecond}, 1},
		{"second attempt", ProviderErrorInfo{StatusCode: http.StatusTooManyRequests, Kind: ProviderErrorKindRateLimited, RetryAfter: time.Millisecond}, 2},
		{"server rate limit code", ProviderErrorInfo{StatusCode: http.StatusServiceUnavailable, Kind: ProviderErrorKindRateLimited, RetryAfter: time.Millisecond}, 1},
		{"timeout", ProviderErrorInfo{Kind: ProviderErrorKindTimeout}, 1},
		{"network", ProviderErrorInfo{Kind: ProviderErrorKindNetwork}, 1},
		{"provider unavailable", ProviderErrorInfo{Kind: ProviderErrorKindProviderUnavailable}, 1},
		{"invalid response", ProviderErrorInfo{Kind: ProviderErrorKindInvalidResponse}, 1},
		{"unknown", ProviderErrorInfo{Kind: ProviderErrorKindUnknown}, 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if decision := DecideRetry(test.info, test.attempt); decision.Action != RetryActionFail {
				t.Fatalf("decision=%#v", decision)
			}
		})
	}
}

func TestRetryChatStopsOnCanceledBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider := &retryTestProvider{errors: []error{&APIError{StatusCode: http.StatusTooManyRequests, RetryAfter: time.Second}}}
	_, _, _, err := RetryChat(ctx, provider, nil, nil, "test", nil, func(RetryAttempt) { cancel() })
	if err != context.Canceled {
		t.Fatalf("error=%v, want context.Canceled", err)
	}
	if provider.calls != 1 {
		t.Fatalf("calls=%d, want 1", provider.calls)
	}
}

func TestRetryChatDoesNotCallCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	provider := &retryTestProvider{}
	_, attempts, _, err := RetryChat(ctx, provider, nil, nil, "test", nil, nil)
	if err != context.Canceled || attempts != 0 || provider.calls != 0 {
		t.Fatalf("err=%v attempts=%d calls=%d", err, attempts, provider.calls)
	}
}

func TestRetryChatDoesNotRetry5xxRegardlessOfErrorBody(t *testing.T) {
	for _, body := range []string{
		`{"error":{"code":"server_error","message":"upstream failed after checking rate limits"}}`,
		`{"error":{"code":"rate_limit_error","message":"slow down"}}`,
	} {
		t.Run(body, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.Header().Set("Retry-After-Ms", "1")
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()

			_, attempts, _, err := RetryChat(t.Context(), NewHTTPProvider("", server.URL, ""), nil, nil, "test", nil, nil)
			if requests.Load() != 1 || attempts != 1 {
				t.Fatalf("requests=%d attempts=%d, want 1", requests.Load(), attempts)
			}
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusServiceUnavailable {
				t.Fatalf("error=%v, want original 503 APIError", err)
			}
		})
	}
}

func TestRetryChatSDKRequestBudget(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		retryAfter string
		cancel     bool
		wantCalls  int32
	}{
		{"eligible rate limit", http.StatusTooManyRequests, "1", false, 2},
		{"rate limit without delay", http.StatusTooManyRequests, "", false, 1},
		{"server error", http.StatusServiceUnavailable, "1", false, 1},
		{"cancellation", http.StatusTooManyRequests, "1", true, 1},
	}
	providers := []struct {
		name  string
		new   func(string) LLMProvider
		model string
		body  string
	}{
		{
			name: "claude",
			new: func(baseURL string) LLMProvider {
				p := NewClaudeProvider("test-token")
				p.client = createAnthropicTestClient(baseURL, "test-token")
				return p
			},
			model: "test",
			body:  `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`,
		},
		{
			name: "codex",
			new: func(baseURL string) LLMProvider {
				p := NewCodexProvider("test-token", "")
				p.client = createOpenAITestClient(baseURL, "test-token", "")
				return p
			},
			model: "gpt-5.6-sol",
			body:  `{"error":{"code":"rate_limit_exceeded","message":"slow down"}}`,
		},
	}

	for _, providerTest := range providers {
		for _, test := range tests {
			t.Run(providerTest.name+"/"+test.name, func(t *testing.T) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					requests.Add(1)
					if test.retryAfter != "" {
						w.Header().Set("Retry-After-Ms", test.retryAfter)
					}
					w.WriteHeader(test.status)
					_, _ = w.Write([]byte(providerTest.body))
				}))
				defer server.Close()

				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				_, _, _, err := RetryChat(ctx, providerTest.new(server.URL), []Message{{Role: "user", Content: "hello"}}, nil, providerTest.model, map[string]interface{}{}, func(RetryAttempt) {
					if test.cancel {
						cancel()
					}
				})
				if test.cancel && !errors.Is(err, context.Canceled) {
					t.Fatalf("error=%v, want context.Canceled", err)
				}
				if requests.Load() != test.wantCalls {
					t.Fatalf("requests=%d, want %d", requests.Load(), test.wantCalls)
				}
			})
		}
	}
}
