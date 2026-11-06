package skills

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedGiteaSkills(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	global := filepath.Join(root, "global")
	builtin := filepath.Join(root, "builtin")
	managed := filepath.Join(root, "managed")
	write := func(dir, name, text string) {
		t.Helper()
		path := filepath.Join(dir, name, "SKILL.md")
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{filepath.Join(workspace, "skills"), global, builtin} {
		for _, name := range append(append([]string{}, managedGiteaNames...), "move-task-phase-0-to-1", "unrelated") {
			write(dir, name, "workspace content")
		}
	}
	for _, name := range managedGiteaNames {
		write(managed, name, "safe v1")
	}
	t.Setenv("SCICLAW_MANAGED_GITEA_SKILLS_DIR", managed)
	loader := NewSkillsLoader(workspace, global, builtin)
	check := func(name, want string, exists bool) {
		t.Helper()
		got, ok := loader.LoadSkill(name)
		if ok != exists || got != want {
			t.Errorf("LoadSkill(%q) = %q, %v; want %q, %v", name, got, ok, want, exists)
		}
	}
	for _, name := range managedGiteaNames {
		check(name, "safe v1", true)
	}
	check("move-task-phase-0-to-1", "", false)
	check("unrelated", "workspace content", true)
	const example = "Example: {\"owner\":\"<owner>\",\"text\":\"A & B\"}"
	write(managed, managedGiteaNames[0], example)
	check(managedGiteaNames[0], example, true)
	summary := loader.BuildSkillsSummary()
	if !strings.Contains(summary, example) || strings.Contains(summary, managed) {
		t.Errorf("managed content not inlined safely: %s", summary)
	}
	catalog, inline, found := strings.Cut(summary, "</skills>")
	if !found || !strings.Contains(inline, example) {
		t.Errorf("managed content should follow the XML catalog: %s", summary)
	}
	var document struct {
		XMLName xml.Name `xml:"skills"`
	}
	if err := xml.Unmarshal([]byte(catalog+"</skills>"), &document); err != nil {
		t.Errorf("invalid skills catalog: %v", err)
	}
	if err := os.Remove(filepath.Join(managed, managedGiteaNames[1], "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	check(managedGiteaNames[1], "", false)
	seen := make(map[string]SkillInfo)
	for _, skill := range loader.ListSkills() {
		if _, ok := seen[skill.Name]; ok {
			t.Errorf("duplicate skill %q", skill.Name)
		}
		seen[skill.Name] = skill
	}
	if seen[managedGiteaNames[0]].Source != "managed" || seen["unrelated"].Source != "workspace" {
		t.Errorf("unexpected list: %#v", seen)
	}
	if _, ok := seen[managedGiteaNames[1]]; ok {
		t.Error("missing managed skill fell back")
	}
	if _, ok := seen["move-task-phase-0-to-1"]; ok {
		t.Error("legacy skill listed")
	}
	t.Setenv("SCICLAW_MANAGED_GITEA_SKILLS_DIR", "")
	unmanaged := NewSkillsLoader(workspace, global, builtin)
	if got, ok := unmanaged.LoadSkill("move-task-phase-0-to-1"); !ok || got != "workspace content" {
		t.Errorf("unmanaged legacy changed: %q %v", got, ok)
	}
	if got, err := os.ReadFile(filepath.Join(workspace, "skills", managedGiteaNames[0], "SKILL.md")); err != nil || string(got) != "workspace content" {
		t.Errorf("workspace modified: %q %v", got, err)
	}
}
