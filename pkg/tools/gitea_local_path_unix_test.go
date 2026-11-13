//go:build darwin || linux

package tools

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestGiteaLocalPathFIFO(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, "fifo"), 0600); err != nil {
		t.Fatal(err)
	}
	marker := localPathCLI(t, "print('{}')\n")
	result := NewGiteaMCPTool(dir, true).Execute(context.Background(), map[string]interface{}{"tool": "create_or_update_file", "arguments": map[string]interface{}{"local_path": "fifo"}})
	if !result.IsError {
		t.Fatal("FIFO accepted")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("FIFO upload invoked CLI")
	}
}
