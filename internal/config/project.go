// Package config loads, validates and saves muster project configuration
// (muster.json, with deploy.json as a legacy fallback) and global settings.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Muster-dev/muster-tui/internal/jsonx"
)

// ConfigNames are the project config file names, in priority order.
var ConfigNames = []string{"muster.json", "deploy.json"}

// ErrNoProject is returned when no config file is found.
var ErrNoProject = errors.New("no muster.json found")

// Project is a loaded project config. Doc is the source of truth; typed
// accessors read from it and edits write to it, so unknown keys survive.
type Project struct {
	Dir  string
	Path string
	Doc  *jsonx.Object
}

// Health is a service health check.
type Health struct {
	Enabled  bool
	Type     string // http, tcp, command
	Endpoint string
	Port     int
	Timeout  int
	Command  string
}

// GitPull configures the pre-deploy git pull.
type GitPull struct {
	Enabled bool
	Remote  string
	Branch  string
}

// Remote configures SSH deploys for a service.
type Remote struct {
	Enabled      bool
	Host         string
	User         string
	Port         int
	IdentityFile string
	ProjectDir   string
}

// K8s names the deployment behind a service.
type K8s struct {
	Deployment string
	Namespace  string
}

// Service is a typed snapshot of one service.
type Service struct {
	Key           string
	Name          string
	SkipDeploy    bool
	Health        Health
	CredsMode     string
	GitPull       GitPull
	Remote        Remote
	K8s           K8s
	DeployTimeout int
	DeployMode    string
}

// Find walks up from dir looking for a project config.
func Find(dir string) (string, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	for {
		if p := FindIn(dir); p != "" {
			return p, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", ErrNoProject
		}
		dir = parent
	}
}

// FindIn returns the config path inside dir without walking up ("" if none).
func FindIn(dir string) string {
	for _, n := range ConfigNames {
		p := filepath.Join(dir, n)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// Load reads and parses a project config file.
func Load(path string) (*Project, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	doc, err := jsonx.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s is not valid JSON: %w", filepath.Base(path), err)
	}
	abs, _ := filepath.Abs(path)
	return &Project{Dir: filepath.Dir(abs), Path: abs, Doc: doc}, nil
}

// Name is the project name (falls back to the directory name).
func (p *Project) Name() string {
	if n := p.Doc.String("project"); n != "" {
		return n
	}
	return filepath.Base(p.Dir)
}

// ServiceKeys returns service keys in deploy order: deploy_order first, then
// any remaining services sorted by key.
func (p *Project) ServiceKeys() []string {
	svcs := p.Doc.Obj("services")
	if svcs == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, k := range p.Doc.Strings("deploy_order") {
		if _, ok := svcs.Value(k); ok && !seen[k] {
			out = append(out, k)
			seen[k] = true
		}
	}
	var rest []string
	for _, k := range svcs.Keys() {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

// DeployOrder returns the raw deploy_order array.
func (p *Project) DeployOrder() []string { return p.Doc.Strings("deploy_order") }

// Services returns typed snapshots in deploy order.
func (p *Project) Services() []Service {
	keys := p.ServiceKeys()
	out := make([]Service, 0, len(keys))
	for _, k := range keys {
		out = append(out, p.Service(k))
	}
	return out
}

// HasService reports whether key is a configured service.
func (p *Project) HasService(key string) bool {
	_, ok := p.Doc.Get("services", key)
	return ok
}

// Service returns a typed snapshot of one service.
func (p *Project) Service(key string) Service {
	o := p.Doc.Obj("services", key)
	s := Service{Key: key, Name: o.String("name")}
	if s.Name == "" {
		s.Name = key
	}
	s.SkipDeploy, _ = o.Bool("skip_deploy")
	en, ok := o.Bool("health", "enabled")
	s.Health.Enabled = en || !ok && o.String("health", "type") != ""
	s.Health.Type = o.String("health", "type")
	s.Health.Endpoint = o.String("health", "endpoint")
	s.Health.Port, _ = o.Int("health", "port")
	s.Health.Timeout, _ = o.Int("health", "timeout")
	s.Health.Command = o.String("health", "command")
	s.CredsMode = o.String("credentials", "mode")
	if s.CredsMode == "" {
		s.CredsMode = "off"
	}
	s.GitPull.Enabled, _ = o.Bool("git_pull", "enabled")
	s.GitPull.Remote = o.String("git_pull", "remote")
	s.GitPull.Branch = o.String("git_pull", "branch")
	s.Remote.Enabled, _ = o.Bool("remote", "enabled")
	s.Remote.Host = o.String("remote", "host")
	s.Remote.User = o.String("remote", "user")
	s.Remote.Port, _ = o.Int("remote", "port")
	s.Remote.IdentityFile = o.String("remote", "identity_file")
	s.Remote.ProjectDir = o.String("remote", "project_dir")
	s.K8s.Deployment = o.String("k8s", "deployment")
	s.K8s.Namespace = o.String("k8s", "namespace")
	s.DeployTimeout, _ = o.Int("deploy_timeout")
	s.DeployMode = o.String("deploy_mode")
	return s
}

// HealthLabel is a short description such as "http :8080" or "tcp :6379".
func (s Service) HealthLabel() string {
	if !s.Health.Enabled {
		return "no check"
	}
	switch s.Health.Type {
	case "http":
		if s.Health.Port > 0 {
			return fmt.Sprintf("http :%d", s.Health.Port)
		}
		return "http"
	case "tcp":
		if s.Health.Port > 0 {
			return fmt.Sprintf("tcp :%d", s.Health.Port)
		}
		return "tcp"
	case "command":
		return "command"
	case "":
		return "hook"
	}
	return s.Health.Type
}

// HookPath returns the path of a hook script for a service.
func (p *Project) HookPath(svc, hook string) string {
	return filepath.Join(p.Dir, ".muster", "hooks", svc, hook+".sh")
}

// Stack guesses a service's stack from its deploy hook (k8s, compose, docker,
// dev, bare). The CLI does not record it in muster.json.
func (p *Project) Stack(svc string) string {
	data, err := os.ReadFile(p.HookPath(svc, "deploy"))
	if err != nil {
		return ""
	}
	s := string(data)
	switch {
	case strings.Contains(s, "kubectl"):
		return "k8s"
	case strings.Contains(s, "docker compose") || strings.Contains(s, "docker-compose"):
		return "compose"
	case strings.Contains(s, ".muster/pids") || strings.Contains(s, "MUSTER_PID"):
		return "dev"
	case strings.Contains(s, "docker "):
		return "docker"
	}
	return "bare"
}

// Reload re-reads the file from disk.
func (p *Project) Reload() (*Project, error) { return Load(p.Path) }

// Save validates and writes the document atomically.
func (p *Project) Save() error {
	if errs := p.Validate(); len(errs) > 0 {
		return errs[0]
	}
	data, err := p.Doc.Pretty()
	if err != nil {
		return err
	}
	return writeAtomic(p.Path, data, 0o644)
}

// Clone returns a deep copy (for editing without touching the original).
func (p *Project) Clone() *Project {
	return &Project{Dir: p.Dir, Path: p.Path, Doc: p.Doc.Clone()}
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
