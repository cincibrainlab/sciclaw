package hookpolicy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func ResolveAuditPath(workspaceState, configuredPath string) (string, error) {
	hooksDir := filepath.Join(workspaceState, "hooks")
	configuredPath = strings.TrimSpace(configuredPath)
	if configuredPath == "" {
		configuredPath = "hook-events.jsonl"
	}
	if filepath.IsAbs(configuredPath) {
		return "", fmt.Errorf("absolute hook audit paths are not supported")
	}
	cleaned := filepath.Clean(configuredPath)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("hook audit path escapes workspace state: %s", configuredPath)
	}
	parts := strings.Split(filepath.ToSlash(cleaned), "/")
	if len(parts) > 0 && parts[0] == "hooks" {
		parts = parts[1:]
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("hook audit path must name a file inside hooks/")
	}
	target := filepath.Join(append([]string{hooksDir}, parts...)...)
	relative, err := filepath.Rel(hooksDir, target)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("hook audit path escapes workspace state: %s", configuredPath)
	}
	if info, err := os.Lstat(target); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("hook audit destination must not be a symlink: %s", target)
	} else if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	physicalState, err := filepath.EvalSymlinks(workspaceState)
	if err != nil {
		return "", fmt.Errorf("resolve workspace state: %w", err)
	}
	physicalHooksRoot := filepath.Join(physicalState, "hooks")
	physicalParent, err := resolveExistingPath(filepath.Dir(target))
	if err != nil {
		return "", err
	}
	if !isWithin(physicalParent, physicalHooksRoot) {
		return "", fmt.Errorf("hook audit parent escapes workspace state: %s", configuredPath)
	}
	return target, nil
}

func resolveExistingPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	current := absolute
	for {
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			relative, relErr := filepath.Rel(current, absolute)
			if relErr != nil {
				return "", relErr
			}
			return filepath.Join(resolved, relative), nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("no existing parent for %s", path)
		}
		current = parent
	}
}

func isWithin(path, root string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && (relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))))
}
