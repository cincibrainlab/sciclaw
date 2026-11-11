package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

type GiteaMCPTool struct{}

func (*GiteaMCPTool) Name() string { return "gitea_mcp" }
func (*GiteaMCPTool) Description() string {
	return "Call a Gitea MCP tool with structured arguments without a shell."
}
func (*GiteaMCPTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"tool":      map[string]interface{}{"type": "string", "enum": []string{"pull_request_write", "issue_read", "project_read", "project_write", "get_file_contents", "create_or_update_file"}},
			"arguments": map[string]interface{}{"type": "object", "additionalProperties": true},
		},
		"required": []string{"tool", "arguments"},
	}
}

func (*GiteaMCPTool) Execute(ctx context.Context, args map[string]interface{}) *ToolResult {
	name, _ := args["tool"].(string)
	switch name {
	case "pull_request_write", "issue_read", "project_read", "project_write", "get_file_contents", "create_or_update_file":
	default:
		return ErrorResult("unsupported Gitea MCP tool")
	}
	arguments, ok := args["arguments"].(map[string]interface{})
	if !ok || arguments == nil {
		return ErrorResult("arguments must be a JSON object")
	}
	input, err := json.Marshal(arguments)
	if err != nil {
		return ErrorResult("cannot serialize Gitea MCP arguments: " + err.Error())
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "mcp-server-cli", "call", name, string(input))
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		return ErrorResult(fmt.Sprintf("Gitea MCP call failed: %v\n%s", err, detail))
	}
	return NewToolResult(stdout.String())
}
