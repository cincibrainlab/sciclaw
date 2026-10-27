package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalExecutorWriteFileHonorsPermissions(t *testing.T) {
	t.Setenv("SCICLAW_HOME", filepath.Join(t.TempDir(), "sciclaw"))
	t.Setenv("SCICLAW_STATE_HOME", filepath.Join(t.TempDir(), ".sciclaw"))
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	executor := NewLocalExecutor()
	if err := executor.WriteFile(path, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode=%#o want 0600", info.Mode().Perm())
	}
}

func TestParseServiceStatusFlag(t *testing.T) {
	out := `
Gateway service status:
  Backend:   launchd
  Installed: yes
  Running:   yes
  Enabled:   yes
`
	if v, ok := parseServiceStatusFlag(out, "installed"); !ok || !v {
		t.Fatalf("installed parse = (%v,%v), want (true,true)", v, ok)
	}
	if v, ok := parseServiceStatusFlag(out, "running"); !ok || !v {
		t.Fatalf("running parse = (%v,%v), want (true,true)", v, ok)
	}
}

func TestParseServiceStatusFlag_No(t *testing.T) {
	out := `
Gateway service status:
  Installed: no
  Running:   no
`
	if v, ok := parseServiceStatusFlag(out, "installed"); !ok || v {
		t.Fatalf("installed parse = (%v,%v), want (false,true)", v, ok)
	}
	if v, ok := parseServiceStatusFlag(out, "running"); !ok || v {
		t.Fatalf("running parse = (%v,%v), want (false,true)", v, ok)
	}
}

func TestMergePathList_PrefersFrontAndDedupes(t *testing.T) {
	got := mergePathList(
		[]string{"/Users/tester/.local/bin", "/opt/homebrew/bin", "/usr/local/bin"},
		"/usr/local/bin:/usr/bin:/bin:/opt/homebrew/bin",
	)
	parts := strings.Split(got, ":")
	if len(parts) < 5 {
		t.Fatalf("merged path too short: %q", got)
	}
	if parts[0] != "/Users/tester/.local/bin" {
		t.Fatalf("first path = %q, want %q", parts[0], "/Users/tester/.local/bin")
	}
	if parts[1] != "/opt/homebrew/bin" {
		t.Fatalf("second path = %q, want %q", parts[1], "/opt/homebrew/bin")
	}
	if parts[2] != "/usr/local/bin" {
		t.Fatalf("third path = %q, want %q", parts[2], "/usr/local/bin")
	}
	if strings.Count(got, "/opt/homebrew/bin") != 1 {
		t.Fatalf("expected deduped /opt/homebrew/bin in %q", got)
	}
	if strings.Count(got, "/usr/local/bin") != 1 {
		t.Fatalf("expected deduped /usr/local/bin in %q", got)
	}
}

func TestStableBinaryPathCandidateForLinuxbrewCellar(t *testing.T) {
	got := stableBinaryPathCandidate("/home/linuxbrew/.linuxbrew/Cellar/sciclaw-dev/0.2.5-dev.4/bin/sciclaw")
	want := "/home/linuxbrew/.linuxbrew/bin/sciclaw"
	if got != want {
		t.Fatalf("stableBinaryPathCandidate() = %q, want %q", got, want)
	}
}

func TestStableBinaryPathCandidateForHomebrewCellar(t *testing.T) {
	got := stableBinaryPathCandidate("/opt/homebrew/Cellar/sciclaw/0.2.5/bin/sciclaw")
	want := "/opt/homebrew/bin/sciclaw"
	if got != want {
		t.Fatalf("stableBinaryPathCandidate() = %q, want %q", got, want)
	}
}

func TestStableBinaryPathCandidateForNonCellarPath(t *testing.T) {
	temp := filepath.Join(t.TempDir(), "bin", "sciclaw")
	if got := stableBinaryPathCandidate(temp); got != "" {
		t.Fatalf("stableBinaryPathCandidate() = %q, want empty", got)
	}
}
