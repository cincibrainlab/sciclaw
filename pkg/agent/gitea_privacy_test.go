package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/hooks"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
)

const privateGiteaArgument = "PRIVATE_NESTED_ARGUMENT"
const privateGiteaResult = "PRIVATE_TOOL_ERROR"

type privacyProvider struct {
	calls    int
	received []providers.Message
}

func (p *privacyProvider) GetDefaultModel() string { return "test" }
func (p *privacyProvider) Chat(_ context.Context, msgs []providers.Message, _ []providers.ToolDefinition, _ string, _ map[string]interface{}) (*providers.LLMResponse, error) {
	p.calls++
	if p.calls == 1 {
		return &providers.LLMResponse{ToolCalls: []providers.ToolCall{{ID: "private-id", Name: "gitea_mcp", Arguments: map[string]interface{}{"tool": "project_read", "arguments": map[string]interface{}{"nested": privateGiteaArgument}}}}}, nil
	}
	p.received = msgs
	return &providers.LLMResponse{Content: "done"}, nil
}

type privacyTool struct{}

func (privacyTool) Name() string        { return "gitea_mcp" }
func (privacyTool) Description() string { return "test" }
func (privacyTool) Parameters() map[string]interface{} {
	return map[string]interface{}{"type": "object"}
}
func (privacyTool) Execute(context.Context, map[string]interface{}) *tools.ToolResult {
	return tools.ErrorResult(privateGiteaResult)
}

func TestGiteaAgentDebugAndHooksRedactPrivateToolData(t *testing.T) {
	var logs bytes.Buffer
	oldWriter, oldLevel := log.Writer(), logger.GetLevel()
	log.SetOutput(&logs)
	logger.SetLevel(logger.DEBUG)
	defer func() { log.SetOutput(oldWriter); logger.SetLevel(oldLevel) }()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	p := &privacyProvider{}
	al := NewAgentLoop(cfg, bus.NewMessageBus(), p)
	defer al.Stop()
	al.tools.Register(privacyTool{})
	handler := &captureHookHandler{contexts: make(map[hooks.Event]hooks.Context)}
	for _, event := range []hooks.Event{hooks.EventBeforeTool, hooks.EventAfterTool, hooks.EventOnError} {
		al.hooks.Register(event, handler)
	}
	if _, err := al.ProcessDirect(context.Background(), "hello", "privacy"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "Full LLM request") {
		t.Fatal("debug log not exercised")
	}
	for _, secret := range []string{privateGiteaArgument, privateGiteaResult} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("debug log leaked %s", secret)
		}
		for _, event := range []hooks.Event{hooks.EventBeforeTool, hooks.EventAfterTool, hooks.EventOnError} {
			data, ok := handler.contexts[event]
			if !ok {
				t.Fatalf("missing hook %s", event)
			}
			encoded, err := json.Marshal(data)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), secret) {
				t.Errorf("hook %s leaked %s", event, secret)
			}
		}
	}
	encoded, _ := json.Marshal(p.received)
	if !strings.Contains(string(encoded), privateGiteaArgument) || !strings.Contains(string(encoded), privateGiteaResult) {
		t.Fatal("provider lost tool data")
	}
}

func TestGiteaLogRedactsLegacyToolResultByCallID(t *testing.T) {
	messages := []providers.Message{
		{Role: "assistant", ToolCalls: []providers.ToolCall{
			{ID: "gitea", Function: &providers.FunctionCall{Name: "gitea_mcp", Arguments: privateGiteaArgument}},
			{ID: "other", Name: "other_tool", Function: &providers.FunctionCall{Name: "other_tool", Arguments: "public-argument"}},
		}},
		{Role: "tool", ToolCallID: "gitea", Content: privateGiteaResult},
		{Role: "tool", ToolCallID: "other", Content: "public-result"},
	}
	formatted := formatMessagesForLog(messages)
	for _, secret := range []string{privateGiteaArgument, privateGiteaResult} {
		if strings.Contains(formatted, secret) {
			t.Fatalf("private content logged: %s", formatted)
		}
	}
	if !strings.Contains(formatted, "public-argument") || !strings.Contains(formatted, "public-result") {
		t.Fatalf("unrelated data removed: %s", formatted)
	}
}
