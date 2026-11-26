// Package docker talks to the local Docker engine through the docker CLI
// (`--format '{{json .}}'`), so it works with Docker Desktop, colima, OrbStack
// and remote contexts alike.
package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/Muster-dev/muster-tui/internal/proc"
)

// Errors callers can explain to the user.
var (
	ErrNotInstalled = errors.New("docker is not installed")
	ErrDaemonDown   = errors.New("docker isn't running")
)

// Client runs the docker CLI.
type Client struct {
	Bin string
	Env []string
}

// Locate finds docker on PATH ($MUSTER_DOCKER_BIN overrides).
func Locate() (*Client, error) {
	if p := os.Getenv("MUSTER_DOCKER_BIN"); p != "" {
		return &Client{Bin: p}, nil
	}
	p, err := exec.LookPath("docker")
	if err != nil {
		return nil, ErrNotInstalled
	}
	return &Client{Bin: p}, nil
}

func (c *Client) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := proc.Command(ctx, c.Bin, args...)
	cmd.Env = append(os.Environ(), c.Env...)
	return cmd
}

func (c *Client) run(ctx context.Context, args ...string) ([]byte, error) {
	if c == nil {
		return nil, ErrNotInstalled
	}
	out, err := proc.Output(c.command(ctx, args...))
	if err != nil {
		return out, classify(err)
	}
	return out, nil
}

// IsDaemonDownText reports whether docker output means the daemon is down.
func IsDaemonDownText(s string) bool {
	s = strings.ToLower(s)
	for _, needle := range []string{"cannot connect to the docker daemon", "failed to connect to the docker api", "is the docker daemon running", "error during connect", "docker.sock: connect: no such file"} {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

func classify(err error) error {
	var ee *proc.ExitError
	if errors.As(err, &ee) && IsDaemonDownText(ee.Stderr) {
		return fmt.Errorf("%w: %s", ErrDaemonDown, proc.FirstLine(ee.Stderr))
	}
	return err
}

func decodeLines[T any](out []byte) ([]T, error) {
	var items []T
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var v T
		if err := json.Unmarshal(line, &v); err != nil {
			return items, err
		}
		items = append(items, v)
	}
	return items, sc.Err()
}

// Container is one row of `docker ps -a`.
type Container struct {
	ID         string `json:"ID"`
	Names      string `json:"Names"`
	Image      string `json:"Image"`
	State      string `json:"State"`  // running, exited, created, paused, restarting
	Status     string `json:"Status"` // "Up 3 minutes (healthy)"
	Ports      string `json:"Ports"`
	Labels     string `json:"Labels"`
	RunningFor string `json:"RunningFor"`
	CreatedAt  string `json:"CreatedAt"`

	CPU string `json:"-"`
	Mem string `json:"-"`
}

// Name returns the first container name.
func (c Container) Name() string { return strings.Split(c.Names, ",")[0] }

// Running reports whether the container is up.
func (c Container) Running() bool { return c.State == "running" || c.State == "restarting" }

// Label returns a label value.
func (c Container) Label(key string) string {
	for _, kv := range strings.Split(c.Labels, ",") {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v
		}
	}
	return ""
}

// ComposeService is the compose service label, if any.
func (c Container) ComposeService() string { return c.Label("com.docker.compose.service") }

// ShortPorts renders "0.0.0.0:3000->80/tcp, :::3000->80/tcp" as ":3000→80".
func (c Container) ShortPorts() string {
	seen := map[string]bool{}
	var out []string
	for _, p := range strings.Split(c.Ports, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		host, ctr, ok := strings.Cut(p, "->")
		if !ok {
			ctr = p
			host = ""
		}
		ctr = strings.TrimSuffix(ctr, "/tcp")
		if i := strings.LastIndex(host, ":"); i >= 0 {
			host = host[i+1:]
		}
		s := ctr
		if host != "" {
			s = ":" + host + "→" + ctr
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return strings.Join(out, " ")
}

// Containers lists all containers, running first.
func (c *Client) Containers(ctx context.Context) ([]Container, error) {
	out, err := c.run(ctx, "ps", "-a", "--no-trunc=false", "--format", "{{json .}}")
	if err != nil {
		return nil, err
	}
	cs, err := decodeLines[Container](out)
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].Running() != cs[j].Running() {
			return cs[i].Running()
		}
		return cs[i].Name() < cs[j].Name()
	})
	return cs, err
}

// Stat is one row of `docker stats --no-stream`.
type Stat struct {
	ID       string `json:"ID"`
	Name     string `json:"Name"`
	CPUPerc  string `json:"CPUPerc"`
	MemUsage string `json:"MemUsage"`
	MemPerc  string `json:"MemPerc"`
}

// Stats returns CPU/memory per running container, keyed by short ID and name.
func (c *Client) Stats(ctx context.Context) (map[string]Stat, error) {
	out, err := c.run(ctx, "stats", "--no-stream", "--format", "{{json .}}")
	if err != nil {
		return nil, err
	}
	rows, err := decodeLines[Stat](out)
	m := map[string]Stat{}
	for _, r := range rows {
		m[r.ID] = r
		m[r.Name] = r
	}
	return m, err
}

