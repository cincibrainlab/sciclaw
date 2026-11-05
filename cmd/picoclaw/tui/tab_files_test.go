package tui

import (
	"path/filepath"
	"testing"
)

func TestPackagedVMScriptPathsUseSciClawShareOnly(t *testing.T) {
	paths := packagedVMScriptPaths("/opt/homebrew/bin/sciclaw")
	want := filepath.Clean("/opt/homebrew/share/sciclaw/deploy/vm")
	if len(paths) != 1 || filepath.Clean(paths[0]) != want {
		t.Fatalf("unexpected packaged VM script paths: %#v", paths)
	}
}
