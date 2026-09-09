package tui

import (
	"strings"
	"testing"
)

func TestVMExecutorUsesSplitSciClawRoots(t *testing.T) {
	executor := &VMExecutor{}
	if got, want := executor.ConfigPath(), "/home/ubuntu/sciclaw/config.json"; got != want {
		t.Fatalf("ConfigPath()=%q want %q", got, want)
	}
	if got, want := executor.AuthPath(), "/home/ubuntu/.sciclaw/auth.json"; got != want {
		t.Fatalf("AuthPath()=%q want %q", got, want)
	}
}

func TestVMWriteFileCommandHonorsPermissions(t *testing.T) {
	command := vmWriteFileCommand("/home/ubuntu/sciclaw/config.json", []byte("{}"), 0600)
	if !strings.Contains(command, "chmod 0600") || !strings.Contains(command, "umask 077") {
		t.Fatalf("write command does not enforce permissions: %s", command)
	}
}
