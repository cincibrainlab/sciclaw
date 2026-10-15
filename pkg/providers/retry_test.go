package providers

import (
	"context"
	"net/http"
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
	decision := DecideRetry(ProviderErrorInfo{Kind: ProviderErrorKindRateLimited, RetryAfter: time.Second}, 1)
	if decision.Action != RetryActionRetry || decision.Delay != time.Second {
		t.Fatalf("decision=%#v", decision)
	}
	if decision := DecideRetry(ProviderErrorInfo{Kind: ProviderErrorKindRateLimited, RetryAfter: retryMaxDelay + time.Second}, 1); decision.Action != RetryActionFail {
		t.Fatalf("decision=%#v", decision)
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
