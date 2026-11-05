package main

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/paths"
)

type backupOptions struct {
	OutputPath   string
	WithSessions bool
}

type backupEntry struct {
	SourcePath  string
	ArchivePath string
}

func backupCmd() {
	args := os.Args[2:]
	if len(args) == 0 {
		backupCreateCmd(nil)
		return
	}

	switch args[0] {
	case "create":
		backupCreateCmd(args[1:])
	case "list":
		backupListCmd(args[1:])
	case "help", "--help", "-h":
		backupHelp()
	default:
		fmt.Printf("Unknown backup command: %s\n", args[0])
		backupHelp()
	}
}

func backupHelp() {
	commandName := invokedCLIName()
	fmt.Println("\nBackup commands:")
	fmt.Println("  create                  Create a backup archive (default)")
	fmt.Println("  list                    Show files/directories that would be backed up")
	fmt.Println()
	fmt.Println("Create options:")
	fmt.Println("  -o, --output <path>     Output tar.gz path")
	fmt.Println("  --with-sessions         Include managed and legacy workspace sessions")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Printf("  %s backup create\n", commandName)
	fmt.Printf("  %s backup list\n", commandName)
	fmt.Printf("  %s backup create --with-sessions\n", commandName)
	fmt.Printf("  %s backup create --output ~/Desktop/sciclaw-backup.tar.gz\n", commandName)
	fmt.Printf("  (Compatibility alias also works: %s)\n", cliName)
}

func backupCreateCmd(args []string) {
	opts, showHelp, err := parseBackupOptions(args)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		backupHelp()
		return
	}
	if showHelp {
		backupHelp()
		return
	}

	cfg, err := loadConfig()
	if err != nil {
		fmt.Printf("Error loading config: %v\n", err)
		os.Exit(1)
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Printf("Error resolving home directory: %v\n", err)
		os.Exit(1)
	}

	entries := collectBackupEntries(cfg, homeDir, opts.WithSessions)
	if len(entries) == 0 {
		fmt.Println("No backup targets found. Run onboard first, then try again.")
		return
	}

	if opts.OutputPath == "" {
		opts.OutputPath = defaultBackupPath(homeDir)
	}
	opts.OutputPath = expandHomePath(opts.OutputPath, homeDir)

	if err := os.MkdirAll(filepath.Dir(opts.OutputPath), 0755); err != nil {
		fmt.Printf("Error creating backup directory: %v\n", err)
		os.Exit(1)
	}

	if err := createBackupArchive(opts.OutputPath, entries); err != nil {
		fmt.Printf("Error creating backup archive: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✓ Backup created: %s\n", opts.OutputPath)
	fmt.Printf("  Included %d path(s)\n", len(entries))
}

func backupListCmd(args []string) {
	opts, showHelp, err := parseBackupOptions(args)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		backupHelp()
		return
	}
	if showHelp {
		backupHelp()
		return
	}

	cfg, err := loadConfig()
	if err != nil {
		fmt.Printf("Error loading config: %v\n", err)
		os.Exit(1)
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Printf("Error resolving home directory: %v\n", err)
		os.Exit(1)
	}

	entries := collectBackupEntries(cfg, homeDir, opts.WithSessions)
	if len(entries) == 0 {
		fmt.Println("No backup targets found.")
		return
	}

	fmt.Println("\nBackup targets:")
	fmt.Println("---------------")
	for _, entry := range entries {
		fmt.Printf("  %s -> %s\n", entry.SourcePath, entry.ArchivePath)
	}
}

func parseBackupOptions(args []string) (backupOptions, bool, error) {
	opts := backupOptions{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--with-sessions":
			opts.WithSessions = true
		case "-o", "--output":
			if i+1 >= len(args) {
				return opts, false, fmt.Errorf("%s requires a value", args[i])
			}
			opts.OutputPath = args[i+1]
			i++
		case "help", "--help", "-h":
			return opts, true, nil
		default:
			return opts, false, fmt.Errorf("unknown option: %s", args[i])
		}
	}
	return opts, false, nil
}

func defaultBackupPath(homeDir string) string {
	timestamp := time.Now().UTC().Format("20060102-150405")
	return filepath.Join(paths.BackupsDir(), fmt.Sprintf("sciclaw-backup-%s.tar.gz", timestamp))
}

func expandHomePath(path string, homeDir string) string {
	if path == "~" {
		return homeDir
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(homeDir, path[2:])
	}
	return path
}

