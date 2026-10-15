package agent

import (
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestProviderFailureErrorClassification(t *testing.T) {
	tests := []struct {
		name    string
		apiErr  *providers.APIError
		want    string
		details []string
	}{
		{
			name:    "does not expose raw body",
			apiErr:  &providers.APIError{StatusCode: 400, Code: "model_not_found", Message: "private provider details", RequestID: "req-private", Body: `{"error":"private provider details"}`},
			want:    "selected AI model",
			details: []string{"Status: 400"},
		},
		{name: "quota before rate limit", apiErr: &providers.APIError{StatusCode: 429, Code: "insufficient_quota"}, want: "usage limit"},
		{name: "context code before model text", apiErr: &providers.APIError{StatusCode: 400, Code: "context_length_exceeded", Message: "model context length exceeded"}, want: "conversation is too long"},
		{name: "model-specific code not found", apiErr: &providers.APIError{StatusCode: 404, Code: "model_not_found"}, want: "selected AI model"},
		{name: "Ollama model message not found", apiErr: &providers.APIError{StatusCode: 404, Message: "model 'missing' not found"}, want: "selected AI model"},
		{name: "generic not found", apiErr: &providers.APIError{StatusCode: 404, Code: "not_found"}, want: "could not complete"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := newProviderFailureError(test.apiErr, "ERR-123")
			message, presentation, ok := UserError(err)
			if !ok || presentation == nil || !strings.Contains(message, test.want) {
				t.Fatalf("message=%q presentation=%#v", message, presentation)
			}
			if strings.Contains(message, "private provider details") || strings.Contains(presentation.TechnicalDetails, "private provider details") {
				t.Fatalf("raw provider content leaked: message=%q details=%q", message, presentation.TechnicalDetails)
			}
			if test.apiErr.Code != "" && strings.Contains(presentation.TechnicalDetails, test.apiErr.Code) {
				t.Fatalf("provider code leaked: %q", presentation.TechnicalDetails)
			}
			if test.apiErr.RequestID != "" && strings.Contains(presentation.TechnicalDetails, test.apiErr.RequestID) {
				t.Fatalf("provider request ID leaked: %q", presentation.TechnicalDetails)
			}
			for _, detail := range test.details {
				if !strings.Contains(presentation.TechnicalDetails, detail) {
					t.Fatalf("details=%q, want %q", presentation.TechnicalDetails, detail)
				}
			}
			if strings.Contains(presentation.TechnicalDetails, "\nprivate") || len(presentation.TechnicalDetails) > 250 {
				t.Fatalf("unsafe technical details: %q", presentation.TechnicalDetails)
			}
		})
	}
}
