package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/paths"
)

func TestArchiveListsLegacyAndManagedSessions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SCICLAW_HOME", filepath.Join(home, "visible"))
	t.Setenv("SCICLAW_STATE_HOME", filepath.Join(home, "state"))
	workspace := filepath.Join(home, "workspace")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	if err := config.SaveConfig(paths.ConfigPath(), cfg); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(workspace, "sessions")
	managed := filepath.Join(paths.WorkspaceStateDir(workspace), "sessions")
	mustWriteFile(t, filepath.Join(legacy, "legacy.json"), `{"key":"discord:legacy","messages":[{"role":"user","content":"legacy"}]}`)
	mustWriteFile(t, filepath.Join(legacy, "both.json"), `{"key":"discord:both","messages":[{"role":"user","content":"old"}]}`)
	mustWriteFile(t, filepath.Join(managed, "both.json"), `{"key":"discord:both","messages":[{"role":"user","content":"new"},{"role":"user","content":"newer"}]}`)
	out := captureStdout(t, func() { archiveDiscordCmd([]string{"list"}) })
	if !strings.Contains(out, "discord:legacy | messages=1") || !strings.Contains(out, "discord:both | messages=2") {
		t.Fatalf("unexpected archive listing: %s", out)
	}
}

func TestParseArchiveRunOptions(t *testing.T) {
	opts, err := parseArchiveRunOptions([]string{"--session-key", "discord:1", "--dry-run"})
	if err != nil {
		t.Fatalf("parseArchiveRunOptions error: %v", err)
	}
	if opts.SessionKey != "discord:1" {
		t.Fatalf("unexpected session key: %q", opts.SessionKey)
	}
	if !opts.DryRun {
		t.Fatal("expected dry-run=true")
	}
}

func TestParseArchiveRunOptionsUnknown(t *testing.T) {
	if _, err := parseArchiveRunOptions([]string{"--nope"}); err == nil {
		t.Fatal("expected error for unknown option")
	}
}

func TestParseArchiveRecallOptions(t *testing.T) {
	opts, err := parseArchiveRecallOptions(
		[]string{"alpha", "token", "--top-k", "4", "--max-chars", "1200", "--session-key", "discord:1", "--json"},
		6,
		3000,
	)
	if err != nil {
		t.Fatalf("parseArchiveRecallOptions error: %v", err)
	}
	if opts.Query != "alpha token" {
		t.Fatalf("unexpected query: %q", opts.Query)
	}
	if opts.TopK != 4 {
		t.Fatalf("unexpected top-k: %d", opts.TopK)
	}
	if opts.MaxChars != 1200 {
		t.Fatalf("unexpected max chars: %d", opts.MaxChars)
	}
	if opts.SessionKey != "discord:1" {
		t.Fatalf("unexpected session key: %q", opts.SessionKey)
	}
	if !opts.JSON {
		t.Fatal("expected json=true")
	}
}

func TestParseArchiveRecallOptionsMissingQuery(t *testing.T) {
	if _, err := parseArchiveRecallOptions([]string{"--top-k", "2"}, 6, 3000); err == nil {
		t.Fatal("expected missing query error")
	}
}
