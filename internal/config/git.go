package config

import (
	"os"
	"path/filepath"
	"strings"
)

// GitBranch reads the current branch from .git/HEAD without running git
// (fast enough for the first frame). Returns "" outside a repo and a short
// SHA for a detached HEAD.
func GitBranch(dir string) string {
	for d := dir; ; {
		head := filepath.Join(d, ".git", "HEAD")
		if data, err := os.ReadFile(head); err == nil {
			s := strings.TrimSpace(string(data))
			if ref, ok := strings.CutPrefix(s, "ref: refs/heads/"); ok {
				return ref
			}
			if len(s) >= 7 {
				return s[:7]
			}
			return s
		}
		parent := filepath.Dir(d)
		if parent == d {
			return ""
		}
		d = parent
	}
}
