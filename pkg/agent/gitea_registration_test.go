package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
)

func TestGiteaRegistrationLocalPathPolicy(t *testing.T) {
	workspace, shared, outside, bin := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	for _, dir := range []string{workspace, shared, outside} {
		if err := os.WriteFile(filepath.Join(dir, "file"), []byte{255}, 0600); err != nil {
			t.Fatal(err)
		}
	}
	marker := filepath.Join(bin, "invoked")
	t.Setenv("UPLOAD_MARKER", marker)
	script := `#!/usr/bin/python3
import json, os, sys
open(os.environ['UPLOAD_MARKER'], 'w').close()
assert sys.argv[1:] == ['call','create_or_update_file','--stdin','--require-base64-support']
assert json.load(sys.stdin) == {'content':'/w==','encoding':'base64'}
print('{}')
`
	if err := os.WriteFile(filepath.Join(bin, "mcp-server-cli"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.SharedWorkspace = shared
	cfg.Agents.Defaults.SharedWorkspaceReadOnly = true
	registry := createToolRegistry(workspace, true, cfg, bus.NewMessageBus(), ToolProfileDefault)
	for _, path := range []string{"file", filepath.Join(shared, "file"), filepath.Join(outside, "file")} {
		_ = os.Remove(marker)
		result := registry.Execute(context.Background(), "atomicqms_mcp", map[string]interface{}{"tool": "create_or_update_file", "arguments": map[string]interface{}{"local_path": path}})
		denied := path == filepath.Join(outside, "file")
		if result.IsError != denied {
			t.Fatalf("path %q: %#v", path, result)
		}
		if denied {
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("denied upload invoked CLI")
			}
		}
	}
}
