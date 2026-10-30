package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
)

type paginationResultTool struct{ content string }

func (t *paginationResultTool) Name() string        { return "gitea_mcp" }
func (t *paginationResultTool) Description() string { return "test Gitea result" }
func (t *paginationResultTool) Parameters() map[string]interface{} {
	return map[string]interface{}{"type": "object"}
}
func (t *paginationResultTool) Execute(context.Context, map[string]interface{}) *tools.ToolResult {
	return tools.NewToolResult(t.content)
}

func TestGiteaListResultLimit(t *testing.T) {
	for _, tc := range []struct {
		name, method             string
		local, explicit          bool
		size                     int
		wantError, wantTruncated bool
	}{
		{"default aggregate over limit", "list_columns", false, false, defaultToolResultMaxChars + 1, true, false},
		{"local aggregate over limit", "list_column_issues", true, false, localToolResultMaxChars + 1, true, false},
		{"explicit page over limit", "list", false, true, defaultToolResultMaxChars + 1, true, false},
		{"at limit", "list_columns", false, false, defaultToolResultMaxChars, false, false},
		{"other method unchanged", "get", false, false, defaultToolResultMaxChars + 1, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.Agents.Defaults.Workspace = t.TempDir()
			arguments := map[string]interface{}{"method": tc.method}
			if tc.explicit {
				arguments["page"] = 2
			}
			provider := &scriptedProvider{responses: []*providers.LLMResponse{
				{ToolCalls: []providers.ToolCall{{ID: "call-1", Name: "gitea_mcp", Arguments: map[string]interface{}{"tool": "project_read", "arguments": arguments}}}},
				{Content: "done"},
			}}
			al := NewAgentLoop(cfg, bus.NewMessageBus(), provider)
			al.localMode = tc.local
			al.tools.Register(&paginationResultTool{content: strings.Repeat("x", tc.size)})
			_, err := al.ProcessDirectWithChannel(context.Background(), "inspect project", "test-session", "test", "chat", "user")
			if err != nil {
				t.Fatal(err)
			}
			if len(provider.calls) < 2 {
				t.Fatalf("expected second provider call, got %d", len(provider.calls))
			}
			var result string
			for _, message := range provider.calls[1] {
				if message.Role == "tool" && message.ToolCallID == "call-1" {
					result = message.Content
				}
			}
			if tc.wantError {
				if !strings.Contains(result, "no results were returned") || strings.Contains(result, strings.Repeat("x", 100)) {
					t.Fatalf("expected fail-closed result, got %q", result)
				}
			} else if tc.wantTruncated {
				if !strings.Contains(result, "tool output truncated") {
					t.Fatalf("expected normal truncation: %q", result)
				}
			} else if result != strings.Repeat("x", tc.size) {
				t.Fatalf("result changed at limit: length %d", len(result))
			}
		})
	}
}
