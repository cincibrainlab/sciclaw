package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestGiteaMCPTransmitsArgumentsWithoutShell(t *testing.T) {
	dir := t.TempDir()
	cli := filepath.Join(dir, "mcp-server-cli")
	if err := os.WriteFile(cli, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$CAPTURE\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CAPTURE", filepath.Join(dir, "capture"))
	marker := filepath.Join(dir, "injected")
	value := "O'Brien\n$(touch " + marker + "); `touch " + marker + "` & |"
	args := map[string]interface{}{"method": "create", "title": value, "nested": map[string]interface{}{"path": value}}
	for _, name := range []string{"pull_request_write", "issue_read", "project_read", "project_write", "get_file_contents", "create_or_update_file"} {
		result := (&GiteaMCPTool{}).Execute(context.Background(), map[string]interface{}{"tool": name, "arguments": args})
		if result.IsError {
			t.Fatalf("call failed: %s", result.ForLLM)
		}
		b, err := os.ReadFile(filepath.Join(dir, "capture"))
		if err != nil {
			t.Fatal(err)
		}
		// JSON serialization escapes newlines, so each CLI argument occupies one line.
		lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
		if len(lines) != 3 || lines[0] != "call" || lines[1] != name {
			t.Fatalf("wrong argv: %q", lines)
		}
		var decoded map[string]interface{}
		if err := json.Unmarshal([]byte(lines[2]), &decoded); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(decoded, args) {
			t.Fatalf("arguments changed: %#v", decoded)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("injection marker exists: %v", err)
		}
	}
}

func TestGiteaMCPReportsCLIErrorOutput(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "mcp-server-cli"), []byte("#!/bin/sh\nprintf 'server rejected request'\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	result := (&GiteaMCPTool{}).Execute(context.Background(), map[string]interface{}{"tool": "project_read", "arguments": map[string]interface{}{}})
	if !result.IsError || !strings.Contains(result.ForLLM, "server rejected request") {
		t.Fatalf("missing error details: %+v", result)
	}
}
