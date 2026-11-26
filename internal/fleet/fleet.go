// Package fleet reads fleet machines and groups, tests SSH reachability and
// reports Fleet Cloud login state.
package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Muster-dev/muster-tui/internal/engine"
	"github.com/Muster-dev/muster-tui/internal/proc"
)

// Machine is one fleet target.
type Machine struct {
	Name      string `json:"-"`
	Host      string `json:"host"`
	User      string `json:"user"`
	Port      int    `json:"-"`
	Identity  string `json:"identity_file"`
	Path      string `json:"project_dir"`
	Mode      string `json:"mode"`      // muster, push
	Transport string `json:"transport"` // ssh, cloud
	HookMode  string `json:"hook_mode"`
}

// Target renders user@host[:port].
func (m Machine) Target() string {
	t := m.Host
	if m.User != "" {
		t = m.User + "@" + t
	}
	if m.Port != 0 && m.Port != 22 {
		t += ":" + strconv.Itoa(m.Port)
	}
	return t
}

// Group is a named set of machines.
type Group struct {
	Name     string
	Machines []string
}

// Inventory is all machines and groups.
type Inventory struct {
	Machines []Machine
	Groups   []Group
}

// Names returns machine names.
func (inv Inventory) Names() []string {
	out := make([]string, len(inv.Machines))
	for i, m := range inv.Machines {
		out[i] = m.Name
	}
	return out
}

// GroupNames returns group names.
func (inv Inventory) GroupNames() []string {
	out := make([]string, len(inv.Groups))
	for i, g := range inv.Groups {
		out[i] = g.Name
	}
	return out
}

// Find returns the machine with name.
func (inv Inventory) Find(name string) (Machine, bool) {
	for _, m := range inv.Machines {
		if m.Name == name {
			return m, true
		}
	}
	return Machine{}, false
}

