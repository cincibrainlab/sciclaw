package providers

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"syscall"
	"time"
)

type ProviderErrorKind string

const (
	ProviderErrorKindUnknown             ProviderErrorKind = "unknown"
	ProviderErrorKindAuthentication      ProviderErrorKind = "authentication"
	ProviderErrorKindPermission          ProviderErrorKind = "permission"
	ProviderErrorKindQuota               ProviderErrorKind = "quota"
	ProviderErrorKindRateLimited         ProviderErrorKind = "rate_limited"
	ProviderErrorKindContextLimit        ProviderErrorKind = "context_limit"
	ProviderErrorKindModelUnavailable    ProviderErrorKind = "model_unavailable"
	ProviderErrorKindInvalidRequest      ProviderErrorKind = "invalid_request"
	ProviderErrorKindPolicyRejected      ProviderErrorKind = "policy_rejected"
	ProviderErrorKindTimeout             ProviderErrorKind = "timeout"
	ProviderErrorKindNetwork             ProviderErrorKind = "network"
	ProviderErrorKindProviderUnavailable ProviderErrorKind = "provider_unavailable"
	ProviderErrorKindInvalidResponse     ProviderErrorKind = "invalid_response"
	ProviderErrorKindCanceled            ProviderErrorKind = "canceled"
)

type ProviderErrorInfo struct {
	Kind         ProviderErrorKind
	Provider     string
	StatusCode   int
	ProviderCode string
	RequestID    string
	RetryAfter   time.Duration
}

// ClassifyProviderError returns provider diagnostics and a stable error category.
func ClassifyProviderError(err error) ProviderErrorInfo {
	info := ProviderErrorInfo{Kind: ProviderErrorKindUnknown, Provider: "unknown"}
	if err == nil {
		return info
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		info.Provider = apiErr.Provider
		info.StatusCode = apiErr.StatusCode
		info.ProviderCode = apiErr.Code
		info.RequestID = apiErr.RequestID
		info.RetryAfter = apiErr.RetryAfter
	}
	if errors.Is(err, context.Canceled) {
		info.Kind = ProviderErrorKindCanceled
		return info
	}
	if errors.Is(err, context.DeadlineExceeded) {
		info.Kind = ProviderErrorKindTimeout
		return info
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		info.Kind = ProviderErrorKindInvalidResponse
		return info
	}
	var responseDecodeErr *responseDecodeError
	if errors.As(err, &responseDecodeErr) {
		info.Kind = ProviderErrorKindInvalidResponse
		return info
	}
	if isNetworkSyscall(err) {
		info.Kind = ProviderErrorKindNetwork
		return info
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) {
		if networkErr.Timeout() {
			info.Kind = ProviderErrorKindTimeout
		} else {
			info.Kind = ProviderErrorKindNetwork
		}
		return info
	}

	code := strings.ToLower(info.ProviderCode)
	if kind, ok := providerErrorKindForCode(code); ok {
		info.Kind = kind
		return info
	}

	message := strings.ToLower(err.Error())
	switch {
	case info.StatusCode == 401 || containsProviderErrorText(message, "invalid api key", "invalid bearer token", "unauthorized", "authentication"):
		info.Kind = ProviderErrorKindAuthentication
	case info.StatusCode == 403:
		info.Kind = ProviderErrorKindPermission
	case containsProviderErrorText(message, "insufficient quota", "billing", "quota exceeded", "token usage"):
		info.Kind = ProviderErrorKindQuota
	case info.StatusCode == 429 || containsProviderErrorText(message, "rate limit", "rate_limit"):
		info.Kind = ProviderErrorKindRateLimited
	case containsProviderErrorText(message, "context length", "context_length", "context window", "context_window", "maximum context", "too many tokens"):
		info.Kind = ProviderErrorKindContextLimit
	case isModelUnavailable("", message):
		info.Kind = ProviderErrorKindModelUnavailable
	case containsProviderErrorText(message, "content policy", "moderation", "content_filter"):
		info.Kind = ProviderErrorKindPolicyRejected
	case info.StatusCode == 408 || containsProviderErrorText(message, "timeout", "deadline exceeded"):
		info.Kind = ProviderErrorKindTimeout
	case info.StatusCode == 400:
		info.Kind = ProviderErrorKindInvalidRequest
	case info.StatusCode == 409 || info.StatusCode >= 500 || containsProviderErrorText(message, "temporarily unavailable"):
		info.Kind = ProviderErrorKindProviderUnavailable
	case containsProviderErrorText(message, "malformed", "unexpected end", "empty response"):
		info.Kind = ProviderErrorKindInvalidResponse
	}
	return info
}

func providerErrorKindForCode(code string) (ProviderErrorKind, bool) {
	switch {
	case containsProviderErrorText(code, "invalid_api_key", "invalid_bearer_token", "authentication"):
		return ProviderErrorKindAuthentication, true
	case containsProviderErrorText(code, "permission", "access_denied", "forbidden"):
		return ProviderErrorKindPermission, true
	case containsProviderErrorText(code, "insufficient_quota", "billing", "quota"):
		return ProviderErrorKindQuota, true
	case containsProviderErrorText(code, "rate_limit"):
		return ProviderErrorKindRateLimited, true
	case containsProviderErrorText(code, "context_length", "context_window", "max_context", "too_many_tokens"):
		return ProviderErrorKindContextLimit, true
	case isModelUnavailable(code, ""):
		return ProviderErrorKindModelUnavailable, true
	case containsProviderErrorText(code, "policy", "moderation", "content_filter"):
		return ProviderErrorKindPolicyRejected, true
	case containsProviderErrorText(code, "timeout", "deadline_exceeded"):
		return ProviderErrorKindTimeout, true
	case containsProviderErrorText(code, "invalid_request", "unsupported_parameter"):
		return ProviderErrorKindInvalidRequest, true
	case containsProviderErrorText(code, "invalid_response"):
		return ProviderErrorKindInvalidResponse, true
	case containsProviderErrorText(code, "server_error", "service_unavailable", "temporarily_unavailable"):
		return ProviderErrorKindProviderUnavailable, true
	}
	return ProviderErrorKindUnknown, false
}

func isNetworkSyscall(err error) bool {
	return errors.Is(err, syscall.ECONNABORTED) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EHOSTUNREACH) ||
		errors.Is(err, syscall.ENETUNREACH) ||
		errors.Is(err, syscall.EPIPE)
}

func isModelUnavailable(code, message string) bool {
	return containsProviderErrorText(code, "model_not_found", "model_not_available", "model_not_supported", "unknown_model") ||
		strings.Contains(message, "model") && containsProviderErrorText(message, "not found", "does not exist", "not available", "not supported")
}

func containsProviderErrorText(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if strings.Contains(value, candidate) {
			return true
		}
	}
	return false
}
