package paths

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func UserHome() string {
	return rootFromEnv("SCICLAW_HOME", "sciclaw")
}

func StateHome() string {
	return rootFromEnv("SCICLAW_STATE_HOME", ".sciclaw")
}

func ConfigPath() string {
	return filepath.Join(StateHome(), "config.json")
}

func ProfileDir() string {
	return filepath.Join(UserHome(), "profile")
}

func GlobalSkillsDir() string {
	return filepath.Join(UserHome(), "skills")
}

func TemplatesDir() string {
	return filepath.Join(UserHome(), "templates")
}

func BackupsDir() string {
	return filepath.Join(UserHome(), "backups")
}

func AddonsDir() string {
	return filepath.Join(UserHome(), "addons")
}

func DefaultWorkspace() string {
	return filepath.Join(UserHome(), "workspaces", "default")
}

func AuthPath() string {
	return filepath.Join(StateHome(), "auth.json")
}

func LogsDir() string {
	return filepath.Join(StateHome(), "logs")
}

func RuntimeDir() string {
	return filepath.Join(StateHome(), "runtime")
}

func CacheDir() string {
	return filepath.Join(StateHome(), "cache")
}

func TempDir() string {
	return filepath.Join(StateHome(), "tmp")
}

func WorkspacesStateDir() string {
	return filepath.Join(StateHome(), "workspaces")
}

func GatewayLogPath() string {
	return filepath.Join(LogsDir(), "gateway.log")
}

func GatewayErrorLogPath() string {
	return filepath.Join(LogsDir(), "gateway.err.log")
}

func WebLogPath() string {
	return filepath.Join(LogsDir(), "web.log")
}

func WebErrorLogPath() string {
	return filepath.Join(LogsDir(), "web.err.log")
}

func GatewayStatusPath() string {
	return filepath.Join(RuntimeDir(), "gateway.status.json")
}

func RoutingReloadPath() string {
	return filepath.Join(RuntimeDir(), "routing.reload")
}

func JobsPath() string {
	return filepath.Join(RuntimeDir(), "jobs.json")
}

func InteractiveHistoryPath() string {
	return filepath.Join(RuntimeDir(), "history")
}

func WorkspaceStateDir(workspacePath string) string {
	return filepath.Join(WorkspacesStateDir(), WorkspaceID(workspacePath))
}

func WorkspaceID(workspacePath string) string {
	canonical := canonicalWorkspacePath(workspacePath)
	if canonical == canonicalWorkspacePath(DefaultWorkspace()) {
		return "default"
	}
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

func WorkspaceVenvBinDir(workspacePath string) string {
	return filepath.Join(WorkspaceStateDir(workspacePath), ".venv", "bin")
}

func EnsureStateLayout() error {
	if err := ValidateHomes(); err != nil {
		return err
	}
	for _, dir := range []string{StateHome(), LogsDir(), RuntimeDir(), CacheDir(), TempDir(), WorkspacesStateDir()} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		if err := os.Chmod(dir, 0700); err != nil {
			return err
		}
	}
	return nil
}

func EnsureWorkspaceStateDir(workspacePath string) (string, error) {
	if err := ValidateWorkspace(workspacePath); err != nil {
		return "", err
	}
	if err := EnsureStateLayout(); err != nil {
		return "", err
	}
	dir := WorkspaceStateDir(workspacePath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return "", err
	}
	return dir, nil
}

func ValidateHomes() error {
	userHome := UserHome()
	stateHome := StateHome()
	if IsPicoClawPath(userHome) {
		return fmt.Errorf("SCICLAW_HOME must not be ~/.picoclaw or a directory beneath it")
	}
	if IsPicoClawPath(stateHome) {
		return fmt.Errorf("SCICLAW_STATE_HOME must not be ~/.picoclaw or a directory beneath it")
	}
	if pathsOverlap(userHome, stateHome) || pathsOverlap(resolvePhysicalPath(userHome), resolvePhysicalPath(stateHome)) {
		return fmt.Errorf("SCICLAW_HOME and SCICLAW_STATE_HOME must be separate, non-nested directories")
	}
	return nil
}

func ValidateWorkspace(workspacePath string) error {
	if IsPicoClawPath(workspacePath) {
		return fmt.Errorf("SciClaw workspace must not be ~/.picoclaw or a directory beneath it: %s", workspacePath)
	}
	stateHome := StateHome()
	if pathsOverlap(cleanAbsolute(workspacePath), cleanAbsolute(stateHome)) || pathsOverlap(resolvePhysicalPath(workspacePath), resolvePhysicalPath(stateHome)) {
		return fmt.Errorf("SciClaw workspace must not overlap SCICLAW_STATE_HOME: %s", workspacePath)
	}
	return nil
}

func IsPicoClawPath(path string) bool {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return false
	}
	legacyRoot := filepath.Join(home, ".picoclaw")
	return pathWithin(cleanAbsolute(path), cleanAbsolute(legacyRoot)) || pathWithin(resolvePhysicalPath(path), resolvePhysicalPath(legacyRoot))
}

func pathsOverlap(first, second string) bool {
	return pathWithin(first, second) || pathWithin(second, first)
}

func pathWithin(path, root string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && (relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))))
}

func resolvePhysicalPath(path string) string {
	absolute := cleanAbsolute(path)
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		return filepath.Clean(resolved)
	}
	current := absolute
	for {
		parent := filepath.Dir(current)
		if parent == current {
			return absolute
		}
		if resolved, err := filepath.EvalSymlinks(parent); err == nil {
			relative, relErr := filepath.Rel(parent, absolute)
			if relErr == nil {
				return filepath.Clean(filepath.Join(resolved, relative))
			}
			return absolute
		}
		current = parent
	}
}

func rootFromEnv(envName, defaultName string) string {
	if value := strings.TrimSpace(os.Getenv(envName)); value != "" {
		return filepath.Clean(expandHome(value))
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return filepath.Join(".", defaultName)
	}
	return filepath.Join(home, defaultName)
}

func canonicalWorkspacePath(path string) string {
	return cleanAbsolute(path)
}

func cleanAbsolute(path string) string {
	path = filepath.Clean(expandHome(strings.TrimSpace(path)))
	if absolute, err := filepath.Abs(path); err == nil {
		return filepath.Clean(absolute)
	}
	return path
}

func expandHome(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return path
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, path[2:])
}
