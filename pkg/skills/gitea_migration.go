package skills

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
)

// Only exact shipped versions qualify. No workspace files are replaced.
var giteaSkillHashes = map[string][2]string{
	"gitea-create-change-request":  {"6e3eb32ec9773b24cdee14ee7e070ce7413986c1cb8397a97520cca16c36d079", "78d4b04517baa52258cdb58bfbbc599899237cfc18be4a53f1d7a74c4aa5ea21"},
	"gitea-move-file-phase-0-to-1": {"f61dbffd924d38e5eba1b81181becfec01d60bd950efe8cbffcc1ffb9af22c2c", "ce3f7660726e5a3d5b9a24038fb9d829c3ce86eb07f71bef8e591667a0c893aa"},
	"gitea-move-work-item":         {"5fdc00d70e4787868471cf0e5814a2375c787f944790ee91a375bc4fc1cb74bd", "ae79c9ef1449458869724236cd53d261fd11cf2a3a279342a4d651093823be15"},
}

func (sl *SkillsLoader) safeLegacyGitea(name, path string) string {
	hashes, ok := giteaSkillHashes[name]
	if !ok {
		return ""
	}
	for _, p := range []string{filepath.Dir(path), path} {
		info, err := os.Lstat(p)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return ""
		}
	}
	old, err := os.ReadFile(path)
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(old)) != hashes[0] {
		return ""
	}
	current, err := os.ReadFile(filepath.Join(sl.builtinSkills, name, "SKILL.md"))
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(current)) != hashes[1] {
		return ""
	}
	return string(current)
}
