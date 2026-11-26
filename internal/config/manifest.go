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

// HookChange is a hook (or config) file whose content differs from the
// approved hooks manifest.
type HookChange struct {
	Key  string // "api/deploy.sh" or "muster.json"
	Path string
	Kind string // "changed", "new", "removed"
}

// ManifestPath is .muster/hooks.manifest inside the project.
func (p *Project) ManifestPath() string {
	return filepath.Join(p.Dir, ".muster", "hooks.manifest")
}

// HookChanges compares hook files on disk with .muster/hooks.manifest. With no
// manifest (project new on this machine) every hook is reported as new.
func (p *Project) HookChanges() []HookChange {
	hooksDir := filepath.Join(p.Dir, ".muster", "hooks")
	approved := map[string]string{}
	var manifest *jsonx.Object
	if data, err := os.ReadFile(p.ManifestPath()); err == nil {
		manifest, _ = jsonx.Parse(data)
	}
	if manifest != nil {
		for _, k := range manifest.Keys() {
			approved[k] = manifest.String(k, "sha256")
		}
	}
	var out []HookChange
	present := map[string]bool{}
	dirs, _ := os.ReadDir(hooksDir)
	for _, d := range dirs {
		if !d.IsDir() || d.Name() == "logs" || d.Name() == "pids" {
			continue
		}
		files, _ := os.ReadDir(filepath.Join(hooksDir, d.Name()))
		for _, f := range files {
			if f.IsDir() || !(strings.HasSuffix(f.Name(), ".sh") || f.Name() == "justfile") {
				continue
			}
			key := d.Name() + "/" + f.Name()
			path := filepath.Join(hooksDir, d.Name(), f.Name())
			present[key] = true
			sum, err := fileSHA(path)
			if err != nil {
				continue
			}
			want, ok := approved[key]
			switch {
			case !ok:
				out = append(out, HookChange{Key: key, Path: path, Kind: "new"})
			case want != sum:
				out = append(out, HookChange{Key: key, Path: path, Kind: "changed"})
			}
		}
	}
	if manifest != nil {
		for key, want := range approved {
			if strings.Contains(key, "/") {
				if !present[key] {
					out = append(out, HookChange{Key: key, Path: filepath.Join(hooksDir, key), Kind: "removed"})
				}
				continue
			}
			// config entry, e.g. "muster.json"
			path := filepath.Join(p.Dir, key)
			if sum, err := fileSHA(path); err == nil && want != "" && sum != want {
				out = append(out, HookChange{Key: key, Path: path, Kind: "changed"})
			}
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
