//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOutputMarkerSymlinkSwap(t *testing.T) {
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outputs := filepath.Join(workspace, "outputs")
	other := filepath.Join(workspace, "other")
	for _, dir := range []string{outputs, other} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	marker := ".sciclaw-check-" + strings.Repeat("a", 32)
	if err := os.WriteFile(filepath.Join(other, marker), []byte(strings.Repeat("b", 64)), 0600); err != nil {
		t.Fatal(err)
	}
	parent, err := os.Open(workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	if err := os.Rename(outputs, filepath.Join(workspace, "original")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, outputs); err != nil {
		t.Fatal(err)
	}
	if f, err := openDirectoryAt(parent, "outputs"); err == nil {
		f.Close()
		t.Fatal("followed swapped outputs symlink")
	}
	if f, err := openOutputMarker(workspace, marker); err == nil {
		f.Close()
		t.Fatal("read marker through swapped outputs symlink")
	}
}
