package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRootDefaultsIgnorePicoClaw(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SCICLAW_HOME", "")
	t.Setenv("SCICLAW_STATE_HOME", "")

	legacy := filepath.Join(home, ".picoclaw")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "config.json"), []byte(`{"legacy":true}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if got, want := UserHome(), filepath.Join(home, "sciclaw"); got != want {
		t.Fatalf("UserHome()=%q want %q", got, want)
	}
	if got, want := StateHome(), filepath.Join(home, ".sciclaw"); got != want {
		t.Fatalf("StateHome()=%q want %q", got, want)
	}
	if got, want := ConfigPath(), filepath.Join(home, "sciclaw", "config.json"); got != want {
		t.Fatalf("ConfigPath()=%q want %q", got, want)
	}
}

func TestIndependentRootOverrides(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SCICLAW_HOME", "~/visible")
	t.Setenv("SCICLAW_STATE_HOME", "~/managed")

	if got, want := UserHome(), filepath.Join(home, "visible"); got != want {
		t.Fatalf("UserHome()=%q want %q", got, want)
	}
	if got, want := StateHome(), filepath.Join(home, "managed"); got != want {
		t.Fatalf("StateHome()=%q want %q", got, want)
	}
}

func TestVisibleAndStateHelpers(t *testing.T) {
	visible := filepath.Join(t.TempDir(), "visible")
	state := filepath.Join(t.TempDir(), "state")
	t.Setenv("SCICLAW_HOME", visible)
	t.Setenv("SCICLAW_STATE_HOME", state)

	tests := map[string]struct {
		got  string
		want string
	}{
		"config":            {ConfigPath(), filepath.Join(visible, "config.json")},
		"profile":           {ProfileDir(), filepath.Join(visible, "profile")},
		"global skills":     {GlobalSkillsDir(), filepath.Join(visible, "skills")},
		"templates":         {TemplatesDir(), filepath.Join(visible, "templates")},
		"backups":           {BackupsDir(), filepath.Join(visible, "backups")},
		"addons":            {AddonsDir(), filepath.Join(visible, "addons")},
		"default workspace": {DefaultWorkspace(), filepath.Join(visible, "workspaces", "default")},
		"auth":              {AuthPath(), filepath.Join(state, "auth.json")},
		"logs":              {LogsDir(), filepath.Join(state, "logs")},
		"runtime":           {RuntimeDir(), filepath.Join(state, "runtime")},
		"cache":             {CacheDir(), filepath.Join(state, "cache")},
		"temp":              {TempDir(), filepath.Join(state, "tmp")},
		"workspace states":  {WorkspacesStateDir(), filepath.Join(state, "workspaces")},
		"gateway log":       {GatewayLogPath(), filepath.Join(state, "logs", "gateway.log")},
		"gateway error log": {GatewayErrorLogPath(), filepath.Join(state, "logs", "gateway.err.log")},
		"web log":           {WebLogPath(), filepath.Join(state, "logs", "web.log")},
		"web error log":     {WebErrorLogPath(), filepath.Join(state, "logs", "web.err.log")},
		"gateway status":    {GatewayStatusPath(), filepath.Join(state, "runtime", "gateway.status.json")},
		"routing reload":    {RoutingReloadPath(), filepath.Join(state, "runtime", "routing.reload")},
		"jobs":              {JobsPath(), filepath.Join(state, "runtime", "jobs.json")},
		"history":           {InteractiveHistoryPath(), filepath.Join(state, "runtime", "history")},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if test.got != test.want {
				t.Fatalf("got %q want %q", test.got, test.want)
			}
		})
	}
}

func TestWorkspaceStateDirDoesNotResolveSymlinks(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("SCICLAW_STATE_HOME", stateHome)
	root := t.TempDir()
	firstTarget := filepath.Join(root, "first")
	secondTarget := filepath.Join(root, "second")
	if err := os.MkdirAll(firstTarget, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(secondTarget, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "workspace-link")
	if err := os.Symlink(firstTarget, link); err != nil {
		t.Fatal(err)
	}
	want := WorkspaceStateDir(link)
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secondTarget, link); err != nil {
		t.Fatal(err)
	}
	if got := WorkspaceStateDir(link); got != want {
		t.Fatalf("workspace identity changed after symlink retarget: %q != %q", got, want)
	}
}

func TestValidateHomesAndWorkspaceRejectPicoClawTree(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SCICLAW_HOME", filepath.Join(home, ".picoclaw", "sciclaw"))
	t.Setenv("SCICLAW_STATE_HOME", filepath.Join(home, ".sciclaw", "state"))
	if err := ValidateHomes(); err == nil {
		t.Fatal("expected PicoClaw-owned roots to be rejected")
	}
	if err := ValidateWorkspace(filepath.Join(home, ".picoclaw", "workspace")); err == nil {
		t.Fatal("expected PicoClaw-owned workspace to be rejected")
	}
	if IsPicoClawPath(filepath.Join(home, ".picoclaw-backup")) {
		t.Fatal("similarly named sibling must not be treated as PicoClaw-owned")
	}
}

func TestValidateHomesRejectsEqualAndNestedRoots(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, test := range []struct {
		name    string
		visible string
		state   string
	}{
		{name: "equal", visible: filepath.Join(home, "sciclaw"), state: filepath.Join(home, "sciclaw")},
		{name: "state under visible", visible: filepath.Join(home, "sciclaw"), state: filepath.Join(home, "sciclaw", "state")},
		{name: "visible under state", visible: filepath.Join(home, ".sciclaw", "user"), state: filepath.Join(home, ".sciclaw")},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("SCICLAW_HOME", test.visible)
			t.Setenv("SCICLAW_STATE_HOME", test.state)
			if err := ValidateHomes(); err == nil {
				t.Fatal("expected overlapping roots to be rejected")
			}
		})
	}
}

func TestValidationRejectsSymlinkedPicoClawBypass(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	legacy := filepath.Join(home, ".picoclaw")
	if err := os.MkdirAll(filepath.Join(legacy, "nested"), 0755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, "innocent-link")
	if err := os.Symlink(legacy, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SCICLAW_HOME", filepath.Join(link, "visible"))
	t.Setenv("SCICLAW_STATE_HOME", filepath.Join(home, ".sciclaw-state"))
	if err := ValidateHomes(); err == nil {
		t.Fatal("expected symlinked visible root into PicoClaw to be rejected")
	}
	if err := ValidateWorkspace(filepath.Join(link, "nested", "workspace")); err == nil {
		t.Fatal("expected symlinked workspace into PicoClaw to be rejected")
	}
}

func TestValidateWorkspaceRejectsStateHomeOverlap(t *testing.T) {
	home := t.TempDir()
	stateHome := filepath.Join(home, ".sciclaw")
	t.Setenv("HOME", home)
	t.Setenv("SCICLAW_HOME", filepath.Join(home, "sciclaw"))
	t.Setenv("SCICLAW_STATE_HOME", stateHome)
	for _, workspace := range []string{stateHome, filepath.Join(stateHome, "workspaces", "nested"), home} {
		if err := ValidateWorkspace(workspace); err == nil {
			t.Fatalf("expected state-overlapping workspace %q to be rejected", workspace)
		}
	}
	if err := ValidateWorkspace(filepath.Join(home, "visible-project")); err != nil {
		t.Fatalf("visible workspace should remain valid: %v", err)
	}
}

func TestValidateWorkspaceRejectsSymlinkIntoStateHome(t *testing.T) {
	home := t.TempDir()
	stateHome := filepath.Join(home, ".sciclaw")
	t.Setenv("HOME", home)
	t.Setenv("SCICLAW_HOME", filepath.Join(home, "sciclaw"))
	t.Setenv("SCICLAW_STATE_HOME", stateHome)
	if err := os.MkdirAll(filepath.Join(stateHome, "workspaces"), 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, "project-link")
	if err := os.Symlink(filepath.Join(stateHome, "workspaces"), link); err != nil {
		t.Fatal(err)
	}
	if err := ValidateWorkspace(filepath.Join(link, "project")); err == nil {
		t.Fatal("expected symlinked workspace into state home to be rejected")
	}
}

func TestWorkspaceStateDirStableAndCollisionResistant(t *testing.T) {
	t.Setenv("SCICLAW_STATE_HOME", t.TempDir())
	first := filepath.Join(t.TempDir(), "project")
	second := filepath.Join(t.TempDir(), "project")

	firstState := WorkspaceStateDir(first)
	if firstState != WorkspaceStateDir(filepath.Join(first, ".")) {
		t.Fatal("repeated clean resolution is not stable")
	}
	if firstState == WorkspaceStateDir(second) {
		t.Fatalf("same-basename workspaces collided at %s", firstState)
	}
	if err := os.MkdirAll(first, 0o755); err != nil {
		t.Fatal(err)
	}
	if firstState != WorkspaceStateDir(first) {
		t.Fatal("workspace state changed after workspace creation")
	}
}

func TestEnsureStateLayoutUsesPrivatePermissions(t *testing.T) {
	stateHome := filepath.Join(t.TempDir(), "state")
	t.Setenv("SCICLAW_STATE_HOME", stateHome)
	if err := os.MkdirAll(stateHome, 0755); err != nil {
		t.Fatal(err)
	}
	if err := EnsureStateLayout(); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{StateHome(), LogsDir(), RuntimeDir(), CacheDir(), TempDir(), WorkspacesStateDir()} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0700 {
			t.Fatalf("%s mode=%#o want 0700", dir, info.Mode().Perm())
		}
	}
	workspaceState, err := EnsureWorkspaceStateDir(filepath.Join(t.TempDir(), "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(workspaceState)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("workspace state mode must be 0700: %#v, %v", info, err)
	}
}
