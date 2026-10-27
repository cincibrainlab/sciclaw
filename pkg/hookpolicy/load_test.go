package hookpolicy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/hooks"
)

func TestLoadPolicy_MergePrecedenceYAMLOverridesNL(t *testing.T) {
	ws := t.TempDir()

	nl := `# Hooks

## before_turn
- capture reproducibility context
`
	if err := os.WriteFile(filepath.Join(ws, "HOOKS.md"), []byte(nl), 0644); err != nil {
		t.Fatalf("write HOOKS.md: %v", err)
	}
	yml := `enabled: true
events:
  before_turn:
    enabled: false
    verbosity: high
    instructions:
      - yaml override instruction
`
	if err := os.WriteFile(filepath.Join(ws, "hooks.yaml"), []byte(yml), 0644); err != nil {
		t.Fatalf("write hooks.yaml: %v", err)
	}

	policy, _, err := LoadPolicy(t.TempDir(), ws)
	if err != nil {
		t.Fatalf("LoadPolicy: %v", err)
	}

	ep := policy.Events[hooks.EventBeforeTurn]
	if ep.Enabled {
		t.Fatalf("expected yaml override enabled=false")
	}
	if ep.Verbosity != "high" {
		t.Fatalf("verbosity = %q, want high", ep.Verbosity)
	}
	if len(ep.Instructions) != 1 || ep.Instructions[0] != "yaml override instruction" {
		t.Fatalf("instructions not overridden: %#v", ep.Instructions)
	}
}

func TestLoadPolicy_DefaultsWithoutFiles(t *testing.T) {
	ws := t.TempDir()
	policy, _, err := LoadPolicy(t.TempDir(), ws)
	if err != nil {
		t.Fatalf("LoadPolicy: %v", err)
	}
	if !policy.Enabled {
		t.Fatalf("default policy should be enabled")
	}
	if !policy.Events[hooks.EventBeforeTurn].Enabled {
		t.Fatalf("before_turn should be enabled by default")
	}
}

func TestLoadPolicy_ProfileThenWorkspacePrecedence(t *testing.T) {
	profile := t.TempDir()
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(profile, "HOOKS.md"), []byte("## Before Tool\n- global instruction\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "HOOKS.md"), []byte("## Before Tool\n- workspace instruction\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "hooks.yaml"), []byte("events:\n  before_tool:\n    instructions:\n      - yaml override\n"), 0644); err != nil {
		t.Fatal(err)
	}

	policy, _, err := LoadPolicy(profile, workspace)
	if err != nil {
		t.Fatal(err)
	}
	instructions := policy.Events[hooks.EventBeforeTool].Instructions
	if len(instructions) != 1 || instructions[0] != "yaml override" {
		t.Fatalf("workspace YAML should override merged profile/workspace instructions: %#v", instructions)
	}
}