// Image is one row of `docker images`.
type Image struct {
	ID           string `json:"ID"`
	Repository   string `json:"Repository"`
	Tag          string `json:"Tag"`
	Size         string `json:"Size"`
	CreatedSince string `json:"CreatedSince"`
	Containers   string `json:"Containers"`
}

// Ref returns repo:tag (or the ID for dangling images).
func (i Image) Ref() string {
	if i.Dangling() {
		return i.ID
	}
	return i.Repository + ":" + i.Tag
}

// Dangling reports an untagged image.
func (i Image) Dangling() bool { return i.Repository == "<none>" || i.Tag == "<none>" }

// Images lists images.
func (c *Client) Images(ctx context.Context) ([]Image, error) {
	out, err := c.run(ctx, "images", "--format", "{{json .}}")
	if err != nil {
		return nil, err
	}
	return decodeLines[Image](out)
}

// Volume is one row of `docker volume ls`.
type Volume struct {
	Name       string `json:"Name"`
	Driver     string `json:"Driver"`
	Mountpoint string `json:"Mountpoint"`
	Size       string `json:"Size"`
	Links      string `json:"Links"`
}

// Volumes lists volumes.
func (c *Client) Volumes(ctx context.Context) ([]Volume, error) {
	out, err := c.run(ctx, "volume", "ls", "--format", "{{json .}}")
	if err != nil {
		return nil, err
	}
	return decodeLines[Volume](out)
}

// DiskUsage is one row of `docker system df`.
type DiskUsage struct {
	Type        string `json:"Type"`
	TotalCount  any    `json:"TotalCount"`
	Active      any    `json:"Active"`
	Size        string `json:"Size"`
	Reclaimable string `json:"Reclaimable"`
}

// Count renders TotalCount (docker emits a string or a number by version).
func (d DiskUsage) Count() string { return fmt.Sprint(d.TotalCount) }

// DiskUsage runs `docker system df`.
func (c *Client) DiskUsage(ctx context.Context) ([]DiskUsage, error) {
	out, err := c.run(ctx, "system", "df", "--format", "{{json .}}")
	if err != nil {
		return nil, err
	}
	return decodeLines[DiskUsage](out)
}

// Summary is the rail's one-liner.
type Summary struct {
	Running, Total int
	ImagesSize     string
	Reclaimable    string
}

// Summary returns container counts and image disk usage.
func (c *Client) Summary(ctx context.Context) (Summary, error) {
	cs, err := c.Containers(ctx)
	if err != nil {
		return Summary{}, err
	}
	s := Summary{Total: len(cs)}
	for _, x := range cs {
		if x.Running() {
			s.Running++
		}
	}
	if df, err := c.DiskUsage(ctx); err == nil {
		for _, d := range df {
			if d.Type == "Images" {
				s.ImagesSize = d.Size
				s.Reclaimable = d.Reclaimable
			}
		}
	}
	return s, nil
}

// Do runs a simple action: restart/stop/start/rm (containers), rmi/pull
// (images), "volume rm".
func (c *Client) Do(ctx context.Context, verb string, targets ...string) error {
	args := strings.Fields(verb)
	_, err := c.run(ctx, append(args, targets...)...)
	return err
}

// Prune removes dangling images or stopped containers and returns the
// "Total reclaimed space" line.
func (c *Client) Prune(ctx context.Context, what string) (string, error) {
	var args []string
	switch what {
	case "images":
		args = []string{"image", "prune", "-f"}
	case "containers":
		args = []string{"container", "prune", "-f"}
	case "volumes":
		args = []string{"volume", "prune", "-f"}
	default:
		return "", fmt.Errorf("unknown prune target %q", what)
	}
	out, err := c.run(ctx, args...)
	if err != nil {
		return "", err
	}
	for _, l := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(l, "Total reclaimed space:") {
			return strings.TrimSpace(strings.TrimPrefix(l, "Total reclaimed space:")), nil
		}
	}
	return "0B", nil
}

// Logs streams `docker logs --tail N [-f]`.
func (c *Client) Logs(ctx context.Context, id string, tail int, follow bool) (*proc.LineStream, error) {
	if c == nil {
		return nil, ErrNotInstalled
	}
	args := []string{"logs", "--tail", strconv.Itoa(tail)}
	if follow {
		args = append(args, "-f")
	}
	return proc.Lines(c.command(ctx, append(args, id)...))
}

// ShellCmd is an interactive shell in a container (for tea.ExecProcess).
func (c *Client) ShellCmd(id string) *exec.Cmd {
	return exec.Command(c.Bin, "exec", "-it", id, "sh", "-c", "command -v bash >/dev/null 2>&1 && exec bash || exec sh")
}

// ParseSize turns "1.21GB", "512MB (40%)" or "0B" into bytes (best effort).
func ParseSize(s string) float64 {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, " "); i >= 0 {
		s = s[:i]
	}
	units := []struct {
		suffix string
		mult   float64
	}{{"TB", 1e12}, {"GB", 1e9}, {"MB", 1e6}, {"kB", 1e3}, {"KB", 1e3}, {"B", 1}}
	for _, u := range units {
		if strings.HasSuffix(s, u.suffix) {
			n, err := strconv.ParseFloat(strings.TrimSuffix(s, u.suffix), 64)
			if err != nil {
				return 0
			}
			return n * u.mult
		}
	}
	return 0
}
