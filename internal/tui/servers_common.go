package tui

import (
	"fmt"
	"strings"

	"github.com/Muster-dev/muster-tui/internal/fleet"
	"github.com/Muster-dev/muster-tui/internal/theme"
)

// serverState is how one server is doing, in plain words.
type serverState struct {
	Glyph    string // coloured status dot (or spinner)
	Word     string // Online, Offline, Checking…, Needs pairing, Not checked
	Services string // "1/1 healthy", "" when unknown
	Detail   string // one more line of explanation, "" when none
	Status   *fleet.MachineStatus
	Pairing  bool // needs `muster fleet pair`
	Offline  bool
}

const (
	srvOnline   = "Online"
	srvOffline  = "Offline"
	srvChecking = "Checking…"
	srvPairing  = "Needs pairing"
	srvUnknown  = "Not checked"
)

// serverStatus folds the fleet list and the last `fleet status` answer into
// what the servers list shows.
func (e *Env) serverStatus(mc fleet.Machine) serverState {
	st := e.State
	hs, ok := st.FleetHealth[mc.Name]
	checking := e.CheckingServers()
	var out serverState
	switch {
	case ok && !hs.Reachable:
		out = serverState{Glyph: theme.Err.Render(theme.GlyphUnhealthy), Word: theme.Err.Render(srvOffline), Offline: true}
		out.Detail = firstNonEmpty(hs.Error, "Can't reach it")
	case ok && hs.Error == "unpaired", !ok && mc.Paired != nil && !*mc.Paired && mc.Mode == "muster":
		out = serverState{Glyph: theme.Warning.Render(theme.GlyphWarn), Word: theme.Warning.Render(srvPairing), Pairing: true}
		out.Detail = "This computer isn't paired with it yet — press p to pair"
	case ok:
		out = serverState{Glyph: theme.OK.Render(theme.GlyphHealthy), Word: theme.OK.Render(srvOnline)}
		if hs.Error != "" {
			out.Detail = hs.Error
		}
	case checking:
		out = serverState{Glyph: theme.AccentFg.Render(e.Spin()), Word: theme.AccentFg.Render(srvChecking)}
	default:
		out = serverState{Glyph: theme.Dim.Render(theme.GlyphUnknown), Word: theme.Dim.Render(srvUnknown)}
		if e.Engine == nil {
			out.Detail = "The muster CLI is needed to check servers"
		}
	}
	if ok {
		h := hs
		out.Status = &h
		if hs.Healthy != nil && hs.Total != nil && *hs.Total > 0 {
			txt := fmt.Sprintf("%d/%d healthy", *hs.Healthy, *hs.Total)
			if *hs.Healthy < *hs.Total {
				out.Services = theme.Warning.Render(txt)
			} else {
				out.Services = theme.OK.Render(txt)
			}
		}
	}
	return out
}

// connection says how muster reaches a server, in plain words.
func connection(mc fleet.Machine) string {
	if mc.Transport == "cloud" {
		return "Fleet Cloud"
	}
	return "SSH · " + mc.Target()
}

// runsHow explains the deploy mode.
func runsHow(mc fleet.Machine) string {
	if mc.Mode == "muster" {
		return "Runs muster on the server"
	}
	return "Hooks sent over SSH from this computer"
}

// canShell reports whether muster can open an SSH session on mc.
func canShell(mc fleet.Machine) bool { return mc.Transport != "cloud" }

// serviceWord turns a remote service status into a word.
func serviceWord(s fleet.ServiceHealth) string {
	switch strings.ToLower(s.Status) {
	case "healthy":
		return theme.OK.Render("healthy")
	case "unhealthy", "down", "failed":
		return theme.Err.Render(s.Status)
	case "":
		return theme.Dim.Render("unknown")
	}
	return theme.Warning.Render(s.Status)
}

func serviceGlyph(s fleet.ServiceHealth) string {
	switch strings.ToLower(s.Status) {
	case "healthy":
		return theme.OK.Render(theme.GlyphHealthy)
	case "unhealthy", "down", "failed":
		return theme.Err.Render(theme.GlyphUnhealthy)
	case "", "disabled", "no_health_check":
		return theme.Dim.Render(theme.GlyphUnknown)
	}
	return theme.Warning.Render(theme.GlyphWarn)
}
