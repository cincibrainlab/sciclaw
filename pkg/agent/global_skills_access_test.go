package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/paths"
)

func TestGlobalSkillAccessWithRestrictedWorkspaces(t *testing.T) {
	for _, customRoot := range []bool{false, true} {
		t.Run(map[bool]string{false: "default-root", true: "custom-root"}[customRoot], func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("SCICLAW_HOME", "")
			t.Setenv("SCICLAW_STATE_HOME", "")
			if customRoot {
				t.Setenv("SCICLAW_HOME", filepath.Join(home, "custom"))
			}
			skillDir := filepath.Join(paths.GlobalSkillsDir(), "example")
			if err := os.MkdirAll(skillDir, 0755); err != nil {
				t.Fatal(err)
			}
			skillPath := filepath.Join(skillDir, "SKILL.md")
			if err := os.WriteFile(skillPath, []byte("---\nname: example\ndescription: test\n---\ninstructions"), 0644); err != nil {
				t.Fatal(err)
			}
			supportPath := filepath.Join(skillDir, "support.txt")
			if err := os.WriteFile(supportPath, []byte("support"), 0644); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(paths.UserHome(), "unrelated")
			if err := os.WriteFile(outside, []byte("private"), 0644); err != nil {
				t.Fatal(err)
			}
			escape := filepath.Join(skillDir, "escape")
			if err := os.Symlink(outside, escape); err != nil {
				t.Fatal(err)
			}
			cfg := config.DefaultConfig()
			for _, workspace := range []string{cfg.WorkspacePath(), filepath.Join(home, "routed")} {
				if err := os.MkdirAll(workspace, 0755); err != nil {
					t.Fatal(err)
				}
				prompt := NewContextBuilder(workspace, cfg.SharedWorkspacePath()).BuildSystemPrompt()
				if !strings.Contains(prompt, "<location>"+skillPath+"</location>") {
					t.Fatalf("global skill path missing from prompt: %s", prompt)
				}
				registry := createToolRegistry(workspace, true, cfg, bus.NewMessageBus(), ToolProfileDefault)
				for _, path := range []string{skillPath, supportPath} {
					if result := registry.Execute(context.Background(), "read_file", map[string]interface{}{"path": path}); result.IsError {
						t.Fatalf("read %s: %s", path, result.ForLLM)
					}
				}
				for _, path := range []string{outside, escape} {
					if result := registry.Execute(context.Background(), "read_file", map[string]interface{}{"path": path}); !result.IsError {
						t.Fatalf("unrelated path was readable: %s", path)
					}
				}
				if result := registry.Execute(context.Background(), "write_file", map[string]interface{}{"path": skillPath, "content": "changed"}); !result.IsError {
					t.Fatal("global skill was writable")
				}
			}
		})
	}
}
