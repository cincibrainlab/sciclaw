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
