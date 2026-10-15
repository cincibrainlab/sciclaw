package providers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const maxErrorResponseBytes = 64 << 10

const errorBodyTruncationMarker = "\n[response body truncated]"

// APIError preserves provider diagnostics for server-side logs without making
// callers parse formatted response bodies.
type APIError struct {
	Provider   string
	StatusCode int
	Code       string
	Message    string
	RequestID  string
	RetryAfter time.Duration
	Body       string
	cause      error
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s API request failed: status=%d code=%q message=%q body=%s", e.Provider, e.StatusCode, e.Code, e.Message, e.Body)
}

func (e *APIError) Unwrap() error {
	return e.cause
}

func NewAPIError(provider string, response *http.Response, body []byte) error {
	return newAPIError(provider, response, body, nil)
}

func newAPIError(provider string, response *http.Response, body []byte, cause error) *APIError {
	body = truncateErrorResponseBody(body)
	code, message := parseAPIErrorBody(body)
	var statusCode int
	var requestID string
	var retryAfter time.Duration
	if response != nil {
		statusCode = response.StatusCode
		requestID = response.Header.Get("x-request-id")
		if requestID == "" {
			requestID = response.Header.Get("request-id")
		}
		retryAfter = parseRetryAfter(response.Header, time.Now())
	}
	return &APIError{
		Provider:   provider,
		StatusCode: statusCode,
		Code:       code,
		Message:    message,
		RequestID:  requestID,
		RetryAfter: retryAfter,
		Body:       string(body),
		cause:      cause,
	}
}

func parseRetryAfter(header http.Header, now time.Time) time.Duration {
	if retryAfter, ok := parseRetryAfterDuration(strings.TrimSpace(header.Get("Retry-After-Ms")), time.Millisecond); ok {
		return retryAfter
	}
	value := strings.TrimSpace(header.Get("Retry-After"))
	if retryAfter, ok := parseRetryAfterDuration(value, time.Second); ok {
		return retryAfter
	}
	if retryAt, err := http.ParseTime(value); err == nil && retryAt.After(now) {
		return retryAt.Sub(now)
	}
	return 0
}

func parseRetryAfterDuration(value string, unit time.Duration) (time.Duration, bool) {
	amount, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		if value != "" && strings.Trim(value, "0123456789") == "" {
			return time.Duration(1<<63 - 1), true
		}
		return 0, false
	}
	if amount < 0 {
		return 0, true
	}
	if amount > int64(time.Duration(1<<63-1)/unit) {
		return time.Duration(1<<63 - 1), true
	}
	return time.Duration(amount) * unit, true
}

func readProviderResponse(response *http.Response) ([]byte, error) {
	if response.StatusCode == http.StatusOK {
		return io.ReadAll(response.Body)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxErrorResponseBytes+1))
	if err != nil {
		return nil, err
	}
	return body, nil
}

func truncateErrorResponseBody(body []byte) []byte {
	if len(body) <= maxErrorResponseBytes {
		return body
	}
	return append(append([]byte{}, body[:maxErrorResponseBytes-len(errorBodyTruncationMarker)]...), errorBodyTruncationMarker...)
}

func parseAPIErrorBody(body []byte) (string, string) {
	var payload struct {
		Error   json.RawMessage `json:"error"`
		Code    string          `json:"code"`
		Type    string          `json:"type"`
		Message string          `json:"message"`
		Detail  string          `json:"detail"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return "", ""
	}
	var nested struct {
		Code    string `json:"code"`
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	var errorMessage string
	if json.Unmarshal(payload.Error, &nested) != nil {
		_ = json.Unmarshal(payload.Error, &errorMessage)
	}
	code := firstNonEmpty(nested.Code, nested.Type, payload.Code, payload.Type)
	message := firstNonEmpty(nested.Message, errorMessage, payload.Message, payload.Detail)
	return strings.TrimSpace(code), strings.TrimSpace(message)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
