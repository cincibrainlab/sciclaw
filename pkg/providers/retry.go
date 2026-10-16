package providers

import (
	"context"
	"time"
)

type RetryAction string

const (
	RetryActionFail  RetryAction = "fail"
	RetryActionRetry RetryAction = "retry"

	maxProviderAttempts = 2
	retryMaxDelay       = 10 * time.Second
)

type RetryDecision struct {
	Action RetryAction
	Delay  time.Duration
}

type RetryAttempt struct {
	Number   int
	Info     ProviderErrorInfo
	Decision RetryDecision
	Err      error
}

func DecideRetry(info ProviderErrorInfo, attempt int) RetryDecision {
	if attempt >= maxProviderAttempts || info.Kind != ProviderErrorKindRateLimited || info.RetryAfter <= 0 || info.RetryAfter > retryMaxDelay {
		return RetryDecision{Action: RetryActionFail}
	}
	return RetryDecision{Action: RetryActionRetry, Delay: info.RetryAfter}
}

func RetryChat(ctx context.Context, provider LLMProvider, messages []Message, tools []ToolDefinition, model string, options map[string]interface{}, observe func(RetryAttempt)) (*LLMResponse, int, *ProviderErrorInfo, error) {
	var recoveredFrom *ProviderErrorInfo
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, attempt - 1, recoveredFrom, err
		}
		response, err := provider.Chat(ctx, messages, tools, model, options)
		if err == nil {
			return response, attempt, recoveredFrom, nil
		}
		info := ClassifyProviderError(err)
		decision := DecideRetry(info, attempt)
		if decision.Action == RetryActionFail {
			return nil, attempt, recoveredFrom, err
		}
		if observe != nil {
			observe(RetryAttempt{Number: attempt, Info: info, Decision: decision, Err: err})
		}
		copy := info
		recoveredFrom = &copy
		if err := waitForRetry(ctx, decision.Delay); err != nil {
			return nil, attempt, recoveredFrom, err
		}
	}
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
