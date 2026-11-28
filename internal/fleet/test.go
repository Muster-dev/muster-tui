package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/Muster-dev/muster-tui/internal/engine"
	"github.com/Muster-dev/muster-tui/internal/proc"
)

// TestServer is one server's connectivity result.
type TestServer struct {
	Name      string `json:"name"`
	Transport string `json:"transport"` // cloud, ssh
	Mode      string `json:"mode"`      // muster, push
	OK        bool   `json:"ok"`
	Connect   string `json:"connect"` // ok, fail
	Muster    string `json:"muster"`  // ok, fail, skip
	LatencyMS int    `json:"latency_ms"`
	Error     string `json:"error"`
	// Pairing: reachable, but this computer has no token for it (text
	// output only; the CLI counts it as neither passed nor failed).
	Pairing bool `json:"-"`
}

// TestReport is `muster fleet test [target] --json`, or what could be read
// from the text output of older CLIs.
type TestReport struct {
	Servers []TestServer `json:"servers"`
	Passed  int          `json:"passed"`
	Failed  int          `json:"failed"`
	// FromText: parsed from the human output (no --json support).
	FromText bool `json:"-"`
}

// ParseTest decodes `muster fleet test --json`.
func ParseTest(data []byte) (TestReport, error) {
	var r TestReport
	if err := json.Unmarshal(data, &r); err != nil {
		return r, fmt.Errorf("fleet test: unexpected output: %w", err)
	}
	if r.Servers == nil {
		return r, fmt.Errorf("fleet test: no servers in the answer")
	}
	return r, nil
}

var (
	boxRe     = regexp.MustCompile(`[─│┌┐└┘├┤┬┴┼╭╮╰╯═║╔╗╚╝]+`)
	summaryRe = regexp.MustCompile(`(?i)all (\d+) machine\(s\) passed|(\d+) passed, (\d+) failed`)
	// "● name    cloud ok, muster ok" once the box is gone.
	testRowRe = regexp.MustCompile(`^\s*[●○*•]\s+(\S+)\s+(cloud ok, muster ok|cloud ok, muster fail|cloud ok|cloud fail|no tunnel|SSH ok, token ok|SSH ok, token fail|SSH ok|SSH fail|unpaired)\s*$`)
)

// CleanCLIText strips colour codes and box drawing from CLI output, so text
// that has no JSON form still reads cleanly inside the TUI.
func CleanCLIText(s string) string {
	s = proc.StripANSI(s)
	s = boxRe.ReplaceAllString(s, " ")
	return strings.TrimRight(s, " \t")
}

// ParseTestText reads the boxed text `muster fleet test` prints (muster
// without --json). Long names are cut with "..." by the CLI, so they are
// matched back to the fleet's machines when possible.
func ParseTestText(out string, machines []Machine) TestReport {
	r := TestReport{FromText: true}
	for _, raw := range strings.Split(out, "\n") {
		// Spinner frames are redrawn with \r; keep what was drawn last.
		raw = strings.TrimRight(raw, "\r")
		if i := strings.LastIndex(raw, "\r"); i >= 0 {
			raw = raw[i+1:]
		}
		line := CleanCLIText(raw)
		if m := summaryRe.FindStringSubmatch(line); m != nil {
			if m[1] != "" {
				r.Passed, _ = strconv.Atoi(m[1])
			} else {
				r.Passed, _ = strconv.Atoi(m[2])
				r.Failed, _ = strconv.Atoi(m[3])
			}
			continue
		}
		m := testRowRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		ts := TestServer{Name: m[1]}
		mc, found := matchMachine(m[1], machines)
		if found {
			ts.Name, ts.Transport, ts.Mode = mc.Name, mc.Transport, mc.Mode
		}
		switch tag := m[2]; tag {
		case "cloud ok, muster ok":
			ts.Transport, ts.OK, ts.Connect, ts.Muster = "cloud", true, "ok", "ok"
		case "cloud ok, muster fail":
			ts.Transport, ts.Connect, ts.Muster = "cloud", "ok", "fail"
			ts.Error = "Connected, but muster didn't answer on the server"
		case "cloud ok":
			ts.Transport, ts.OK, ts.Connect, ts.Muster = "cloud", true, "ok", "skip"
		case "cloud fail":
			ts.Transport, ts.Connect, ts.Muster = "cloud", "fail", "skip"
			ts.Error = "Fleet Cloud couldn't reach the server's agent"
		case "no tunnel":
			ts.Transport, ts.Connect, ts.Muster = "cloud", "fail", "skip"
			ts.Error = "Fleet Cloud isn't set up on this computer — run cloud login"
		case "SSH ok, token ok":
			ts.Transport, ts.OK, ts.Connect, ts.Muster = "ssh", true, "ok", "ok"
		case "SSH ok, token fail":
			ts.Transport, ts.Connect, ts.Muster = "ssh", "ok", "fail"
			ts.Error = "Connected, but the server rejected this computer's token — pair again"
		case "SSH ok":
			ts.Transport, ts.OK, ts.Connect, ts.Muster = "ssh", true, "ok", "skip"
		case "SSH fail":
			ts.Transport, ts.Connect, ts.Muster = "ssh", "fail", "skip"
			ts.Error = "SSH couldn't connect"
		case "unpaired":
			ts.Transport, ts.Connect, ts.Muster, ts.Pairing = "ssh", "ok", "skip", true
			ts.Error = "Connected, but this computer isn't paired with it yet"
		}
		r.Servers = append(r.Servers, ts)
	}
	if r.Passed == 0 && r.Failed == 0 {
		for _, s := range r.Servers {
			if s.OK {
				r.Passed++
			} else if !s.Pairing {
				r.Failed++
			}
		}
	}
	return r
}

