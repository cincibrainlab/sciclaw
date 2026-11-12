package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
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

func TestGiteaMCPRegistryRedactsArgumentsAndErrors(t *testing.T) {
	dir := t.TempDir()
	secret := "PRIVATE-GITEA-REVIEW-TEXT"
	if err := os.WriteFile(filepath.Join(dir, "mcp-server-cli"), []byte("#!/bin/sh\nprintf '%s' \"$3\"\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previous)
	registry := NewToolRegistry()
	registry.Register(&GiteaMCPTool{})
	result := registry.Execute(context.Background(), "gitea_mcp", map[string]interface{}{"tool": "project_read", "arguments": map[string]interface{}{"title": secret}})
	if !result.IsError || !strings.Contains(result.ForLLM, secret) {
		t.Fatalf("caller lost error details: %+v", result)
	}
	if strings.Contains(logs.String(), secret) {
		t.Fatalf("private data logged: %s", logs.String())
	}
}

func TestGiteaMCPRejectsUnsafeNumericIDs(t *testing.T) {
	result := (&GiteaMCPTool{}).Execute(context.Background(), map[string]interface{}{
		"tool": "project_read", "arguments": map[string]interface{}{"nested": []interface{}{map[string]interface{}{"id": float64(9007199254740992)}}},
	})
	if !result.IsError || !strings.Contains(result.ForLLM, "numeric") {
		t.Fatalf("unsafe numeric ID accepted: %+v", result)
	}
}

func TestGiteaMCPWithBundledCLIAndMCPServer(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun not installed")
	}
	cliSource, err := filepath.Abs(filepath.Join("..", "..", "tools", "mcp-server-cli"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cliSource, "node_modules")); err != nil {
		t.Skip("CLI dependencies not installed")
	}
	dir := t.TempDir()
	source, err := os.ReadFile(filepath.Join(cliSource, "src", "index.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.ts"), source, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(cliSource, "node_modules"), filepath.Join(dir, "node_modules")); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bun, "build", "--compile", "--outfile", filepath.Join(dir, "mcp-server-cli"), "index.ts")
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v: %s", err, output)
	}
	marker := filepath.Join(dir, "injected")
	value := "O'Brien\n$(touch " + marker + "); `touch " + marker + "` & |"
	var calls []map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("request: %v", err)
			w.WriteHeader(400)
			return
		}
		if len(req.ID) == 0 {
			w.WriteHeader(202)
			return
		}
		var result interface{}
		switch req.Method {
		case "initialize":
			result = map[string]interface{}{"protocolVersion": "2025-03-26", "capabilities": map[string]interface{}{}, "serverInfo": map[string]interface{}{"name": "test", "version": "1"}}
		case "tools/call":
			var params struct {
				Name      string                 `json:"name"`
				Arguments map[string]interface{} `json:"arguments"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil {
				t.Error(err)
			}
			calls = append(calls, params.Arguments)
			result = map[string]interface{}{"content": []interface{}{map[string]interface{}{"type": "text", "text": "rejected"}}, "isError": true}
		default:
			t.Errorf("unexpected method %s", req.Method)
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	defer server.Close()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SCICLAW_MCP_URL", server.URL)
	args := map[string]interface{}{"title": value, "nested": map[string]interface{}{"id": float64(9007199254740991)}}
	result := (&GiteaMCPTool{}).Execute(context.Background(), map[string]interface{}{"tool": "pull_request_write", "arguments": args})
	if !result.IsError || !strings.Contains(result.ForLLM, "rejected") {
		t.Fatalf("error not returned: %+v", result)
	}
	if len(calls) != 1 || !reflect.DeepEqual(calls[0], args) {
		t.Fatalf("wrong transmitted arguments: %#v", calls)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("injection marker exists: %v", err)
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
