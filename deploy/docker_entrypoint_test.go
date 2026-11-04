package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDockerEntrypointPreservesWorkspaceSkills(t *testing.T) {
	tmp := t.TempDir()
	workspace := filepath.Join(tmp, "workspace", "skills")
	builtin := filepath.Join(tmp, "builtin", "skills")
	bin := filepath.Join(tmp, "bin")
	write := func(path, content string, mode os.FileMode) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	skills := []string{"gitea-create-change-request", "gitea-move-work-item", "gitea-move-file-phase-0-to-1"}
	for _, skill := range skills {
		write(filepath.Join(builtin, skill, "SKILL.md"), "built-in", 0644)
	}
	write(filepath.Join(workspace, skills[0], "SKILL.md"), "custom", 0644)
	write(filepath.Join(workspace, skills[0], "extra.txt"), "extra", 0644)
	write(filepath.Join(workspace, "move-task-phase-0-to-1", "SKILL.md"), "legacy", 0644)
	write(filepath.Join(bin, "sciclaw"), "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$GATEWAY_ARGS\"\n", 0755)

	script, err := os.ReadFile("docker-entrypoint.sh")
	if err != nil {
		t.Fatal(err)
	}
	entrypoint := strings.ReplaceAll(string(script), "/root/sciclaw/skills", workspace)
	entrypoint = strings.ReplaceAll(entrypoint, "/opt/sciclaw/skills", builtin)
	entrypointPath := filepath.Join(tmp, "entrypoint.sh")
	write(entrypointPath, entrypoint, 0755)

	for i := 0; i < 2; i++ {
		cmd := exec.Command("sh", entrypointPath, "gateway", "--test")
		cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "GATEWAY_ARGS="+filepath.Join(tmp, "args"))
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("start %d: %v: %s", i+1, err, output)
		}
		for _, name := range []string{skills[0], "move-task-phase-0-to-1"} {
			if !strings.Contains(string(output), "Warning:") || !strings.Contains(string(output), name) {
				t.Errorf("start %d: missing warning for %s: %s", i+1, name, output)
			}
		}
		if i == 1 && !strings.Contains(string(output), skills[1]) {
			t.Errorf("start %d: missing warning for modified skill %s: %s", i+1, skills[1], output)
		}
		if strings.Contains(string(output), skills[2]) {
			t.Errorf("start %d: warned for unchanged skill %s: %s", i+1, skills[2], output)
		}
		seededWant := "built-in"
		if i == 1 {
			seededWant = "customized after start"
		}
		for path, want := range map[string]string{
			filepath.Join(workspace, skills[0], "SKILL.md"):                "custom",
			filepath.Join(workspace, skills[0], "extra.txt"):               "extra",
			filepath.Join(workspace, skills[1], "SKILL.md"):                seededWant,
			filepath.Join(workspace, skills[2], "SKILL.md"):                "built-in",
			filepath.Join(workspace, "move-task-phase-0-to-1", "SKILL.md"): "legacy",
			filepath.Join(tmp, "args"):                                     "gateway\n--test\n",
		} {
			got, err := os.ReadFile(path)
			if err != nil || string(got) != want {
				t.Errorf("start %d: %s: got %q, err %v; want %q", i+1, path, got, err, want)
			}
		}
		if i == 0 {
			write(filepath.Join(workspace, skills[1], "SKILL.md"), "customized after start", 0644)
		}
	}
}

func TestDockerEntrypointPreservesConcurrentSkillCreation(t *testing.T) {
	tmp := t.TempDir()
	workspace := filepath.Join(tmp, "workspace")
	builtin := filepath.Join(tmp, "builtin")
	bin := filepath.Join(tmp, "bin")
	for _, skill := range []string{"gitea-create-change-request", "gitea-move-work-item", "gitea-move-file-phase-0-to-1"} {
		if err := os.MkdirAll(filepath.Join(builtin, skill), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(builtin, skill, "SKILL.md"), []byte("built-in"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(bin, 0755); err != nil {
		t.Fatal(err)
	}
	ln, err := exec.LookPath("ln")
	if err != nil {
		t.Fatal(err)
	}
	wrapper := "#!/bin/sh\nprintf custom > \"$2\"\nexec " + ln + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "ln"), []byte(wrapper), 0755); err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile("docker-entrypoint.sh")
	if err != nil {
		t.Fatal(err)
	}
	entrypoint := strings.ReplaceAll(string(script), "/root/sciclaw/skills", workspace)
	entrypoint = strings.ReplaceAll(entrypoint, "/opt/sciclaw/skills", builtin)
	entrypoint = strings.Replace(entrypoint, "exec sciclaw \"$@\"", "exit 0", 1)
	path := filepath.Join(tmp, "entrypoint.sh")
	if err := os.WriteFile(path, []byte(entrypoint), 0755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", path)
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("start: %v: %s", err, output)
	}
	got, err := os.ReadFile(filepath.Join(workspace, "gitea-create-change-request", "SKILL.md"))
	if err != nil || string(got) != "custom" {
		t.Fatalf("concurrent edit: got %q, err %v", got, err)
	}
}

func TestDockerEntrypointFailsOnIncompleteSkillCopy(t *testing.T) {
	tmp := t.TempDir()
	workspace := filepath.Join(tmp, "workspace")
	bin := filepath.Join(tmp, "bin")
	if err := os.Mkdir(bin, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "cp"), []byte("#!/bin/sh\nprintf partial > \"$2\"\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile("docker-entrypoint.sh")
	if err != nil {
		t.Fatal(err)
	}
	entrypoint := strings.ReplaceAll(string(script), "/root/sciclaw/skills", workspace)
	entrypoint = strings.Replace(entrypoint, "exec sciclaw \"$@\"", "exit 0", 1)
	path := filepath.Join(tmp, "entrypoint.sh")
	if err := os.WriteFile(path, []byte(entrypoint), 0755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", path)
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if output, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("start succeeded after failed copy: %s", output)
	}
	dir := filepath.Join(workspace, "gitea-create-change-request")
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("failed copy left skill directory: %v", err)
	}
}
