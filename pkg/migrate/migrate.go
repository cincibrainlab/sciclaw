package migrate

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/paths"
)

type ActionType int

const (
	ActionCopy ActionType = iota
	ActionSkip
	ActionBackup
	ActionConvertConfig
	ActionCreateDir
	ActionMergeConfig
)

type Options struct {
	DryRun        bool
	ConfigOnly    bool
	WorkspaceOnly bool
	Force         bool
	Refresh       bool
	OpenClawHome  string
	PicoClawHome  string
}

type Action struct {
	Type        ActionType
	Source      string
	Destination string
	Description string
}

type Result struct {
	FilesCopied    int
	FilesSkipped   int
	BackupsCreated int
	ConfigMigrated bool
	DirsCreated    int
	Warnings       []string
	Errors         []error
}

func Run(opts Options) (*Result, error) {
	if err := paths.ValidateHomes(); err != nil {
		return nil, fmt.Errorf("invalid SciClaw destination roots: %w", err)
	}
	if opts.ConfigOnly && opts.WorkspaceOnly {
		return nil, fmt.Errorf("--config-only and --workspace-only are mutually exclusive")
	}
	destinationOverride := opts.PicoClawHome
	if destinationOverride == "" {
		destinationOverride = os.Getenv("PICOCLAW_HOME")
	}
	if destinationOverride != "" {
		destination, err := filepath.Abs(expandHome(destinationOverride))
		if err != nil || filepath.Clean(destination) != filepath.Clean(paths.UserHome()) {
			return nil, fmt.Errorf("--picoclaw-home or PICOCLAW_HOME destination must match SCICLAW_HOME; configuration is stored separately in SCICLAW_STATE_HOME")
		}
	}

	if opts.Refresh {
		opts.WorkspaceOnly = true
	}

	openclawHome, err := resolveOpenClawHome(opts.OpenClawHome)
	if err != nil {
		return nil, err
	}
	sciClawHome := paths.UserHome()

	if _, err := os.Stat(openclawHome); os.IsNotExist(err) {
		return nil, fmt.Errorf("OpenClaw installation not found at %s", openclawHome)
	}

	actions, warnings, err := Plan(opts, openclawHome, sciClawHome)
	if err != nil {
		return nil, err
	}

	fmt.Println("Migrating from OpenClaw to sciClaw")
	fmt.Printf("  Source:      %s\n", openclawHome)
	fmt.Printf("  Destination: %s\n", sciClawHome)
	fmt.Println()

	if opts.DryRun {
		PrintPlan(actions, warnings)
		return &Result{Warnings: warnings}, nil
	}

	if !opts.Force {
		PrintPlan(actions, warnings)
		if !Confirm() {
			fmt.Println("Aborted.")
			return &Result{Warnings: warnings}, nil
		}
		fmt.Println()
	}

	result := Execute(actions, openclawHome, sciClawHome)
	result.Warnings = warnings
	if err := ResultError(result); err != nil {
		return result, err
	}
	return result, nil
}

func ResultError(result *Result) error {
	if result == nil || len(result.Errors) == 0 {
		return nil
	}
	return fmt.Errorf("migration failed with %d error(s): %v", len(result.Errors), result.Errors[0])
}

func Plan(opts Options, openclawHome, sciClawHome string) ([]Action, []string, error) {
	var actions []Action
	var warnings []string
	var configAction *Action

	if !opts.WorkspaceOnly {
		configPath, err := findOpenClawConfig(openclawHome)
		if err != nil {
			if opts.ConfigOnly {
				return nil, nil, err
			}
			warnings = append(warnings, fmt.Sprintf("Config migration skipped: %v", err))
		} else {
			destination := paths.ConfigPath()
			if _, statErr := os.Stat(destination); statErr == nil {
				return nil, nil, fmt.Errorf("active SciClaw config already exists at %s; OpenClaw import will not overwrite it", destination)
			} else if !os.IsNotExist(statErr) {
				return nil, nil, statErr
			}
			configAction = &Action{
				Type:        ActionConvertConfig,
				Source:      configPath,
				Destination: destination,
				Description: "convert OpenClaw config to sciClaw format",
			}

			data, err := LoadOpenClawConfig(configPath)
			if err == nil {
				_, configWarnings, _ := ConvertConfig(data)
				warnings = append(warnings, configWarnings...)
			}
		}
	}

	if !opts.ConfigOnly {
		srcWorkspace := resolveWorkspace(openclawHome)
		dstWorkspace := filepath.Join(sciClawHome, "workspaces", "default")

		if _, err := os.Stat(srcWorkspace); err == nil {
			wsActions, err := PlanWorkspaceMigration(srcWorkspace, dstWorkspace, filepath.Join(sciClawHome, "profile"), opts.Force || opts.Refresh)
			if err != nil {
				return nil, nil, fmt.Errorf("planning workspace migration: %w", err)
			}
			actions = append(actions, wsActions...)
			legacySessions := filepath.Join(srcWorkspace, "sessions")
			if _, statErr := os.Stat(legacySessions); statErr == nil {
				sessionActions, sessionErr := planDirCopy(
					legacySessions,
					filepath.Join(paths.WorkspaceStateDir(dstWorkspace), "sessions"),
					opts.Force || opts.Refresh,
				)
				if sessionErr != nil {
					return nil, nil, fmt.Errorf("planning session migration: %w", sessionErr)
				}
				actions = append(actions, sessionActions...)
			}
		} else {
			warnings = append(warnings, "OpenClaw workspace directory not found, skipping workspace migration")
		}
	}

	if configAction != nil {
		actions = append(actions, *configAction)
	}
	return actions, warnings, nil
}

