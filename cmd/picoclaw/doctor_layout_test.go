package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/paths"
)

func TestCheckLayoutAllowsIndependentPicoClawInstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SCICLAW_HOME", filepath.Join(home, "sciclaw"))
	if err := os.MkdirAll(filepath.Join(home, ".picoclaw"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".picoclaw", "config.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if check := checkLayout(config.DefaultConfig()); check.Status != doctorOK {
		t.Fatalf("valid PicoClaw install should not warn: %#v", check)
	}
}

func TestCheckLayoutWarnsForActiveLegacyWorkspace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = "~/.picoclaw/workspace"
	if check := checkLayout(cfg); check.Status != doctorErr {
		t.Fatalf("legacy active workspace should warn: %#v", check)
	}
}

func TestCheckLayoutRejectsSciClawRootsUnderPicoClaw(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Run("visible root", func(t *testing.T) {
		t.Setenv("SCICLAW_HOME", filepath.Join(home, ".picoclaw", "sciclaw"))
		t.Setenv("SCICLAW_STATE_HOME", filepath.Join(home, ".sciclaw-state"))
		if check := checkLayout(nil); check.Status != doctorErr {
			t.Fatalf("visible PicoClaw root should warn: %#v", check)
		}
	})
	t.Run("state root", func(t *testing.T) {
		t.Setenv("SCICLAW_HOME", filepath.Join(home, "sciclaw"))
		t.Setenv("SCICLAW_STATE_HOME", filepath.Join(home, ".picoclaw", "state"))
		if check := checkLayout(nil); check.Status != doctorErr {
			t.Fatalf("state PicoClaw root should warn: %#v", check)
		}
	})
}

func TestLoadConfigRejectsActivePicoClawWorkspace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SCICLAW_HOME", filepath.Join(home, "sciclaw"))
	t.Setenv("SCICLAW_STATE_HOME", filepath.Join(home, ".sciclaw"))
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = "~/.picoclaw/workspace"
	if err := config.SaveConfig(paths.ConfigPath(), cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(); err == nil {
		t.Fatal("normal runtime accepted a PicoClaw-owned workspace")
	}
}
