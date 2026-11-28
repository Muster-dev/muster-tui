package tui

import (
	"fmt"
	"strings"

	"github.com/Muster-dev/muster-tui/internal/docker"
	"github.com/Muster-dev/muster-tui/internal/theme"
)

// stateHint is the footer's fallback: what's going on and the next useful
// thing to do.
func stateHint(e *Env) string {
	st := e.State
	now := e.Now()
	if e.Engine == nil {
		msg := "The muster CLI isn't on your PATH"
		if e.EngineErr != nil {
			msg = e.EngineErr.Error()
		}
		return theme.Warning.Render(theme.GlyphWarn+" ") + msg + theme.Dim.Render(" — Docker and settings still work")
	}
	if st.Project == nil {
		return theme.Dim.Render("No muster.json here · ") + code("setup") + theme.Dim.Render(" scans this folder")
	}
	if st.AuthErr != nil || e.TokenMissing {
		return theme.Warning.Render(theme.GlyphWarn+" ") + "Not paired with the muster CLI" + theme.Dim.Render(" — run ") + code("muster") + theme.Dim.Render(" once, or ") + code("muster-tui --set-token <token>")
	}
	if n := len(st.HookChanges); n > 0 {
		return theme.Warning.Render(theme.GlyphWarn+" ") + fmt.Sprintf("%s changed since you approved them", plural(n, "hook file")) + theme.Dim.Render(" · ") + code("approve")
	}
	var unhealthy []string
	for _, k := range st.ServiceKeys() {
		if st.Svc(k).Shown() == "unhealthy" {
			unhealthy = append(unhealthy, k)
		}
	}
	if len(unhealthy) > 0 {
		k := unhealthy[0]
		since := ""
		if s := st.Svc(k).Since; !s.IsZero() && now.Sub(s).Minutes() >= 1 {
			since = " for " + dur(now.Sub(s).Truncate(60e9))
		}
		who := k + " is"
		if len(unhealthy) > 1 {
			who = strings.Join(unhealthy, ", ") + " are"
		}
		return theme.Err.Render(theme.GlyphUnhealthy+" ") + who + " unhealthy" + since + theme.Dim.Render(" · ") + code("logs "+k) + theme.Dim.Render(" · ") + code("rollback "+k)
	}
	if st.DockerErr != nil && st.DockerErr != docker.ErrNotInstalled && usesDocker(e) {
		return theme.Warning.Render(theme.GlyphWarn+" ") + "Docker isn't running" + theme.Dim.Render(" — start Docker Desktop (or ") + code("colima start") + theme.Dim.Render(")")
	}
	if st.HistoryLoaded && len(st.History) == 0 {
		return theme.Dim.Render("Nothing deployed yet · ") + code("dry-run") + theme.Dim.Render(" to preview · ") + code("deploy") + theme.Dim.Render(" when ready")
	}
	if st.Doctor != nil && st.Doctor.Failures > 0 {
		return theme.Warning.Render(theme.GlyphWarn+" ") + fmt.Sprintf("Doctor found %s", plural(st.Doctor.Failures, "problem")) + theme.Dim.Render(" · ") + code("doctor")
	}
	h, _, _, _ := st.Counts()
	if st.StatusLoaded && h > 0 && h == len(st.ServiceKeys()) {
		return theme.OK.Render(theme.GlyphOK+" ") + fmt.Sprintf("All %s healthy", plural(h, "service")) + theme.Dim.Render(" · ") + code("deploy") + theme.Dim.Render(" · ") + code("logs <service>")
	}
	return theme.Dim.Render("Try ") + code("deploy") + theme.Dim.Render(", ") + code("logs <service>") + theme.Dim.Render(", ") + code("docker") + theme.Dim.Render(" — ctrl+k lists everything")
}

func usesDocker(e *Env) bool {
	p := e.State.Project
	if p == nil {
		return false
	}
	for _, k := range p.ServiceKeys() {
		switch p.Stack(k) {
		case "compose", "docker":
			return true
		}
	}
	return false
}