func Execute(actions []Action, openclawHome, _ string) *Result {
	result := &Result{}
	for _, action := range actions {
		if action.Type == ActionSkip {
			continue
		}
		if migrationTargetsPicoClaw(action.Destination) || (action.Type == ActionBackup && migrationTargetsPicoClaw(action.Destination+".bak")) {
			result.Errors = append(result.Errors, fmt.Errorf("migration destination points into ~/.picoclaw: %s", action.Destination))
			return result
		}
	}

	for _, action := range actions {
		switch action.Type {
		case ActionConvertConfig:
			if len(result.Errors) != 0 {
				continue
			}
			if err := executeConfigMigration(action.Source, action.Destination); err != nil {
				result.Errors = append(result.Errors, fmt.Errorf("config migration: %w", err))
				fmt.Printf("  ✗ Config migration failed: %v\n", err)
			} else {
				result.ConfigMigrated = true
				fmt.Printf("  ✓ Converted config: %s\n", action.Destination)
			}
		case ActionCreateDir:
			if err := os.MkdirAll(action.Destination, 0755); err != nil {
				result.Errors = append(result.Errors, err)
			} else {
				result.DirsCreated++
			}
		case ActionBackup:
			bakPath := action.Destination + ".bak"
			if err := copyFile(action.Destination, bakPath); err != nil {
				result.Errors = append(result.Errors, fmt.Errorf("backup %s: %w", action.Destination, err))
				fmt.Printf("  ✗ Backup failed: %s\n", action.Destination)
				continue
			}
			result.BackupsCreated++
			fmt.Printf("  ✓ Backed up %s -> %s.bak\n", filepath.Base(action.Destination), filepath.Base(action.Destination))

			if err := os.MkdirAll(filepath.Dir(action.Destination), 0755); err != nil {
				result.Errors = append(result.Errors, err)
				continue
			}
			if err := copyFile(action.Source, action.Destination); err != nil {
				result.Errors = append(result.Errors, fmt.Errorf("copy %s: %w", action.Source, err))
				fmt.Printf("  ✗ Copy failed: %s\n", action.Source)
			} else {
				result.FilesCopied++
				fmt.Printf("  ✓ Copied %s\n", relPath(action.Source, openclawHome))
			}
		case ActionCopy:
			if err := os.MkdirAll(filepath.Dir(action.Destination), 0755); err != nil {
				result.Errors = append(result.Errors, err)
				continue
			}
			if err := copyFile(action.Source, action.Destination); err != nil {
				result.Errors = append(result.Errors, fmt.Errorf("copy %s: %w", action.Source, err))
				fmt.Printf("  ✗ Copy failed: %s\n", action.Source)
			} else {
				result.FilesCopied++
				fmt.Printf("  ✓ Copied %s\n", relPath(action.Source, openclawHome))
			}
		case ActionSkip:
			result.FilesSkipped++
		}
	}

	return result
}

func migrationTargetsPicoClaw(path string) bool {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return true
	}
	for links := 0; links <= 40; links++ {
		if paths.IsPicoClawPath(absolute) {
			return true
		}
		volume := filepath.VolumeName(absolute)
		current := volume + string(filepath.Separator)
		parts := strings.Split(strings.TrimPrefix(absolute, current), string(filepath.Separator))
		followed := false
		for i, part := range parts {
			current = filepath.Join(current, part)
			info, err := os.Lstat(current)
			if os.IsNotExist(err) {
				return false
			}
			if err != nil {
				return true
			}
			if info.Mode()&os.ModeSymlink == 0 {
				continue
			}
			target, err := os.Readlink(current)
			if err != nil {
				return true
			}
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(current), target)
			}
			absolute = filepath.Join(append([]string{target}, parts[i+1:]...)...)
			followed = true
			break
		}
		if !followed {
			return false
		}
	}
	return true
}