func collectBackupEntries(cfg *config.Config, homeDir string, withSessions bool) []backupEntry {
	candidates := []backupEntry{
		{
			SourcePath:  paths.ConfigPath(),
			ArchivePath: filepath.ToSlash(filepath.Join("state", "config.json")),
		},
		{
			SourcePath:  paths.ProfileDir(),
			ArchivePath: filepath.ToSlash(filepath.Join("user", "profile")),
		},
		{
			SourcePath:  paths.GlobalSkillsDir(),
			ArchivePath: filepath.ToSlash(filepath.Join("user", "skills")),
		},
		{
			SourcePath:  paths.TemplatesDir(),
			ArchivePath: filepath.ToSlash(filepath.Join("user", "templates")),
		},
		{
			SourcePath:  paths.AddonsDir(),
			ArchivePath: filepath.ToSlash(filepath.Join("user", "addons")),
		},
		{
			SourcePath:  paths.AuthPath(),
			ArchivePath: filepath.ToSlash(filepath.Join("state", "auth.json")),
		},
		{
			SourcePath:  filepath.Join(homeDir, ".config", "sciclaw", "op.env"),
			ArchivePath: filepath.ToSlash(filepath.Join("state", "op.env")),
		},
	}
	workspace := cfg.WorkspacePath()
	for _, name := range []string{"AGENTS.md", "HOOKS.md", "IDENTITY.md", "SOUL.md", "TOOLS.md", "USER.md", "memory", "skills", "cron"} {
		candidates = append(candidates, backupEntry{SourcePath: filepath.Join(workspace, name), ArchivePath: filepath.ToSlash(filepath.Join("workspace", name))})
	}
	if withSessions {
		legacy := filepath.Join(workspace, "sessions")
		if paths.ValidateWorkspace(workspace) == nil && !paths.IsPicoClawPath(legacy) {
			if resolved, err := filepath.EvalSymlinks(legacy); err == nil {
				legacy = resolved
			}
			candidates = append(candidates, backupEntry{SourcePath: legacy, ArchivePath: "workspace/sessions"})
		}
		managed := filepath.Join(paths.WorkspaceStateDir(workspace), "sessions")
		if resolved, err := filepath.EvalSymlinks(managed); err == nil {
			managed = resolved
		}
		candidates = append(candidates, backupEntry{
			SourcePath:  managed,
			ArchivePath: filepath.ToSlash(filepath.Join("state", "workspaces", paths.WorkspaceID(workspace), "sessions")),
		})
	}

	existing := make([]backupEntry, 0, len(candidates))
	seen := map[string]bool{}
	for _, entry := range candidates {
		cleaned := filepath.Clean(entry.SourcePath)
		key := cleaned + "\x00" + entry.ArchivePath
		if seen[key] {
			continue
		}
		seen[key] = true
		entry.SourcePath = cleaned
		if _, err := os.Stat(entry.SourcePath); err == nil {
			existing = append(existing, entry)
		}
	}
	return existing
}

func createBackupArchive(outputPath string, entries []backupEntry) error {
	file, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer file.Close()

	gzw := gzip.NewWriter(file)
	defer gzw.Close()

	tw := tar.NewWriter(gzw)
	defer tw.Close()

	for _, entry := range entries {
		info, err := os.Stat(entry.SourcePath)
		if err != nil {
			return err
		}
		if info.IsDir() {
			if err := addDirectoryToArchive(tw, entry.SourcePath, entry.ArchivePath); err != nil {
				return err
			}
			continue
		}
		if err := addFileToArchive(tw, entry.SourcePath, entry.ArchivePath); err != nil {
			return err
		}
	}

	return nil
}

func addDirectoryToArchive(tw *tar.Writer, sourceDir, archiveRoot string) error {
	return filepath.Walk(sourceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relPath, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return err
		}

		target := archiveRoot
		if relPath != "." {
			target = filepath.Join(archiveRoot, relPath)
		}
		target = filepath.ToSlash(target)

		if info.IsDir() {
			return addDirHeaderToArchive(tw, info, target)
		}
		if !info.Mode().IsRegular() {
			return nil
		}

		return addFileToArchive(tw, path, target)
	})
}

func addDirHeaderToArchive(tw *tar.Writer, info os.FileInfo, archivePath string) error {
	header, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	header.Name = strings.TrimSuffix(archivePath, "/") + "/"
	return tw.WriteHeader(header)
}

func addFileToArchive(tw *tar.Writer, sourcePath, archivePath string) error {
	info, err := os.Stat(sourcePath)
	if err != nil {
		return err
	}

	header, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	header.Name = archivePath

	if err := tw.WriteHeader(header); err != nil {
		return err
	}

	file, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = io.Copy(tw, file)
	return err
}
