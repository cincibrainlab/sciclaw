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

// Exercise Execute through its real subprocess/stdin boundary, never a remote service.
func localPathCLI(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	marker := filepath.Join(dir, "invoked")
	t.Setenv("UPLOAD_MARKER", marker)
	script := "#!/usr/bin/python3\nimport os, sys, json\nopen(os.environ['UPLOAD_MARKER'], 'w').close()\n" + body
	if err := os.WriteFile(filepath.Join(dir, "mcp-server-cli"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return marker
}

func TestGiteaLocalPathBinary(t *testing.T) {
	file := filepath.Join(t.TempDir(), "binary")
	data := []byte{0, 1, 2, 255}
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	localPathCLI(t, `assert sys.argv[1:] == ['call', 'create_or_update_file', '--stdin', '--require-base64-support']
a = json.load(sys.stdin)
assert a == {'owner':'o', 'files':[{'path':'dest.bin', 'sha':'sha', 'from_path':'old.bin', 'encoding':'base64', 'content':'AAEC/w=='}]}, a
print('{"content":[{"type":"text","text":"AAEC/w=="}]}')
`)
	entry := map[string]interface{}{"path": "dest.bin", "sha": "sha", "from_path": "old.bin", "local_path": file, "encoding": "base64"}
	args := map[string]interface{}{"tool": "create_or_update_file", "arguments": map[string]interface{}{"owner": "o", "files": []interface{}{entry}}}
	before, _ := json.Marshal(args)
	result := (&GiteaMCPTool{}).Execute(context.Background(), args)
	if result.IsError || strings.Contains(result.ForLLM, "AAEC/w==") || strings.Contains(result.ForUser, "AAEC/w==") {
		t.Fatalf("upload result: %#v", result)
	}
	after, _ := json.Marshal(args)
	if string(before) != string(after) {
		t.Fatal("caller arguments were modified")
	}
	got, err := os.ReadFile(file)
	if err != nil || !reflect.DeepEqual(got, data) {
		t.Fatalf("source changed: %v", err)
	}
}

func TestGiteaLocalPathPolicy(t *testing.T) {
	workspace, shared, outside := t.TempDir(), t.TempDir(), t.TempDir()
	for _, dir := range []string{workspace, shared, outside} {
		if err := os.WriteFile(filepath.Join(dir, "file"), []byte{255}, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("file", filepath.Join(workspace, "leaf")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(shared, filepath.Join(workspace, "shared-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(workspace, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".", filepath.Join(workspace, "inside")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name              string
		path              interface{}
		restrict, allowed bool
	}{
		{"relative", "file", true, true},
		{"normalized", "sub/../file", true, true},
		{"absolute", filepath.Join(workspace, "file"), true, true},
		{"shared read-only", filepath.Join(shared, "file"), true, true},
		{"inside directory symlink", "inside/file", true, true},
		{"outside unrestricted", filepath.Join(outside, "file"), false, true},
		{"outside restricted", filepath.Join(outside, "file"), true, false},
		{"missing", "missing-private-name", true, false},
		{"directory", ".", true, false},
		{"leaf symlink", "leaf", true, false},
		{"escape", "escape/file", true, false},
		{"other allowed root symlink", "shared-link/file", true, false},
		{"empty", "", true, false},
		{"nul", "private\x00name", true, false},
		{"non-string", 42, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			marker := localPathCLI(t, "a=json.load(sys.stdin)\nassert a == {'content':'/w==','encoding':'base64'}\nprint('{}')\n")
			tool := NewGiteaMCPTool(workspace, tc.restrict)
			tool.SetSharedWorkspacePolicy(shared, true)
			result := tool.Execute(context.Background(), map[string]interface{}{"tool": "create_or_update_file", "arguments": map[string]interface{}{"local_path": tc.path}})
			if result.IsError == tc.allowed {
				t.Fatalf("allowed=%v result=%#v", tc.allowed, result)
			}
			if !tc.allowed {
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatal("invalid upload launched CLI")
				}
				for _, secret := range []string{workspace, shared, outside, "missing-private-name", "private"} {
					if strings.Contains(result.ForLLM, secret) {
						t.Fatalf("path leaked: %s", result.ForLLM)
					}
				}
			}
		})
	}
}

func TestGiteaLocalPathConflicts(t *testing.T) {
	for _, conflict := range []map[string]interface{}{
		{"content": "secret"}, {"content": nil}, {"edits": []interface{}{}}, {"delete": true}, {"encoding": "utf-8"}, {"encoding": nil},
	} {
		marker := localPathCLI(t, "print('{}')\n")
		entry := map[string]interface{}{"local_path": "/secret-missing"}
		for k, v := range conflict {
			entry[k] = v
		}
		result := (&GiteaMCPTool{}).Execute(context.Background(), map[string]interface{}{"tool": "create_or_update_file", "arguments": entry})
		if !result.IsError || strings.Contains(result.ForLLM, "secret") {
			t.Fatalf("conflict result: %#v", result)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatal("conflict launched CLI")
		}
	}
}

func TestGiteaLocalPathLimits(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name    string
		size    int64
		count   int
		message int
		allowed bool
	}{
		{"raw boundary", 20 << 20, 1, 0, true},
		{"raw overflow", (20 << 20) + 1, 1, 0, false},
		{"aggregate overflow", 11 << 20, 2, 0, false},
		{"JSON boundary", 0, 1, (31 << 20) - len(`{"files":[{"content":"","encoding":"base64"}],"message":""}`), true},
		{"JSON overflow", 0, 1, 31 << 20, false},
		{"empty", 0, 1, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := filepath.Join(dir, "file")
			f, err := os.Create(file)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.Truncate(tc.size); err != nil {
				t.Fatal(err)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			marker := localPathCLI(t, "json.load(sys.stdin)\nprint('{}')\n")
			files := make([]interface{}, tc.count)
			for i := range files {
				files[i] = map[string]interface{}{"local_path": file}
			}
			result := NewGiteaMCPTool(dir, true).Execute(context.Background(), map[string]interface{}{"tool": "create_or_update_file", "arguments": map[string]interface{}{"files": files, "message": strings.Repeat("x", tc.message)}})
			if result.IsError == tc.allowed {
				t.Fatalf("allowed=%v: %#v", tc.allowed, result)
			}
			if !tc.allowed {
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatal("oversized upload launched CLI")
				}
			}
		})
	}
}

func TestGiteaLocalPathErrorRedaction(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("private payload"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		"sys.stderr.write(sys.stdin.read()); sys.exit(1)\n",
		"a=json.load(sys.stdin); print(json.dumps({'isError':True,'content':a}))\n",
	} {
		localPathCLI(t, body)
		result := NewGiteaMCPTool(dir, true).Execute(context.Background(), map[string]interface{}{"tool": "create_or_update_file", "arguments": map[string]interface{}{"local_path": "file"}})
		if !result.IsError || !strings.Contains(strings.ToLower(result.ForLLM), "reconcile") || strings.Contains(result.ForLLM, "cHJpdmF0ZSBwYXlsb2Fk") || strings.Contains(result.ForLLM, dir) {
			t.Fatalf("unsafe error: %#v", result)
		}
	}
}
