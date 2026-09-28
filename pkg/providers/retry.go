package providers

import (
	"context"
	"math/rand/v2"
	"time"
)

type RetryAction string

const (
	RetryActionFail       RetryAction = "fail"
	RetryActionRetry      RetryAction = "retry"
	RetryActionRetryAfter RetryAction = "retry_after"

	maxProviderAttempts = 2
	retryBaseDelay      = 250 * time.Millisecond
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
	Final    bool
	Err      error
}

func DecideRetry(info ProviderErrorInfo, attempt int) RetryDecision {
	if attempt >= maxProviderAttempts {
		return RetryDecision{Action: RetryActionFail}
	}
	switch info.Kind {
	case ProviderErrorKindRateLimited:
		if info.RetryAfter > retryMaxDelay {
			return RetryDecision{Action: RetryActionFail}
		}
		if info.RetryAfter > 0 {
			return RetryDecision{Action: RetryActionRetryAfter, Delay: info.RetryAfter}
		}
	case ProviderErrorKindTimeout, ProviderErrorKindNetwork, ProviderErrorKindProviderUnavailable, ProviderErrorKindInvalidResponse:
	default:
		return RetryDecision{Action: RetryActionFail}
	}
	return RetryDecision{Action: RetryActionRetry, Delay: retryBaseDelay + time.Duration(rand.Int64N(int64(retryBaseDelay)))}
}

func RetryChat(ctx context.Context, provider LLMProvider, messages []Message, tools []ToolDefinition, model string, options map[string]interface{}, observe func(RetryAttempt)) (*LLMResponse, int, *ProviderErrorInfo, error) {
	var recoveredFrom *ProviderErrorInfo
	for attempt := 1; attempt <= maxProviderAttempts; attempt++ {
		response, err := provider.Chat(ctx, messages, tools, model, options)
		if err == nil {
			return response, attempt, recoveredFrom, nil
		}
		info := ClassifyProviderError(err)
		decision := DecideRetry(info, attempt)
		final := decision.Action == RetryActionFail
		if observe != nil {
			observe(RetryAttempt{Number: attempt, Info: info, Decision: decision, Final: final, Err: err})
		}
		if final {
			return nil, attempt, recoveredFrom, err
		}
		copy := info
		recoveredFrom = &copy
		if err := waitForRetry(ctx, decision.Delay); err != nil {
			return nil, attempt, recoveredFrom, err
		}
	}
	return nil, maxProviderAttempts, recoveredFrom, context.DeadlineExceeded
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
