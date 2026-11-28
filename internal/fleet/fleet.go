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
	Fleet     string `json:"fleet"`  // muster ≥ 0.7 ("" for remotes.json)
	Group     string `json:"-"`      // muster ≥ 0.7, may be empty
	Paired    *bool  `json:"paired"` // muster ≥ 0.7; nil = unknown
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

// Parse decodes `muster fleet list --json` or a remotes.json file. It
// accepts both shapes:
//
//   - muster ≥ 0.7: machines and groups are arrays of objects with a "name"
//     ({"machines":[{"name":"prod-1",…}],"groups":[{"name":"prod","machines":[…]}]})
//   - muster < 0.7 and remotes.json: objects keyed by name
//     ({"machines":{"prod-1":{…}},"groups":{"prod":["prod-1"]}}), or empty arrays.
func Parse(data []byte) (Inventory, error) {
	var raw struct {
		Machines json.RawMessage `json:"machines"`
		Groups   json.RawMessage `json:"groups"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return Inventory{}, err
	}
	var inv Inventory
	switch firstByte(raw.Machines) {
	case '{':
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw.Machines, &m); err != nil {
			return inv, err
		}
		for name, body := range m {
			inv.Machines = append(inv.Machines, decodeMachine(name, body))
		}
	case '[':
		var list []json.RawMessage
		if err := json.Unmarshal(raw.Machines, &list); err != nil {
			return inv, err
		}
		for _, body := range list {
			var id struct {
				Name string `json:"name"`
			}
			if json.Unmarshal(body, &id) != nil || id.Name == "" {
				continue
			}
			inv.Machines = append(inv.Machines, decodeMachine(id.Name, body))
		}
	}
	switch firstByte(raw.Groups) {
	case '{':
		var g map[string]json.RawMessage
		if err := json.Unmarshal(raw.Groups, &g); err == nil {
			for name, body := range g {
				inv.Groups = append(inv.Groups, Group{Name: name, Machines: groupMembers(body)})
			}
		}
	case '[':
		var list []json.RawMessage
		if err := json.Unmarshal(raw.Groups, &list); err == nil {
			seen := map[string]int{}
			for _, body := range list {
				var obj struct {
					Name     string          `json:"name"`
					Machines json.RawMessage `json:"machines"`
				}
				if json.Unmarshal(body, &obj) != nil || obj.Name == "" {
					continue
				}
				members := groupMembers(obj.Machines)
				// The same group name in two fleets: one row, all members.
				if i, ok := seen[obj.Name]; ok {
					inv.Groups[i].Machines = appendNew(inv.Groups[i].Machines, members...)
					continue
				}
				seen[obj.Name] = len(inv.Groups)
				inv.Groups = append(inv.Groups, Group{Name: obj.Name, Machines: members})
			}
		}
	}
	sort.Slice(inv.Machines, func(i, j int) bool { return inv.Machines[i].Name < inv.Machines[j].Name })
	sort.Slice(inv.Groups, func(i, j int) bool { return inv.Groups[i].Name < inv.Groups[j].Name })
	return inv, nil
}

func firstByte(b json.RawMessage) byte {
	for _, c := range b {
		switch c {
		case ' ', '\t', '\n', '\r':
			continue
		}
		return c
	}
	return 0
}

func decodeMachine(name string, body json.RawMessage) Machine {
	var mc Machine
	_ = json.Unmarshal(body, &mc)
	var extra struct {
		Port  any `json:"port"`
		Group any `json:"group"`
	}
	_ = json.Unmarshal(body, &extra)
	mc.Port = anyInt(extra.Port)
	if g, ok := extra.Group.(string); ok {
		mc.Group = g
	}
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
	return mc
}

// groupMembers reads ["a","b"] or {"machines":["a","b"]}.
func groupMembers(body json.RawMessage) []string {
	var members []string
	if json.Unmarshal(body, &members) == nil {
		return members
	}
	var obj struct {
		Machines []string `json:"machines"`
	}
	_ = json.Unmarshal(body, &obj)
	return obj.Machines
}

func appendNew(xs []string, add ...string) []string {
	for _, a := range add {
		found := false
		for _, x := range xs {
			if x == a {
				found = true
				break
			}
		}
		if !found {
			xs = append(xs, a)
		}
	}
	return xs
}

// MachineStatus is one machine in `muster fleet status --json`.
type MachineStatus struct {
	Machine   string          `json:"machine"`
	Host      string          `json:"host"`
	Reachable bool            `json:"reachable"`
	Healthy   *int            `json:"healthy"` // nil when unknown
	Total     *int            `json:"total"`
	Error     string          `json:"error"`
	Services  []ServiceHealth `json:"-"`
}

// ServiceHealth is one service on a fleet machine.
type ServiceHealth struct {
	Service string `json:"service"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Detail  string `json:"detail"`
	Stack   string `json:"stack"`
	// LastDeploy is the server's own last deploy of this service, when the
	// CLI reports it.
	LastDeploy *struct {
		TS     string `json:"ts"`
		Status string `json:"status"`
		Commit string `json:"commit"`
	} `json:"last_deploy"`
}

// ParseStatus decodes `muster fleet status --json`: {"status":[…]} from
// muster ≥ 0.7, or the bare array older versions print.
func ParseStatus(data []byte) ([]MachineStatus, error) {
	var list []json.RawMessage
	switch firstByte(data) {
	case '[':
		if err := json.Unmarshal(data, &list); err != nil {
			return nil, err
		}
	case '{':
		var wrap struct {
			Status []json.RawMessage `json:"status"`
		}
		if err := json.Unmarshal(data, &wrap); err != nil {
			return nil, err
		}
		list = wrap.Status
	default:
		return nil, fmt.Errorf("fleet status: unexpected output")
	}
	out := make([]MachineStatus, 0, len(list))
	for _, body := range list {
		var ms MachineStatus
		if json.Unmarshal(body, &ms) != nil || ms.Machine == "" {
			continue
		}
		var svc struct {
			Services json.RawMessage `json:"services"`
		}
		_ = json.Unmarshal(body, &svc)
		switch firstByte(svc.Services) {
		case '[':
			_ = json.Unmarshal(svc.Services, &ms.Services)
		case '{': // a machine's own status --json services map
			var m map[string]ServiceHealth
			if json.Unmarshal(svc.Services, &m) == nil {
				for k, v := range m {
					v.Service = k
					ms.Services = append(ms.Services, v)
				}
				sort.Slice(ms.Services, func(i, j int) bool { return ms.Services[i].Service < ms.Services[j].Service })
			}
		}
		if ms.Healthy == nil && len(ms.Services) > 0 {
			h, t := 0, len(ms.Services)
			for _, s := range ms.Services {
				if s.Status == "healthy" {
					h++
				}
			}
			ms.Healthy, ms.Total = &h, &t
		}
		out = append(out, ms)
	}
	return out, nil
}

// Status asks the CLI for fleet health (SSH / relay round trips; slow).
func Status(ctx context.Context, eng *engine.Engine, target string) ([]MachineStatus, error) {
	args := []string{"fleet", "status"}
	if target != "" {
		args = append(args, target)
	}
	var raw json.RawMessage
	if err := eng.RunJSON(ctx, &raw, append(args, "--json")...); err != nil {
		return nil, err
	}
	return ParseStatus(raw)
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
	// HasToken: a CLI token exists (token file, or a keychain reference in
	// settings). The Muster Assistant needs only this, not an org.
	HasToken bool
	// AssistantOff is the global kill switch (settings "assistant": "off").
	AssistantOff bool
}

// CloudStatus reads ~/.muster/settings.json (cloud.*) and the cloud token
// store without touching the network.
func CloudStatus() Cloud {
	home, _ := os.UserHomeDir()
	var c Cloud
	if data, err := os.ReadFile(filepath.Join(home, ".muster", "settings.json")); err == nil {
		var s struct {
			Cloud struct {
				Relay    string `json:"relay"`
				Org      string `json:"org_id"`
				Name     string `json:"name"`
				TokenRef string `json:"token_ref"`
			} `json:"cloud"`
			Assistant string `json:"assistant"`
		}
		_ = json.Unmarshal(data, &s)
		c.Relay, c.Org, c.Name = s.Cloud.Relay, s.Cloud.Org, s.Cloud.Name
		c.AssistantOff = s.Assistant == "off"
		// muster cloud login stores the token in the OS keychain when it can
		// and records the reference here; logout clears it.
		c.HasToken = s.Cloud.TokenRef != ""
	}
	if data, err := os.ReadFile(filepath.Join(home, ".muster", "tokens", "cloud.json")); err == nil {
		var t struct {
			Tokens map[string]string `json:"tokens"`
		}
		_ = json.Unmarshal(data, &t)
		if t.Tokens["default"] != "" {
			c.HasToken = true
		}
	}
	c.LoggedIn = c.HasToken && c.Org != ""
	return c
}
