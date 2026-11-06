package providers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAdapterChatClassifiesResponseDecodingFailures(t *testing.T) {
	adapters := []struct {
		name string
		new  func(string) LLMProvider
	}{
		{"azure", func(endpoint string) LLMProvider { return NewAzureProvider("test-key", endpoint, "") }},
		{"ollama", func(endpoint string) LLMProvider { return NewOllamaProvider(endpoint, time.Second) }},
	}
	responses := []struct {
		name string
		body string
	}{
		{"HTML", "<html>bad upstream</html>"},
		{"wrong field type", `{"choices":"wrong-type","message":"wrong-type"}`},
	}

	for _, adapter := range adapters {
		for _, response := range responses {
			t.Run(adapter.name+"/"+response.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = w.Write([]byte(response.body))
				}))
				defer server.Close()

				_, err := adapter.new(server.URL).Chat(context.Background(), nil, nil, "test-model", nil)
				if err == nil {
					t.Fatal("Chat() error = nil")
				}
				for _, candidate := range []error{err, fmt.Errorf("wrapped: %w", err)} {
					if got := ClassifyProviderError(candidate).Kind; got != ProviderErrorKindInvalidResponse {
						t.Fatalf("ClassifyProviderError().Kind = %q, want %q", got, ProviderErrorKindInvalidResponse)
					}
				}
			})
		}
	}
}
