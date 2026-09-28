package agent

import (
	"errors"
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/providers"
)

type providerFailureError struct {
	cause    error
	outbound bus.OutboundError
	message  string
	info     providers.ProviderErrorInfo
}

func (e *providerFailureError) Error() string { return e.cause.Error() }
func (e *providerFailureError) Unwrap() error { return e.cause }

func newProviderFailureError(cause error, referenceID string, info providers.ProviderErrorInfo) error {
	message, details := providerFailurePresentation(info)
	return &providerFailureError{
		cause:   cause,
		message: message + "\n\nReference ID: `" + referenceID + "`",
		outbound: bus.OutboundError{
			TechnicalDetails: details,
			ReferenceID:      referenceID,
		},
		info: info,
	}
}

// UserError returns the safe presentation attached to an error, if any.
func UserError(err error) (string, *bus.OutboundError, bool) {
	var providerErr *providerFailureError
	if errors.As(err, &providerErr) {
		return providerErr.message, &providerErr.outbound, true
	}
	var visible userVisibleError
	if errors.As(err, &visible) && strings.TrimSpace(visible.UserMessage()) != "" {
		return visible.UserMessage(), nil, true
	}
	return "", nil, false
}

func providerFailurePresentation(info providers.ProviderErrorInfo) (string, string) {
	message, detail := "The AI service could not complete this request. Please try again, then contact an administrator if it continues.", "The provider did not return a recognized error category."
	switch info.Kind {
	case providers.ProviderErrorKindAuthentication, providers.ProviderErrorKindPermission:
		message, detail = "The AI service needs administrator attention before it can respond.", "Authentication or account access was rejected."
	case providers.ProviderErrorKindQuota:
		message, detail = "The AI service has reached its usage limit. Please contact an administrator.", "The provider rejected the request because of an account usage limit."
	case providers.ProviderErrorKindRateLimited:
		message, detail = "The AI service is busy. Please wait a moment and try again.", "The provider rate-limited this request."
	case providers.ProviderErrorKindContextLimit:
		message, detail = "This conversation is too long for the selected AI model. Start a new conversation or shorten your request.", "The provider rejected the request because it exceeds the model context limit."
	case providers.ProviderErrorKindModelUnavailable:
		message, detail = "The selected AI model is not available. Choose another model or contact an administrator.", "The provider rejected the selected model."
	case providers.ProviderErrorKindTimeout, providers.ProviderErrorKindNetwork, providers.ProviderErrorKindProviderUnavailable, providers.ProviderErrorKindInvalidResponse:
		message, detail = "The AI service is temporarily unavailable. Please try again shortly.", "The provider or network did not complete the request."
	}
	technical := []string{detail}
	if info.StatusCode != 0 {
		technical = append(technical, fmt.Sprintf("Status: %d", info.StatusCode))
	}
	return message, strings.Join(technical, "\n")
}

func providerFailureInfo(err error) (providers.ProviderErrorInfo, bool) {
	var providerErr *providerFailureError
	if errors.As(err, &providerErr) {
		return providerErr.info, true
	}
	return providers.ProviderErrorInfo{}, false
}
