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
	write(filepath.Join(workspace, skills[1], "extra.txt"), "keep", 0644)
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
			filepath.Join(workspace, skills[1], "extra.txt"):               "keep",
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

func TestDockerEntrypointPreservesNonDirectorySkillEntries(t *testing.T) {
	for _, kind := range []string{"file", "dangling symlink", "directory symlink", "file symlink"} {
		t.Run(kind, func(t *testing.T) {
			tmp := t.TempDir()
			workspace := filepath.Join(tmp, "workspace")
			builtin := filepath.Join(tmp, "builtin")
			skill := "gitea-create-change-request"
			entry := filepath.Join(workspace, skill)
			target := filepath.Join(tmp, "target")
			if err := os.MkdirAll(workspace, 0755); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{skill, "gitea-move-work-item", "gitea-move-file-phase-0-to-1"} {
				if err := os.MkdirAll(filepath.Join(builtin, name), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(builtin, name, "SKILL.md"), []byte("built-in"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "file":
				if err := os.WriteFile(entry, []byte("custom"), 0644); err != nil {
					t.Fatal(err)
				}
			case "directory symlink":
				if err := os.Mkdir(target, 0755); err != nil {
					t.Fatal(err)
				}
			case "file symlink":
				if err := os.WriteFile(target, []byte("custom"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			if kind != "file" {
				if err := os.Symlink(target, entry); err != nil {
					t.Fatal(err)
				}
			}
			script, err := os.ReadFile("docker-entrypoint.sh")
			if err != nil {
				t.Fatal(err)
			}
			content := strings.ReplaceAll(string(script), "/root/sciclaw/skills", workspace)
			content = strings.ReplaceAll(content, "/opt/sciclaw/skills", builtin)
			content = strings.Replace(content, "exec sciclaw \"$@\"", "exit 0", 1)
			path := filepath.Join(tmp, "entrypoint.sh")
			if err := os.WriteFile(path, []byte(content), 0755); err != nil {
				t.Fatal(err)
			}
			output, err := exec.Command("sh", path).CombinedOutput()
			if err != nil {
				t.Errorf("startup failed: %v: %s", err, output)
			}
			if !strings.Contains(string(output), "Warning:") || !strings.Contains(string(output), skill) {
				t.Errorf("missing warning: %s", output)
			}
			if kind == "file" {
				got, err := os.ReadFile(entry)
				if err != nil || string(got) != "custom" {
					t.Errorf("entry changed: %q, %v", got, err)
				}
			} else {
				got, err := os.Readlink(entry)
				if err != nil || got != target {
					t.Errorf("symlink changed: %q, %v", got, err)
				}
			}
			if kind == "directory symlink" {
				entries, err := os.ReadDir(target)
				if err != nil || len(entries) != 0 {
					t.Errorf("symlink target modified: %v, %v", entries, err)
				}
			}
			if kind == "file symlink" {
				got, err := os.ReadFile(target)
				if err != nil || string(got) != "custom" {
					t.Errorf("symlink target changed: %q, %v", got, err)
				}
			}
			if got, err := os.ReadFile(filepath.Join(workspace, "gitea-move-work-item", "SKILL.md")); err != nil || string(got) != "built-in" {
				t.Errorf("other skill not seeded: %q, %v", got, err)
			}
		})
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
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed copy left partial files: %v, %v", entries, err)
	}
}