func executeConfigMigration(srcConfigPath, dstConfigPath string) error {
	data, err := LoadOpenClawConfig(srcConfigPath)
	if err != nil {
		return err
	}

	incoming, _, err := ConvertConfig(data)
	if err != nil {
		return err
	}
	if agents, ok := getMap(data, "agents"); ok {
		if defaults, ok := getMap(agents, "defaults"); ok {
			if workspace, ok := getString(defaults, "workspace"); ok && filepath.Clean(expandHome(workspace)) == filepath.Join(filepath.Dir(srcConfigPath), "workspace") {
				incoming.Agents.Defaults.Workspace = paths.DefaultWorkspace()
			}
		}
	}

	if _, err := os.Stat(dstConfigPath); err == nil {
		return fmt.Errorf("active SciClaw config already exists at %s", dstConfigPath)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := paths.ValidateWorkspace(incoming.WorkspacePath()); err != nil {
		return fmt.Errorf("invalid migrated workspace: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(dstConfigPath), 0755); err != nil {
		return err
	}
	return config.SaveConfig(dstConfigPath, incoming)
}

func Confirm() bool {
	fmt.Print("Proceed with migration? (y/n): ")
	var response string
	fmt.Scanln(&response)
	return strings.ToLower(strings.TrimSpace(response)) == "y"
}

func PrintPlan(actions []Action, warnings []string) {
	fmt.Println("Planned actions:")
	copies := 0
	skips := 0
	backups := 0
	configCount := 0

	for _, action := range actions {
		switch action.Type {
		case ActionConvertConfig:
			fmt.Printf("  [config]  %s -> %s\n", action.Source, action.Destination)
			configCount++
		case ActionCopy:
			fmt.Printf("  [copy]    %s\n", filepath.Base(action.Source))
			copies++
		case ActionBackup:
			fmt.Printf("  [backup]  %s (exists, will backup and overwrite)\n", filepath.Base(action.Destination))
			backups++
			copies++
		case ActionSkip:
			if action.Description != "" {
				fmt.Printf("  [skip]    %s (%s)\n", filepath.Base(action.Source), action.Description)
			}
			skips++
		case ActionCreateDir:
			fmt.Printf("  [mkdir]   %s\n", action.Destination)
		}
	}

	if len(warnings) > 0 {
		fmt.Println()
		fmt.Println("Warnings:")
		for _, w := range warnings {
			fmt.Printf("  - %s\n", w)
		}
	}

	fmt.Println()
	fmt.Printf("%d files to copy, %d configs to convert, %d backups needed, %d skipped\n",
		copies, configCount, backups, skips)
}

func PrintSummary(result *Result) {
	fmt.Println()
	parts := []string{}
	if result.FilesCopied > 0 {
		parts = append(parts, fmt.Sprintf("%d files copied", result.FilesCopied))
	}
	if result.ConfigMigrated {
		parts = append(parts, "1 config converted")
	}
	if result.BackupsCreated > 0 {
		parts = append(parts, fmt.Sprintf("%d backups created", result.BackupsCreated))
	}
	if result.FilesSkipped > 0 {
		parts = append(parts, fmt.Sprintf("%d files skipped", result.FilesSkipped))
	}

	if len(parts) > 0 {
		fmt.Printf("Migration complete! %s.\n", strings.Join(parts, ", "))
	} else {
		fmt.Println("Migration complete! No actions taken.")
	}

	if len(result.Errors) > 0 {
		fmt.Println()
		fmt.Printf("%d errors occurred:\n", len(result.Errors))
		for _, e := range result.Errors {
			fmt.Printf("  - %v\n", e)
		}
	}
}

func resolveOpenClawHome(override string) (string, error) {
	if override != "" {
		return expandHome(override), nil
	}
	if envHome := os.Getenv("OPENCLAW_HOME"); envHome != "" {
		return expandHome(envHome), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return filepath.Join(home, ".openclaw"), nil
}

func resolveWorkspace(homeDir string) string {
	return filepath.Join(homeDir, "workspace")
}

func expandHome(path string) string {
	if path == "" {
		return path
	}
	if path[0] == '~' {
		home, _ := os.UserHomeDir()
		if len(path) > 1 && path[1] == '/' {
			return home + path[1:]
		}
		return home
	}
	return path
}

func backupFile(path string) error {
	bakPath := path + ".bak"
	return copyFile(path, bakPath)
}

func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	info, err := srcFile.Stat()
	if err != nil {
		return err
	}

	dstFile, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	defer dstFile.Close()

	_, err = io.Copy(dstFile, srcFile)
	return err
}

func relPath(path, base string) string {
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return filepath.Base(path)
	}
	return rel
}
