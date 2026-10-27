package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/paths"
)

func TestWorkspaceVenvPaths(t *testing.T) {
	workspace := "/tmp/sciclaw-workspace"
	stateDir := paths.WorkspaceStateDir(workspace)
	if got := workspaceVenvDir(workspace); got != filepath.Join(stateDir, ".venv") {
		t.Fatalf("unexpected venv dir: %q", got)
	}
	if got := workspaceVenvBinDir(workspace); got != filepath.Join(stateDir, ".venv", "bin") {
		t.Fatalf("unexpected venv bin dir: %q", got)
	}
	if got := workspaceVenvPythonPath(workspace); got != filepath.Join(stateDir, ".venv", "bin", "python") {
		t.Fatalf("unexpected venv python path: %q", got)
	}
}

func TestEnsureUsableManagedVenvKeepsHealthyInterpreter(t *testing.T) {
	venvDir := filepath.Join(t.TempDir(), ".venv")
	pythonPath := filepath.Join(venvDir, "bin", "python")
	if err := os.MkdirAll(filepath.Dir(pythonPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pythonPath, []byte("python"), 0700); err != nil {
		t.Fatal(err)
	}
	recreated, err := ensureUsableManagedVenv(venvDir, pythonPath, func(_ time.Duration, name string, args ...string) (string, error) {
		if name != pythonPath || len(args) != 1 || args[0] != "--version" {
			t.Fatalf("unexpected validation command: %s %#v", name, args)
		}
		return "Python 3", nil
	}, func() error {
		t.Fatal("healthy venv must not be recreated")
		return nil
	})
	if err != nil || recreated {
		t.Fatalf("healthy venv result = %v, %v", recreated, err)
	}
}

func TestEnsureUsableManagedVenvRecreatesBrokenRelocatedInterpreter(t *testing.T) {
	venvDir := filepath.Join(t.TempDir(), ".venv")
	pythonPath := filepath.Join(venvDir, "bin", "python")
	markerPath := filepath.Join(venvDir, pythonBootstrapMarker)
	if err := os.MkdirAll(filepath.Dir(pythonPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pythonPath, []byte("broken"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(markerPath, []byte("stale"), 0600); err != nil {
		t.Fatal(err)
	}
	checks := 0
	recreated, err := ensureUsableManagedVenv(venvDir, pythonPath, func(_ time.Duration, _ string, _ ...string) (string, error) {
		checks++
		if checks == 1 {
			return "", errors.New("relocated interpreter is broken")
		}
		return "Python 3", nil
	}, func() error {
		if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
			t.Fatalf("stale managed venv was not removed before recreation: %v", err)
		}
		if err := os.MkdirAll(filepath.Dir(pythonPath), 0700); err != nil {
			return err
		}
		return os.WriteFile(pythonPath, []byte("healthy"), 0700)
	})
	if err != nil || !recreated {
		t.Fatalf("broken venv result = %v, %v", recreated, err)
	}
	if checks != 2 {
		t.Fatalf("expected validation before and after recreation, got %d checks", checks)
	}
}

func TestPythonSetupHint(t *testing.T) {
	msg := pythonSetupHint("No module named venv")
	if msg == "" {
		t.Fatalf("expected apt hint for missing venv module")
	}
	if msg2 := pythonSetupHint("some unrelated error"); msg2 != "" {
		t.Fatalf("unexpected hint for unrelated error: %q", msg2)
	}
}
