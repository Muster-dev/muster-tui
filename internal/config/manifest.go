package config

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Muster-dev/muster-tui/internal/jsonx"
)

// HookChange is a hook (or config) file whose content differs from what was
// approved on this machine.
type HookChange struct {
	Key      string // "api/deploy.sh" or "_config/muster.json"
	Path     string // current file
	Approved string // approved copy for diffs ("" when none)
	Kind     string // "changed", "new", "removed"
}

// Label is a display name ("api/deploy.sh", "muster.json").
func (c HookChange) Label() string { return strings.TrimPrefix(c.Key, "_config/") }

// TrustDir is where the CLI keeps this machine's approvals for the project:
// ~/.muster/trust/<sha256 of the physical project path>/.
func (p *Project) TrustDir() string {
	abs := p.Dir
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		abs = r
	}
	sum := sha256.Sum256([]byte(abs))
	return filepath.Join(MusterHome(), "trust", hex.EncodeToString(sum[:]))
}

// ManifestPath returns the approvals manifest: the per-user trust store, or
// the legacy in-repo .muster/hooks.manifest when that's all there is.
func (p *Project) ManifestPath() string {
	trust := filepath.Join(p.TrustDir(), "hooks.manifest")
	if _, err := os.Stat(trust); err == nil {
		return trust
	}
	legacy := filepath.Join(p.Dir, ".muster", "hooks.manifest")
	if _, err := os.Stat(legacy); err == nil {
		return legacy
	}
	return trust
}

func (p *Project) keyPath(key string) string {
	if rest, ok := strings.CutPrefix(key, "_config/"); ok {
		return filepath.Join(p.Dir, rest)
	}
	if !strings.Contains(key, "/") { // legacy config entry ("muster.json")
		return filepath.Join(p.Dir, key)
	}
	return filepath.Join(p.Dir, ".muster", "hooks", key)
}

func (p *Project) approvedCopy(key string) string {
	c := filepath.Join(p.TrustDir(), "approved", key)
	if _, err := os.Stat(c); err == nil {
		return c
	}
	return ""
}

// HookChanges compares hooks and the config file with the approvals
// manifest. With no manifest (project new on this machine) every hook is new.
func (p *Project) HookChanges() []HookChange {
	approved := map[string]string{}
	var manifest *jsonx.Object
	if data, err := os.ReadFile(p.ManifestPath()); err == nil {
		manifest, _ = jsonx.Parse(data)
	}
	for _, k := range manifest.Keys() {
		approved[k] = manifest.String(k, "sha256")
	}
	var out []HookChange
	seen := map[string]bool{}
	check := func(key string) {
		seen[key] = true
		path := p.keyPath(key)
		sum, err := fileSHA(path)
		if err != nil {
			return
		}
		want, ok := approved[key]
		if !ok && strings.HasPrefix(key, "_config/") {
			want, ok = approved[strings.TrimPrefix(key, "_config/")]
		}
		switch {
		case !ok && manifest == nil, !ok && !strings.HasPrefix(key, "_config/"):
			out = append(out, HookChange{Key: key, Path: path, Kind: "new"})
		case ok && want != "" && want != sum:
			out = append(out, HookChange{Key: key, Path: path, Approved: p.approvedCopy(key), Kind: "changed"})
		}
	}
	hooksDir := filepath.Join(p.Dir, ".muster", "hooks")
	dirs, _ := os.ReadDir(hooksDir)
	for _, d := range dirs {
		if !d.IsDir() || d.Name() == "logs" || d.Name() == "pids" {
			continue
		}
		files, _ := os.ReadDir(filepath.Join(hooksDir, d.Name()))
		for _, f := range files {
			if !f.IsDir() && (strings.HasSuffix(f.Name(), ".sh") || f.Name() == "justfile") {
				check(d.Name() + "/" + f.Name())
			}
		}
	}
	if manifest != nil {
		check("_config/" + filepath.Base(p.Path))
		for key := range approved {
			if strings.HasPrefix(key, "_config/") || !strings.Contains(key, "/") || seen[key] {
				continue
			}
			out = append(out, HookChange{Key: key, Path: p.keyPath(key), Approved: p.approvedCopy(key), Kind: "removed"})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func fileSHA(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
