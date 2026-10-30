package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGiteaMCPInvocation(t *testing.T) {
	dir := t.TempDir()
	script := "#!/usr/bin/python3\nimport sys, os\nif sys.argv[1:] != ['call', 'pull_request_write', '--stdin'] or os.environ.get('SCICLAW_MCP_URL') != 'http://example.test/mcp':\n sys.exit(2)\nsys.stdout.write(sys.stdin.read())\n"
	if err := os.WriteFile(filepath.Join(dir, "mcp-server-cli"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("SCICLAW_MCP_URL", "http://example.test/mcp")
	marker := filepath.Join(dir, "injected")
	value := "O'Brien \"quote\"\n$(touch " + marker + "); `touch " + marker + "` | & \\\""
	result := (&GiteaMCPTool{}).Execute(context.Background(), map[string]interface{}{"tool": "pull_request_write", "arguments": map[string]interface{}{"title": value}})
	if result.IsError {
		t.Fatal(result.ForLLM)
	}
	var got map[string]interface{}
	if err := json.Unmarshal([]byte(result.ForLLM), &got); err != nil || got["title"] != value {
		t.Fatalf("result %q: %v", result.ForLLM, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("injection marker exists: %v", err)
	}
}

func TestGiteaMCPErrors(t *testing.T) {
	tool := &GiteaMCPTool{}
	for _, args := range []map[string]interface{}{
		{"tool": "unknown", "arguments": map[string]interface{}{}},
		{"tool": "issue_read", "arguments": "{}"},
		{"tool": "issue_read", "arguments": map[string]interface{}{"bad": make(chan int)}},
	} {
		if result := tool.Execute(context.Background(), args); !result.IsError {
			t.Fatalf("expected error: %#v", args)
		}
	}
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	args := map[string]interface{}{"tool": "issue_read", "arguments": map[string]interface{}{}}
	if result := tool.Execute(context.Background(), args); !result.IsError {
		t.Fatal("missing executable succeeded")
	}
	if err := os.WriteFile(filepath.Join(dir, "mcp-server-cli"), []byte("#!/usr/bin/python3\nimport sys\nsys.stderr.write('server failed')\nsys.exit(1)\n"), 0755); err != nil {
		t.Fatal(err)
	}
	result := tool.Execute(context.Background(), args)
	if !result.IsError || !strings.Contains(result.ForLLM, "server failed") {
		t.Fatalf("unexpected result: %#v", result)
	}
	if err := os.WriteFile(filepath.Join(dir, "mcp-server-cli"), []byte("#!/bin/sh\necho 'MCP tool failed'\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	result = tool.Execute(context.Background(), args)
	if !result.IsError || !strings.Contains(result.ForLLM, "MCP tool failed") {
		t.Fatalf("stdout error not propagated: %#v", result)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result := tool.Execute(ctx, args); !result.IsError {
		t.Fatal("canceled call succeeded")
	}
}

func TestLimitGiteaProjectListResult(t *testing.T) {
	list := map[string]interface{}{"tool": "project_read", "arguments": map[string]interface{}{"method": "list_columns"}}
	oversize := NewToolResult("  12345  ")
	for _, tc := range []struct {
		name      string
		tool      string
		args      map[string]interface{}
		result    *ToolResult
		limit     int
		wantError bool
	}{
		{"oversize", "gitea_mcp", list, oversize, 4, true},
		{"at limit after trim", "gitea_mcp", list, oversize, 5, false},
		{"disabled", "gitea_mcp", list, oversize, 0, false},
		{"other tool", "other", list, oversize, 4, false},
		{"other method", "gitea_mcp", map[string]interface{}{"tool": "project_read", "arguments": map[string]interface{}{"method": "get"}}, oversize, 4, false},
		{"existing error", "gitea_mcp", list, ErrorResult("existing error"), 4, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := LimitGiteaProjectListResult(tc.tool, tc.args, tc.result, tc.limit)
			if tc.wantError {
				if !got.IsError || !strings.Contains(got.ForLLM, "until an empty page") || !strings.Contains(got.ForLLM, "restart at page 1") {
					t.Fatalf("missing fail-closed pagination guidance: %#v", got)
				}
			} else if got != tc.result {
				t.Fatalf("unexpected replacement: %#v", got)
			}
		})
	}
}

func TestGiteaMCPPagination(t *testing.T) {
	dir := t.TempDir()
	script := `#!/usr/bin/python3
import json, sys
args = json.load(sys.stdin)
assert args['per_page'] == 50
page = args['page']
items = [{'id': page}] if page < 3 else []
print(json.dumps({'content': [{'type': 'text', 'text': json.dumps(items)}]}))
`
	if err := os.WriteFile(filepath.Join(dir, "mcp-server-cli"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	result := (&GiteaMCPTool{}).Execute(context.Background(), map[string]interface{}{"tool": "project_read", "arguments": map[string]interface{}{"method": "list_columns"}})
	if result.IsError {
		t.Fatal(result.ForLLM)
	}
	var response struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal([]byte(result.ForLLM), &response); err != nil || len(response.Content) != 1 {
		t.Fatalf("invalid response: %s: %v", result.ForLLM, err)
	}
	var items []map[string]int
	if err := json.Unmarshal([]byte(response.Content[0].Text), &items); err != nil || len(items) != 2 || items[0]["id"] != 1 || items[1]["id"] != 2 {
		t.Fatalf("unexpected items: %s: %v", response.Content[0].Text, err)
	}
}

func TestGiteaMCPPaginationBehavior(t *testing.T) {
	dir := t.TempDir()
	script := `#!/usr/bin/python3
import json, sys
args = json.load(sys.stdin)
case = args.get('case')
page = args.get('page', 0)
if case == 'explicit':
    print(json.dumps(args))
    sys.exit(0)
if case == 'failure' and page == 2:
    print(json.dumps({'isError': True, 'content': [{'type': 'text', 'text': 'server denied access'}]}))
    sys.exit(0)
if case == 'malformed' and page == 2:
    print('not json')
    sys.exit(0)
if case == 'null' and page == 2:
    items = None
elif case == 'repeat':
    items = [{'id': 1}]
else:
    items = [{'id': page}] if page < 3 else []
print(json.dumps({'content': [{'type': 'text', 'text': json.dumps(items)}]}))
`
	if err := os.WriteFile(filepath.Join(dir, "mcp-server-cli"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	tool := &GiteaMCPTool{}
	for _, method := range []string{"list", "list_columns", "list_column_issues"} {
		t.Run(method, func(t *testing.T) {
			args := map[string]interface{}{"method": method, "per_page": 2}
			result := tool.Execute(context.Background(), map[string]interface{}{"tool": "project_read", "arguments": args})
			if result.IsError || !strings.Contains(result.ForLLM, `\"id\":1`) || !strings.Contains(result.ForLLM, `\"id\":2`) {
				t.Fatalf("incomplete result: %#v", result)
			}
			if _, ok := args["page"]; ok || args["per_page"] != 2 {
				t.Fatalf("arguments mutated: %#v", args)
			}
		})
	}
	for _, page := range []interface{}{float64(2), float64(0), nil, "bad"} {
		args := map[string]interface{}{"method": "list_columns", "case": "explicit", "page": page}
		result := tool.Execute(context.Background(), map[string]interface{}{"tool": "project_read", "arguments": args})
		var got map[string]interface{}
		if result.IsError || json.Unmarshal([]byte(result.ForLLM), &got) != nil || got["page"] != page {
			t.Fatalf("explicit page %v not passed through: %#v", page, result)
		}
		if _, ok := got["per_page"]; ok {
			t.Fatalf("explicit page modified: %#v", got)
		}
	}
	for _, tc := range []struct{ name, message string }{
		{"failure", "server denied access"}, {"malformed", "invalid Gitea MCP pagination response"},
		{"null", "invalid Gitea MCP pagination results"}, {"repeat", "repeated page"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := tool.Execute(context.Background(), map[string]interface{}{"tool": "project_read", "arguments": map[string]interface{}{"method": "list_columns", "case": tc.name}})
			if !result.IsError || !strings.Contains(result.ForLLM, tc.message) {
				t.Fatalf("expected %q error, got %#v", tc.message, result)
			}
		})
	}
}