// Parse decodes `muster fleet list --json` or a remotes.json file. It accepts
// machines/groups as objects (current) or arrays (empty legacy output).
func Parse(data []byte) (Inventory, error) {
	var raw struct {
		Machines json.RawMessage `json:"machines"`
		Groups   json.RawMessage `json:"groups"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return Inventory{}, err
	}
	var inv Inventory
	if len(raw.Machines) > 0 && raw.Machines[0] == '{' {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw.Machines, &m); err != nil {
			return inv, err
		}
		for name, body := range m {
			var mc Machine
			_ = json.Unmarshal(body, &mc)
			var extra struct {
				Port any `json:"port"`
			}
			_ = json.Unmarshal(body, &extra)
			mc.Port = anyInt(extra.Port)
			mc.Name = name
			if mc.Transport == "" {
				mc.Transport = "ssh"
			}
			if mc.Mode == "" {
				switch mc.HookMode {
				case "manual":
					mc.Mode = "muster"
				default:
					mc.Mode = "push"
				}
			}
			inv.Machines = append(inv.Machines, mc)
		}
	}
	if len(raw.Groups) > 0 && raw.Groups[0] == '{' {
		var g map[string]json.RawMessage
		if err := json.Unmarshal(raw.Groups, &g); err == nil {
			for name, body := range g {
				var members []string
				if json.Unmarshal(body, &members) != nil {
					var obj struct {
						Machines []string `json:"machines"`
					}
					_ = json.Unmarshal(body, &obj)
					members = obj.Machines
				}
				inv.Groups = append(inv.Groups, Group{Name: name, Machines: members})
			}
		}
	}
	sort.Slice(inv.Machines, func(i, j int) bool { return inv.Machines[i].Name < inv.Machines[j].Name })
	sort.Slice(inv.Groups, func(i, j int) bool { return inv.Groups[i].Name < inv.Groups[j].Name })
	return inv, nil
}

func anyInt(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case string:
		n, _ := strconv.Atoi(t)
		return n
	}
	return 0
}

// List asks the CLI, falling back to remotes.json files when the CLI is
// unavailable or not paired.
func List(ctx context.Context, eng *engine.Engine, projectDir string) (Inventory, error) {
	if eng != nil {
		var raw json.RawMessage
		if err := eng.RunJSON(ctx, &raw, "fleet", "list", "--json"); err == nil {
			return Parse(raw)
		} else if ctx.Err() != nil {
			return Inventory{}, ctx.Err()
		}
	}
	return ReadFiles(projectDir), nil
}

// ReadFiles reads remotes.json from the project, then ~/.muster.
func ReadFiles(projectDir string) Inventory {
	home, _ := os.UserHomeDir()
	for _, p := range []string{filepath.Join(projectDir, "remotes.json"), filepath.Join(home, ".muster", "remotes.json")} {
		if projectDir == "" && strings.HasPrefix(p, "remotes.json") {
			continue
		}
		if data, err := os.ReadFile(p); err == nil {
			if inv, err := Parse(data); err == nil {
				return inv
			}
		}
	}
	return Inventory{}
}

func sshArgs(m Machine, batch bool) []string {
	args := []string{}
	if batch {
		args = append(args, "-o", "BatchMode=yes", "-o", "ConnectTimeout=5")
	}
	if m.Port != 0 && m.Port != 22 {
		args = append(args, "-p", strconv.Itoa(m.Port))
	}
	if m.Identity != "" {
		args = append(args, "-i", expandHome(m.Identity))
	}
	dest := m.Host
	if m.User != "" {
		dest = m.User + "@" + m.Host
	}
	return append(args, dest)
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[2:])
	}
	return p
}

// SSHBin is the ssh executable (tests override it).
var SSHBin = "ssh"

// Test checks SSH reachability non-interactively and returns the round trip.
func Test(ctx context.Context, m Machine) (time.Duration, error) {
	if m.Transport == "cloud" {
		return 0, fmt.Errorf("%s uses the cloud relay — test it with `muster fleet test %s`", m.Name, m.Name)
	}
	start := time.Now()
	cmd := proc.Command(ctx, SSHBin, append(sshArgs(m, true), "true")...)
	if _, err := proc.Output(cmd); err != nil {
		var ee *proc.ExitError
		if pe, ok := err.(*proc.ExitError); ok {
			ee = pe
		}
		if ee != nil && ee.Stderr != "" {
			return 0, fmt.Errorf("%s", proc.FirstLine(ee.Stderr))
		}
		return 0, err
	}
	return time.Since(start), nil
}

// SSHCmd opens an interactive shell on m (for tea.ExecProcess).
func SSHCmd(m Machine) *exec.Cmd {
	args := sshArgs(m, false)
	if m.Path != "" {
		args = append([]string{"-t"}, append(args, "cd "+shellQuote(m.Path)+" && exec $SHELL -l")...)
	}
	return exec.Command(SSHBin, args...)
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// Cloud is the Fleet Cloud login state.
type Cloud struct {
	LoggedIn bool
	Name     string
	Org      string
	Relay    string
}

// CloudStatus reads ~/.muster/settings.json (cloud.*) and the cloud token
// store without touching the network.
func CloudStatus() Cloud {
	home, _ := os.UserHomeDir()
	var c Cloud
	if data, err := os.ReadFile(filepath.Join(home, ".muster", "settings.json")); err == nil {
		var s struct {
			Cloud struct {
				Relay string `json:"relay"`
				Org   string `json:"org_id"`
				Name  string `json:"name"`
			} `json:"cloud"`
		}
		_ = json.Unmarshal(data, &s)
		c.Relay, c.Org, c.Name = s.Cloud.Relay, s.Cloud.Org, s.Cloud.Name
	}
	if data, err := os.ReadFile(filepath.Join(home, ".muster", "tokens", "cloud.json")); err == nil {
		var t struct {
			Tokens map[string]string `json:"tokens"`
		}
		_ = json.Unmarshal(data, &t)
		c.LoggedIn = t.Tokens["default"] != "" && c.Org != ""
	}
	return c
}
