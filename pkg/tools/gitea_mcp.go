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

func (t *GiteaMCPTool) Name() string { return "gitea_mcp" }

func (t *GiteaMCPTool) Description() string {
	return "Call an official Gitea MCP tool with structured arguments, without a shell."
}

func (t *GiteaMCPTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"tool":      map[string]interface{}{"type": "string", "enum": []string{"issue_read", "project_read", "project_write", "get_file_contents", "create_or_update_file", "pull_request_write"}},
			"arguments": map[string]interface{}{"type": "object", "additionalProperties": true},
		},
		"required": []string{"tool", "arguments"},
	}
}

func (t *GiteaMCPTool) Execute(ctx context.Context, args map[string]interface{}) *ToolResult {
	name, _ := args["tool"].(string)
	switch name {
	case "issue_read", "project_read", "project_write", "get_file_contents", "create_or_update_file", "pull_request_write":
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
	cmd := exec.CommandContext(ctx, "mcp-server-cli", "call", name, "--stdin")
	cmd.Stdin = bytes.NewReader(input)
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
