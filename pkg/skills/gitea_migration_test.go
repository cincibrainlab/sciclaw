package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLegacyGiteaSkillsUseSafeBundledInstructionsWithoutWritingWorkspace(t *testing.T) {
	workspace := t.TempDir()
	builtin, err := filepath.Abs("../../skills")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"gitea-create-change-request", "gitea-move-work-item", "gitea-move-file-phase-0-to-1"} {
		old, err := os.ReadFile(filepath.Join("testdata", "gitea-legacy", name+".md"))
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(workspace, "skills", name, "SKILL.md")
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, old, 0600); err != nil {
			t.Fatal(err)
		}
	}
	loader := NewSkillsLoader(workspace, "", builtin)
	summary := loader.BuildSkillsSummary()
	if strings.Count(summary, "Never use `exec`") != 3 || strings.Contains(summary, "mcp-server-cli call") {
		t.Fatalf("unsafe skill summary: %s", summary)
	}
	for _, name := range []string{"gitea-create-change-request", "gitea-move-work-item", "gitea-move-file-phase-0-to-1"} {
		content, ok := loader.LoadSkill(name)
		if !ok || !strings.Contains(content, "Never use `exec`") {
			t.Fatalf("unsafe loaded skill %s: %s", name, content)
		}
		got, err := os.ReadFile(filepath.Join(workspace, "skills", name, "SKILL.md"))
		if err != nil {
			t.Fatal(err)
		}
		old, _ := os.ReadFile(filepath.Join("testdata", "gitea-legacy", name+".md"))
		if string(got) != string(old) {
			t.Fatalf("workspace overwritten: %s", name)
		}
	}
}

func TestGiteaMigrationPreservesCustomizedAndSymlinkSkills(t *testing.T) {
	workspace := t.TempDir()
	builtin, _ := filepath.Abs("../../skills")
	name := "gitea-create-change-request"
	dir := filepath.Join(workspace, "skills", name)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "SKILL.md")
	old, err := os.ReadFile(filepath.Join("testdata", "gitea-legacy", name+".md"))
	if err != nil {
		t.Fatal(err)
	}
	custom := append(append([]byte{}, old...), []byte("\ncustom")...)
	if err := os.WriteFile(path, custom, 0600); err != nil {
		t.Fatal(err)
	}
	loader := NewSkillsLoader(workspace, "", builtin)
	if loader.safeLegacyGitea(name, path) != "" {
		t.Fatal("custom skill was substituted")
	}
	if !strings.Contains(loader.BuildSkillsSummary(), "requires user intervention") {
		t.Fatal("customized skill missing intervention warning")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(custom) {
		t.Fatalf("custom data changed: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, old, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if loader.safeLegacyGitea(name, path) != "" {
		t.Fatal("symlink skill was substituted")
	}
	got, err = os.ReadFile(target)
	if err != nil || string(got) != string(old) {
		t.Fatalf("symlink target changed: %v", err)
	}
}

func TestEarlierGiteaVersionsMigrateWithoutOverwritingCustomizations(t *testing.T) {
	builtin, err := filepath.Abs("../../skills")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, revision string }{
		{"gitea-create-change-request", "d7ce6b1"},
		{"gitea-move-file-phase-0-to-1", "d7ce6b1"},
		{"gitea-move-work-item", "d7ce6b1"},
		{"gitea-create-change-request", "2a644b6"},
		{"gitea-move-work-item", "2a644b6"},
		{"gitea-move-work-item", "c38e0f8"},
	} {
		t.Run(tc.name+"-"+tc.revision, func(t *testing.T) {
			workspace := t.TempDir()
			path := filepath.Join(workspace, "skills", tc.name, "SKILL.md")
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			old, err := os.ReadFile(filepath.Join("testdata", "gitea-legacy", tc.name+"-"+tc.revision+".md"))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, old, 0600); err != nil {
				t.Fatal(err)
			}
			loader := NewSkillsLoader(workspace, "", builtin)
			content, ok := loader.LoadSkill(tc.name)
			if !ok || !strings.Contains(content, "Never use `exec`") {
				t.Fatal("shipped legacy skill not migrated")
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != string(old) {
				t.Fatal("legacy file changed")
			}
			custom := append(append([]byte{}, old...), []byte("\ncustom")...)
			if err := os.WriteFile(path, custom, 0600); err != nil {
				t.Fatal(err)
			}
			if loader.safeLegacyGitea(tc.name, path) != "" {
				t.Fatal("customized skill migrated")
			}
			got, err = os.ReadFile(path)
			if err != nil || string(got) != string(custom) {
				t.Fatal("customized file changed")
			}
		})
	}
}
