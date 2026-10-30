package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
)

const providerDiagnosticCanary = "provider-diagnostic-canary"

type failingProvider struct{}

func (p *failingProvider) Chat(context.Context, []providers.Message, []providers.ToolDefinition, string, map[string]interface{}) (*providers.LLMResponse, error) {
	return nil, &providers.APIError{
		Provider:   "test-provider",
		StatusCode: 401,
		Message:    providerDiagnosticCanary,
		Body:       providerDiagnosticCanary,
	}
}

func (p *failingProvider) GetDefaultModel() string { return "test-model" }

type toolLoopEchoProvider struct {
	calls    int
	seen     [][]providers.Message
	toolCall *providers.ToolCall
}

func (p *toolLoopEchoProvider) Chat(ctx context.Context, messages []providers.Message, tools []providers.ToolDefinition, model string, options map[string]interface{}) (*providers.LLMResponse, error) {
	cloned := make([]providers.Message, len(messages))
	copy(cloned, messages)
	p.seen = append(p.seen, cloned)

	if p.calls == 0 {
		p.calls++
		if p.toolCall != nil {
			return &providers.LLMResponse{ToolCalls: []providers.ToolCall{*p.toolCall}}, nil
		}
		return &providers.LLMResponse{
			ToolCalls: []providers.ToolCall{
				{
					ID:        "tool-1",
					Name:      "big_output",
					Arguments: map[string]interface{}{},
				},
			},
		}, nil
	}
	p.calls++
	return &providers.LLMResponse{Content: lastToolMessageContent(messages)}, nil
}

func (p *toolLoopEchoProvider) GetDefaultModel() string { return "test-model" }

type fixedToolResultTool struct {
	name   string
	result *ToolResult
}

func (t *fixedToolResultTool) Name() string        { return t.name }
func (t *fixedToolResultTool) Description() string { return "test tool" }
func (t *fixedToolResultTool) Parameters() map[string]interface{} {
	return map[string]interface{}{"type": "object"}
}
func (t *fixedToolResultTool) Execute(ctx context.Context, args map[string]interface{}) *ToolResult {
	return t.result
}

func lastToolMessageContent(messages []providers.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "tool" {
			return messages[i].Content
		}
	}
	return ""
}

func TestRunToolLoop_TruncatesToolOutputWhenConfigured(t *testing.T) {
	provider := &toolLoopEchoProvider{}
	registry := NewToolRegistry()
	registry.Register(&fixedToolResultTool{
		name:   "big_output",
		result: NewToolResult(strings.Repeat("x", 120)),
	})

	result, err := RunToolLoop(context.Background(), ToolLoopConfig{
		Provider:           provider,
		Model:              "test-model",
		Tools:              registry,
		ToolResultMaxChars: 40,
	}, []providers.Message{{Role: "user", Content: "test"}}, "discord", "chat-1")
	if err != nil {
		t.Fatalf("RunToolLoop returned error: %v", err)
	}

	if result.Content == strings.Repeat("x", 120) {
		t.Fatal("expected tool output to be truncated before the second model turn")
	}
	if !strings.Contains(result.Content, "[tool output truncated: showing first 40 of 120 chars]") {
		t.Fatalf("expected truncation notice, got %q", result.Content)
	}
	if !strings.HasPrefix(result.Content, strings.Repeat("x", 40)) {
		t.Fatalf("expected truncated content prefix, got %q", result.Content)
	}
}

func TestRunToolLoop_RejectsOversizeGiteaProjectList(t *testing.T) {
	provider := &toolLoopEchoProvider{toolCall: &providers.ToolCall{
		ID: "tool-1", Name: "gitea_mcp",
		Arguments: map[string]interface{}{"tool": "project_read", "arguments": map[string]interface{}{"method": "list_columns", "page": 2}},
	}}
	registry := NewToolRegistry()
	registry.Register(&fixedToolResultTool{name: "gitea_mcp", result: NewToolResult(strings.Repeat("x", 500))})
	result, err := RunToolLoop(context.Background(), ToolLoopConfig{
		Provider: provider, Model: "test-model", Tools: registry, ToolResultMaxChars: 250,
	}, []providers.Message{{Role: "user", Content: "test"}}, "discord", "chat-1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "no results were returned") || strings.Contains(result.Content, strings.Repeat("x", 100)) {
		t.Fatalf("expected fail-closed result, got %q", result.Content)
	}
}

func TestRunToolLoop_LeavesToolOutputUnchangedWhenLimitDisabled(t *testing.T) {
	provider := &toolLoopEchoProvider{}
	registry := NewToolRegistry()
	full := strings.Repeat("y", 120)
	registry.Register(&fixedToolResultTool{
		name:   "big_output",
		result: NewToolResult(full),
	})

	result, err := RunToolLoop(context.Background(), ToolLoopConfig{
		Provider: provider,
		Model:    "test-model",
		Tools:    registry,
	}, []providers.Message{{Role: "user", Content: "test"}}, "discord", "chat-1")
	if err != nil {
		t.Fatalf("RunToolLoop returned error: %v", err)
	}

	if result.Content != full {
		t.Fatalf("expected unbounded tool output when limit disabled, got %q", result.Content)
	}
}

func TestRunToolLoop_ProviderFailureLogsRawDiagnostics(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "provider-errors.log")
	previousLevel := logger.GetLevel()
	logger.SetLevel(logger.INFO)
	defer logger.SetLevel(previousLevel)
	if err := logger.EnableFileLogging(logPath); err != nil {
		t.Fatalf("EnableFileLogging: %v", err)
	}
	defer logger.DisableFileLogging()

	_, err := RunToolLoop(context.Background(), ToolLoopConfig{
		Provider: &failingProvider{},
		Model:    "test-model",
	}, []providers.Message{{Role: "user", Content: "test"}}, "cli", "direct")
	if err == nil {
		t.Fatal("expected RunToolLoop to return an error")
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var entry logger.LogEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("parse log entry: %v", err)
		}
		if entry.Component == "toolloop" && entry.Message == "LLM call failed" {
			if !strings.Contains(entry.Fields["error"].(string), providerDiagnosticCanary) {
				t.Fatalf("raw diagnostic missing from log entry: %#v", entry)
			}
			return
		}
	}
	t.Fatal("toolloop LLM failure log entry not found")
}

func TestRunToolLoop_TruncatesErrorFallbackWhenConfigured(t *testing.T) {
	provider := &toolLoopEchoProvider{}
	registry := NewToolRegistry()
	errText := strings.Repeat("boom", 40)
	registry.Register(&fixedToolResultTool{
		name: "big_output",
		result: (&ToolResult{
			IsError: true,
			Err:     errors.New(errText),
		}),
	})

	result, err := RunToolLoop(context.Background(), ToolLoopConfig{
		Provider:           provider,
		Model:              "test-model",
		Tools:              registry,
		ToolResultMaxChars: 32,
	}, []providers.Message{{Role: "user", Content: "test"}}, "discord", "chat-1")
	if err != nil {
		t.Fatalf("RunToolLoop returned error: %v", err)
	}

	if !strings.Contains(result.Content, "[tool output truncated: showing first 32 of 160 chars]") {
		t.Fatalf("expected truncation notice for error fallback, got %q", result.Content)
	}
}