func matchMachine(name string, machines []Machine) (Machine, bool) {
	for _, m := range machines {
		if m.Name == name {
			return m, true
		}
	}
	if p, ok := strings.CutSuffix(name, "..."); ok && p != "" {
		var hit Machine
		n := 0
		for _, m := range machines {
			if strings.HasPrefix(m.Name, p) {
				hit, n = m, n+1
			}
		}
		if n == 1 {
			return hit, true
		}
	}
	return Machine{}, false
}

// RunTest runs `muster fleet test [target]`, with --json when the CLI
// supports it, otherwise reading the text output.
func RunTest(ctx context.Context, eng *engine.Engine, target string, useJSON bool, machines []Machine) (TestReport, error) {
	args := []string{"fleet", "test"}
	if target != "" {
		args = append(args, target)
	}
	if useJSON {
		var raw json.RawMessage
		// Exit 1 means "some failed" and still carries the report.
		if err := eng.RunJSONTolerant(ctx, &raw, append(args, "--json")...); err != nil {
			return TestReport{}, err
		}
		return ParseTest(raw)
	}
	out, err := proc.Output(eng.Command(ctx, args...))
	if ctx.Err() != nil {
		return TestReport{}, ctx.Err()
	}
	r := ParseTestText(string(out), machines)
	if len(r.Servers) == 0 {
		if err != nil {
			msg := strings.TrimSpace(CleanCLIText(string(out)))
			if msg == "" {
				msg = err.Error()
			}
			return r, fmt.Errorf("%s", ServerWords(msg))
		}
		return r, fmt.Errorf("muster fleet test printed nothing muster-tui could read")
	}
	return r, nil
}

// RestartResult is `muster fleet restart <server> … --json`.
type RestartResult struct {
	Server  string `json:"server"`
	Results []struct {
		Service string `json:"service"`
		OK      bool   `json:"ok"`
		Error   string `json:"error"`
	} `json:"results"`
}

// Restart runs `muster fleet restart <server> [service...] --yes --json`.
func Restart(ctx context.Context, eng *engine.Engine, server string, services ...string) (RestartResult, error) {
	var r RestartResult
	args := append([]string{"fleet", "restart", server}, services...)
	err := eng.RunJSONTolerant(ctx, &r, append(args, "--yes", "--json")...)
	return r, err
}

// Logs streams `muster fleet logs <server> <service> --tail N --json` (the
// server's own `muster logs --json` events, passed through).
func Logs(ctx context.Context, eng *engine.Engine, server, service string, tail int) (*engine.DeployStream, error) {
	return eng.Stream(ctx, "fleet", "logs", server, service, "--tail", strconv.Itoa(tail), "--json")
}

var machineRe = regexp.MustCompile(`\b([Mm])achine(\(s\)|s\b|\b)`)

// ServerWords rewrites the CLI's "machine" wording to "server", which is
// what the TUI calls them ("All 2 machine(s) passed" → "All 2 servers
// passed").
func ServerWords(s string) string {
	s = regexp.MustCompile(`\b1 machine\(s\)`).ReplaceAllString(s, "1 server")
	return machineRe.ReplaceAllStringFunc(s, func(m string) string {
		out := "server"
		if strings.HasSuffix(m, "(s)") || strings.HasSuffix(m, "s") {
			out = "servers"
		}
		if m[0] == 'M' {
			out = "S" + out[1:]
		}
		return out
	})
}
