package providers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestCodexMissingResponseAgentPresentation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"RAW_CANARY\"}\n\n"))
	}))
	defer server.Close()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.Model = "gpt-5.6-sol"
	loop := agent.NewAgentLoop(cfg, bus.NewMessageBus(), providers.NewTestCodexProvider(server.URL))
	defer loop.Stop()
	_, err := loop.ProcessDirect(context.Background(), "hello", "codex-missing-response")
	if err == nil {
		t.Fatal("expected error")
	}
	info := providers.ClassifyProviderError(err)
	if info.Provider != "Codex" || info.Kind != providers.ProviderErrorKindInvalidResponse {
		t.Fatalf("classification = %+v", info)
	}
	message, presentation, ok := agent.UserError(err)
	if !ok || presentation == nil || !strings.Contains(message, "temporarily unavailable") || strings.Contains(message, "RAW_CANARY") || strings.Contains(presentation.TechnicalDetails, "RAW_CANARY") {
		t.Fatalf("unsafe or incorrect presentation: %q, %#v", message, presentation)
	}
}
