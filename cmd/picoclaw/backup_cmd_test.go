package main

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/paths"
)

func TestParseBackupOptions(t *testing.T) {
	opts, showHelp, err := parseBackupOptions([]string{"--with-sessions", "-o", "~/x.tar.gz"})
	if err != nil {
		t.Fatalf("parseBackupOptions returned error: %v", err)
	}
	if showHelp {
		t.Fatalf("expected showHelp=false")
	}
	if !opts.WithSessions {
		t.Fatalf("expected WithSessions=true")
	}
	if opts.OutputPath != "~/x.tar.gz" {
		t.Fatalf("unexpected OutputPath: %q", opts.OutputPath)
	}
}

func TestParseBackupOptionsHelp(t *testing.T) {
	_, showHelp, err := parseBackupOptions([]string{"--help"})
	if err != nil {
		t.Fatalf("parseBackupOptions returned error: %v", err)
	}
	if !showHelp {
		t.Fatalf("expected showHelp=true for --help")
	}
}

func TestCollectBackupEntries(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("SCICLAW_HOME", filepath.Join(homeDir, "sciclaw"))
	t.Setenv("SCICLAW_STATE_HOME", filepath.Join(homeDir, ".sciclaw"))
	workspace := filepath.Join(homeDir, "external-workspace")
	managedDefault := paths.DefaultWorkspace()
	managedSecond := filepath.Join(paths.UserHome(), "workspaces", "second")
	if err := os.MkdirAll(workspace, 0755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}

	mustWriteFile(t, paths.ConfigPath(), "{}")
	mustWriteFile(t, paths.AuthPath(), "{}")
	mustWriteFile(t, filepath.Join(homeDir, ".config", "sciclaw", "op.env"), "OP_SERVICE_ACCOUNT_TOKEN=x")
	mustWriteFile(t, filepath.Join(workspace, "AGENTS.md"), "# AGENTS")
	mustWriteFile(t, filepath.Join(workspace, "HOOKS.md"), "# HOOKS")
	mustWriteFile(t, filepath.Join(workspace, "IDENTITY.md"), "# IDENTITY")
	mustWriteFile(t, filepath.Join(workspace, "SOUL.md"), "# SOUL")
	mustWriteFile(t, filepath.Join(workspace, "TOOLS.md"), "# TOOLS")
	mustWriteFile(t, filepath.Join(workspace, "USER.md"), "# USER")
	if err := os.MkdirAll(filepath.Join(workspace, "memory"), 0755); err != nil {
		t.Fatalf("mkdir memory: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, "skills"), 0755); err != nil {
		t.Fatalf("mkdir skills: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, "cron"), 0755); err != nil {
		t.Fatalf("mkdir cron: %v", err)
	}
	mustWriteFile(t, filepath.Join(workspace, "results", "arbitrary.txt"), "result")
	mustWriteFile(t, filepath.Join(managedDefault, "memory", "MEMORY.md"), "default")
	mustWriteFile(t, filepath.Join(managedSecond, "outputs", "second.txt"), "second")
	for _, current := range []string{workspace, managedDefault, managedSecond} {
		if err := os.MkdirAll(filepath.Join(paths.WorkspaceStateDir(current), "sessions"), 0700); err != nil {
			t.Fatalf("mkdir sessions: %v", err)
		}
	}

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace: workspace,
			},
		},
	}

	entriesNoSessions := collectBackupEntries(cfg, homeDir, false)
	if !hasArchivePath(entriesNoSessions, "user/workspaces") {
		t.Fatalf("expected all managed workspaces in backup entries")
	}
	externalArchiveRoot := filepath.ToSlash(filepath.Join("external-workspaces", paths.WorkspaceID(workspace)))
	if !hasArchivePath(entriesNoSessions, externalArchiveRoot) {
		t.Fatalf("expected configured external workspace in backup entries")
	}
	if hasArchivePath(entriesNoSessions, filepath.ToSlash(filepath.Join("state", "workspaces", paths.WorkspaceID(workspace), "sessions"))) {
		t.Fatalf("did not expect managed sessions without --with-sessions")
	}

	entriesWithSessions := collectBackupEntries(cfg, homeDir, true)
	for _, current := range []string{workspace, managedDefault, managedSecond} {
		sessionArchive := filepath.ToSlash(filepath.Join("state", "workspaces", paths.WorkspaceID(current), "sessions"))
		if !hasArchivePath(entriesWithSessions, sessionArchive) {
			t.Fatalf("expected sessions for %s", current)
		}
	}

	archivePath := filepath.Join(t.TempDir(), "backup.tar.gz")
	if err := createBackupArchive(archivePath, entriesNoSessions); err != nil {
		t.Fatalf("create backup: %v", err)
	}
	if !archiveContains(t, archivePath, externalArchiveRoot+"/results/arbitrary.txt") {
		t.Fatal("backup omitted arbitrary workspace output")
	}
	if !archiveContains(t, archivePath, "user/workspaces/second/outputs/second.txt") {
		t.Fatal("backup omitted secondary managed workspace")
	}

	insideOutput := filepath.Join(workspace, "backup.tar.gz")
	if err := createBackupArchive(insideOutput, entriesNoSessions); err != nil {
		t.Fatalf("create backup inside workspace: %v", err)
	}
	if archiveContains(t, insideOutput, externalArchiveRoot+"/backup.tar.gz") {
		t.Fatal("backup recursively included its own output")
	}
}

func TestBackupFollowsConfiguredWorkspaceRootSymlinkOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SCICLAW_HOME", filepath.Join(home, "sciclaw"))
	t.Setenv("SCICLAW_STATE_HOME", filepath.Join(home, ".sciclaw"))
	realWorkspace := filepath.Join(home, "real-workspace")
	workspaceLink := filepath.Join(home, "workspace-link")
	mustWriteFile(t, filepath.Join(realWorkspace, "outputs", "result.txt"), "result")
	outsideFile := filepath.Join(t.TempDir(), "outside.txt")
	mustWriteFile(t, outsideFile, "outside")
	if err := os.Symlink(outsideFile, filepath.Join(realWorkspace, "nested-link.txt")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := os.Symlink(realWorkspace, workspaceLink); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspaceLink
	cfg.Agents.Defaults.SharedWorkspace = workspaceLink
	entries := collectBackupEntries(cfg, home, false)
	archiveRoot := filepath.ToSlash(filepath.Join("external-workspaces", paths.WorkspaceID(workspaceLink)))
	archivePath := filepath.Join(t.TempDir(), "backup.tar.gz")
	if err := createBackupArchive(archivePath, entries); err != nil {
		t.Fatal(err)
	}
	if !archiveContains(t, archivePath, archiveRoot+"/outputs/result.txt") {
		t.Fatal("configured symlink workspace contents were not archived")
	}
	if archiveContains(t, archivePath, archiveRoot+"/nested-link.txt") {
		t.Fatal("nested workspace symlink must not be followed or archived")
	}
}

func archiveContains(t *testing.T, path, want string) bool {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			return false
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Name == want {
			return true
		}
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir parent for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func hasArchivePath(entries []backupEntry, archivePath string) bool {
	for _, entry := range entries {
		if entry.ArchivePath == archivePath {
			return true
		}
	}
	return false
}
