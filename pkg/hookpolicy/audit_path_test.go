package hookpolicy

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveAuditPathConfinesRelativePathsToHooks(t *testing.T) {
	state := t.TempDir()
	for _, test := range []struct {
		configured string
		want       string
	}{
		{configured: "", want: filepath.Join(state, "hooks", "hook-events.jsonl")},
		{configured: "custom.jsonl", want: filepath.Join(state, "hooks", "custom.jsonl")},
		{configured: "hooks/nested/events.jsonl", want: filepath.Join(state, "hooks", "nested", "events.jsonl")},
	} {
		got, err := ResolveAuditPath(state, test.configured)
		if err != nil {
			t.Fatalf("ResolveAuditPath(%q): %v", test.configured, err)
		}
		if got != test.want {
			t.Fatalf("ResolveAuditPath(%q)=%q want %q", test.configured, got, test.want)
		}
	}
}

func TestResolveAuditPathRejectsSymlinkedParent(t *testing.T) {
	state := t.TempDir()
	hooksDir := filepath.Join(state, "hooks")
	outside := t.TempDir()
	if err := os.MkdirAll(hooksDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(hooksDir, "escape")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := ResolveAuditPath(state, "hooks/escape/events.jsonl"); err == nil {
		t.Fatal("expected symlinked parent escape to be rejected")
	}
}

func TestResolveAuditPathRejectsSymlinkedDestination(t *testing.T) {
	state := t.TempDir()
	hooksDir := filepath.Join(state, "hooks")
	if err := os.MkdirAll(hooksDir, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "events.jsonl")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(hooksDir, "events.jsonl")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := ResolveAuditPath(state, "events.jsonl"); err == nil {
		t.Fatal("expected symlinked audit destination to be rejected")
	}
}

func TestResolveAuditPathRejectsTraversalAndAbsolutePaths(t *testing.T) {
	state := t.TempDir()
	for _, configured := range []string{"../config.json", "hooks/../../config.json", filepath.Join(t.TempDir(), "events.jsonl")} {
		if _, err := ResolveAuditPath(state, configured); err == nil {
			t.Fatalf("expected %q to be rejected", configured)
		}
	}
}
