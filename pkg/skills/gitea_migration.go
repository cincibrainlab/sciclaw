package skills

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

// Only exact shipped versions qualify. No workspace files are replaced.
var giteaSkillHashes = map[string]struct {
	current string
	legacy  []string
}{
	"gitea-create-change-request":  {"78d4b04517baa52258cdb58bfbbc599899237cfc18be4a53f1d7a74c4aa5ea21", []string{"6e3eb32ec9773b24cdee14ee7e070ce7413986c1cb8397a97520cca16c36d079", "ee39a8e67d4816f34ab14dc22d3b9ea8331e222f4b2c56d7de5547994f8ec7a2"}},
	"gitea-move-file-phase-0-to-1": {"ce3f7660726e5a3d5b9a24038fb9d829c3ce86eb07f71bef8e591667a0c893aa", []string{"f61dbffd924d38e5eba1b81181becfec01d60bd950efe8cbffcc1ffb9af22c2c"}},
	"gitea-move-work-item":         {"ae79c9ef1449458869724236cd53d261fd11cf2a3a279342a4d651093823be15", []string{"5fdc00d70e4787868471cf0e5814a2375c787f944790ee91a375bc4fc1cb74bd", "9fcaccde43f21eb961ad547c08b6154d30d7c8d361f1e0d3e1b179ab3005c4a6", "97426f8fb473acfda4227e9e802100d1821040c8655c7f0e21f2c3ec8cebb454"}},
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
	if err != nil || !slices.Contains(hashes.legacy, fmt.Sprintf("%x", sha256.Sum256(old))) {
		return ""
	}
	current, err := os.ReadFile(filepath.Join(sl.builtinSkills, name, "SKILL.md"))
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(current)) != hashes.current {
		return ""
	}
	return string(current)
}
