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

// LimitGiteaProjectListResult prevents partial project lists from reaching an agent.
func LimitGiteaProjectListResult(name string, args map[string]interface{}, result *ToolResult, limit int) *ToolResult {
	if name != "gitea_mcp" || result.IsError || limit <= 0 || len(strings.TrimSpace(result.ForLLM)) <= limit {
		return result
	}
	arguments, _ := args["arguments"].(map[string]interface{})
	method, _ := arguments["method"].(string)
	tool, _ := args["tool"].(string)
	if tool == "project_read" && (method == "list" || method == "list_columns" || method == "list_column_issues") {
		return ErrorResult("Gitea project list exceeds the agent tool-result limit; no results were returned. Retry with explicit page and smaller per_page, starting at page 1 and continuing until an empty page. If per_page changes, restart at page 1. If one item cannot fit, stop without using partial results.")
	}
	return result
}

func (t *GiteaMCPTool) Name() string { return "gitea_mcp" }

func (t *GiteaMCPTool) Description() string {
	return "Call an official Gitea MCP tool with structured arguments, without a shell."
}

func (t *GiteaMCPTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"tool": map[string]interface{}{
				"type": "string",
				"enum": []string{
					"issue_read",
					"project_read",
					"project_write",
					"get_file_contents",
					"create_or_update_file",
					"pull_request_write",
				},
			},
			"arguments": map[string]interface{}{
				"type":                 "object",
				"additionalProperties": true,
			},
		},
		"required": []string{"tool", "arguments"},
	}
}

func (t *GiteaMCPTool) Execute(ctx context.Context, args map[string]interface{}) *ToolResult {
	name, _ := args["tool"].(string)
	switch name {
	case "issue_read", "project_read", "project_write",
		"get_file_contents", "create_or_update_file", "pull_request_write":
	default:
		return ErrorResult("unsupported Gitea MCP tool")
	}

	arguments, ok := args["arguments"].(map[string]interface{})
	if !ok || arguments == nil {
		return ErrorResult("arguments must be a JSON object")
	}

	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	method, _ := arguments["method"].(string)
	_, explicitPage := arguments["page"]
	paginate := !explicitPage && name == "project_read" && (method == "list" || method == "list_columns" || method == "list_column_issues")
	if paginate {
		copied := make(map[string]interface{}, len(arguments)+2)
		for key, value := range arguments {
			copied[key] = value
		}
		arguments = copied
		if _, ok := arguments["per_page"]; !ok {
			arguments["per_page"] = 50
		}
		arguments["page"] = 1
	}
	var all []json.RawMessage
	seen := make(map[string]bool)
	for pageNumber := 1; ; pageNumber++ {
		input, err := json.Marshal(arguments)
		if err != nil {
			return ErrorResult("cannot serialize Gitea MCP arguments: " + err.Error())
		}
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
		if !paginate {
			return NewToolResult(stdout.String())
		}
		var result struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
			return ErrorResult("invalid Gitea MCP pagination response")
		}
		if result.IsError {
			return ErrorResult("Gitea MCP pagination call failed: " + stdout.String())
		}
		if len(result.Content) != 1 || result.Content[0].Type != "text" {
			return ErrorResult("invalid Gitea MCP pagination response")
		}
		var page []json.RawMessage
		if err := json.Unmarshal([]byte(result.Content[0].Text), &page); err != nil || page == nil {
			return ErrorResult("invalid Gitea MCP pagination results")
		}
		if len(page) == 0 {
			if all == nil {
				all = []json.RawMessage{}
			}
			combined, _ := json.Marshal(all)
			result.Content[0].Text = string(combined)
			output, _ := json.Marshal(result)
			return NewToolResult(string(output))
		}
		// A server that ignores page can otherwise return the same data indefinitely.
		pageJSON, _ := json.Marshal(page)
		if seen[string(pageJSON)] {
			return ErrorResult("Gitea MCP pagination returned a repeated page; results may be incomplete")
		}
		seen[string(pageJSON)] = true
		all = append(all, page...)
		arguments["page"] = pageNumber + 1
	}
}
