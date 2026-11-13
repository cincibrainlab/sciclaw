package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/paths"
)

func TestCheckToolsPolicyReportsStaleAndFixesIt(t *testing.T) {
	workspace := t.TempDir()
	t.Setenv("SCICLAW_HOME", filepath.Join(t.TempDir(), "sciclaw"))
	if err := os.MkdirAll(paths.ProfileDir(), 0755); err != nil {
		t.Fatal(err)
	}
	toolsPath := filepath.Join(paths.ProfileDir(), "TOOLS.md")
	initial := "# Tools\n\n## Critical CLI-First Rules\n\n- For PubMed literature tasks, use the installed `pubmed`/`pubmed-cli` directly.\n- Use `docx-review` only for tracked-change edits/comments/diff on existing documents.\n\n### PubMed Examples (Preferred)\n\n```bash\npubmed search \"query\" --json\n```\n"
	if err := os.WriteFile(toolsPath, []byte(initial), 0o644); err != nil {
		t.Fatalf("seed TOOLS.md: %v", err)
	}

	check := checkToolsPolicy(workspace, doctorOptions{})
	if check.Status != doctorWarn || !strings.Contains(check.Message, "stale") {
		t.Fatalf("checkToolsPolicy() = %#v, want stale warning", check)
	}

	check = checkToolsPolicy(workspace, doctorOptions{Fix: true})
	if check.Status != doctorOK || !strings.Contains(check.Message, "refreshed") {
		t.Fatalf("checkToolsPolicy(fix) = %#v, want refreshed ok", check)
	}

	after, err := os.ReadFile(toolsPath)
	if err != nil {
		t.Fatalf("read TOOLS.md: %v", err)
	}
	txt := string(after)
	if !strings.Contains(txt, "pubmed_search") || !strings.Contains(txt, "pubmed_fetch") {
		t.Fatalf("refreshed TOOLS.md missing typed PubMed guidance:\n%s", txt)
	}
	if !strings.Contains(txt, "xlsx_review_read") || !strings.Contains(txt, "pptx_review_read") {
		t.Fatalf("refreshed TOOLS.md missing xlsx/pptx guidance:\n%s", txt)
	}
}

func TestCheckToolsPolicyUsesProfileOnFreshLayout(t *testing.T) {
	workspace := t.TempDir()
	t.Setenv("SCICLAW_HOME", filepath.Join(t.TempDir(), "sciclaw"))
	if err := os.MkdirAll(paths.ProfileDir(), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paths.ProfileDir(), "TOOLS.md"), []byte("# Tools\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := ensureToolsCLIFirstPolicy(paths.ProfileDir()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "TOOLS.md")); !os.IsNotExist(err) {
		t.Fatal("test requires no obsolete workspace TOOLS.md")
	}
	if check := checkToolsPolicy(workspace, doctorOptions{}); check.Status != doctorOK {
		t.Fatalf("fresh profile should not warn: %#v", check)
	}
}
