package providers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"syscall"
	"testing"
	"time"
)

func TestRuntimeErrorProviderIdentity(t *testing.T) {
	adapters := []struct {
		name string
		new  func(string) LLMProvider
	}{
		{"HTTP", func(url string) LLMProvider { return NewHTTPProvider("key", url, "") }},
		{"Azure", func(url string) LLMProvider { return NewAzureProvider("key", url, "") }},
		{"Ollama", func(url string) LLMProvider { return NewOllamaProvider(url, time.Second) }},
		{"Codex", func(url string) LLMProvider {
			p := NewCodexProvider("key", "")
			p.client = createOpenAITestClient(url, "key", "")
			return p
		}},
	}
	for _, adapter := range adapters {
		t.Run(adapter.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if adapter.name == "Codex" {
					w.Header().Set("Content-Type", "text/event-stream")
				} else {
					_, _ = w.Write([]byte("<html>bad response</html>"))
				}
			}))
			provider := adapter.new(server.URL)
			_, err := provider.Chat(context.Background(), []Message{{Role: "user", Content: "hello"}}, nil, "gpt-5.6-sol", nil)
			if err == nil {
				t.Fatal("missing response error")
			}
			for _, wrapped := range []error{err, fmt.Errorf("wrapped: %w", err)} {
				info := ClassifyProviderError(wrapped)
				if info.Provider != adapter.name || info.Kind != ProviderErrorKindInvalidResponse {
					t.Fatalf("response error classification = %+v, error %v", info, wrapped)
				}
			}
			server.Close()
			_, err = provider.Chat(context.Background(), []Message{{Role: "user", Content: "hello"}}, nil, "gpt-5.6-sol", nil)
			if err == nil {
				t.Fatal("missing transport error")
			}
			info := ClassifyProviderError(err)
			if info.Provider != adapter.name || info.Kind != ProviderErrorKindNetwork || !errors.Is(err, syscall.ECONNREFUSED) {
				t.Fatalf("transport error classification = %+v, error %v", info, err)
			}
		})
	}
}

func TestCodexLocalModelErrorIsNotInvalidResponse(t *testing.T) {
	p := NewCodexProvider("key", "")
	_, err := p.Chat(context.Background(), nil, nil, "not-a-codex-model", nil)
	if err == nil || ClassifyProviderError(err).Kind == ProviderErrorKindInvalidResponse {
		t.Fatalf("local model error = %v", err)
	}
}

func TestClaudeTransportErrorProviderIdentity(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	server.Close()
	p := NewClaudeProvider("key")
	p.client = createAnthropicTestClient(server.URL, "key")
	_, err := p.Chat(context.Background(), []Message{{Role: "user", Content: "hello"}}, nil, "claude-sonnet-4.6", nil)
	if err == nil {
		t.Fatal("missing transport error")
	}
	info := ClassifyProviderError(fmt.Errorf("wrapped: %w", err))
	if info.Provider != "Claude" || info.Kind != ProviderErrorKindNetwork || !errors.Is(err, syscall.ECONNREFUSED) {
		t.Fatalf("transport error classification = %+v, error %v", info, err)
	}
}

// NewTestCodexProvider connects the real adapter to a local test server.
func NewTestCodexProvider(url string) *CodexProvider {
	p := NewCodexProvider("key", "")
	p.client = createOpenAITestClient(url, "key", "")
	return p
}
