package tui

import (
	"testing"
)

func TestVMExecutorUsesSplitSciClawRoots(t *testing.T) {
	executor := &VMExecutor{}
	if got, want := executor.ConfigPath(), "/home/ubuntu/.sciclaw/config.json"; got != want {
		t.Fatalf("ConfigPath()=%q want %q", got, want)
	}
	if got, want := executor.AuthPath(), "/home/ubuntu/.sciclaw/auth.json"; got != want {
		t.Fatalf("AuthPath()=%q want %q", got, want)
	}
}
