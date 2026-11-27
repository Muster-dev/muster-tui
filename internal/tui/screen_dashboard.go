package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/Muster-dev/muster-tui/internal/engine"
	"github.com/Muster-dev/muster-tui/internal/registry"
	"github.com/Muster-dev/muster-tui/internal/theme"
	tea "github.com/charmbracelet/bubbletea"
)

// dashboard is the default screen: health, last deploys, activity, warnings.
type dashboard struct {
	env      *Env
	projects []registry.Project // registered projects, shown when there's no project here
}

func newDashboard(env *Env) *dashboard {
	d := &dashboard{env: env}
	if env.Project == nil {
		if pf, err := registry.Load(); err == nil {
			d.projects = pf.Projects
		}
	}
	return d
}

func (d *dashboard) Key() string                                    { return "dashboard" }
func (d *dashboard) Title() string                                  { return "Overview" }
func (d *dashboard) Init() tea.Cmd                                  { return nil }
func (d *dashboard) Update(tea.Msg) (Screen, tea.Cmd)               { return d, nil }
func (d *dashboard) Hint() string                                   { return "" }
func (d *dashboard) Keys() []KeyHelp                                { return nil }
func (d *dashboard) HandleKey(k tea.KeyMsg) (Screen, tea.Cmd, bool) { return d, nil, false }

func (d *dashboard) View(w, h int) string {
	st := d.env.State
	now := d.env.Now()
	var lines []string
	add := func(s ...string) { lines = append(lines, s...) }

	if st.Project == nil {
		add(section("Welcome to muster"), "")
		add(wrap("There's no muster.json in this folder yet. muster can scan it, find your services and set up deploys in under a minute.", w)...)
		add("", "  "+code("setup")+theme.Dim.Render("     scan this folder"))
		add("  " + code("docker") + theme.Dim.Render("    containers, images and disk — works anywhere"))
		add("  " + code("servers") + theme.Dim.Render("   your fleet machines"))
		if len(d.projects) > 0 {
			add("", section("Your projects"))
			for i, p := range d.projects {
				if i == 6 {
					add(theme.Dim.Render(fmt.Sprintf("  … %d more · muster projects", len(d.projects)-6)))
					break
				}
				add("  " + fit(p.Name, 18) + " " + theme.Dim.Render(p.Path))
			}
			add(theme.Dim.Render("  Open one with ") + code("muster-tui -C <path>"))
		}
		return block(lines, w, h)
	}

	right := ""
	switch {
	case st.AuthErr != nil:
		right = theme.Warning.Render("health unavailable")
	case !st.StatusAt.IsZero():
		right = theme.Dim.Render("checked " + ago(st.StatusAt, now))
	case !st.StatusLoaded && d.env.Engine != nil:
		right = theme.AccentFg.Render(d.env.Spin()) + theme.Dim.Render(" checking")
	}
	add(spread(section(st.Project.Name()), right, w), "")

	keys := st.ServiceKeys()
	nameW := 7
	for _, k := range keys {
		nameW = max(nameW, width(k))
	}
	nameW = min(nameW, 16)
	showVersion := w >= 70
	header := "  " + fit("SERVICE", nameW) + "  " + fit("HEALTH", 12) + "  " + fit("LAST DEPLOY", 20)
	if showVersion {
		header += "  VERSION"
	}
	add(theme.Dim.Render(header))
	for _, k := range keys {
		ss := st.Svc(k)
		glyph := healthGlyph(ss.Shown())
		health := healthWord(ss.Shown())
		if ss.Deploying {
			glyph = theme.AccentFg.Render(d.env.Spin())
			health = theme.AccentFg.Render("deploying")
		} else if ss.LockedBy != nil {
			glyph = theme.Warning.Render(theme.GlyphWarn)
			health = theme.Warning.Render("deploying")
		}
		last := theme.Dim.Render("never deployed")
		version := theme.Dim.Render("—")
		if ev := ss.LastDeploy; ev != nil {
			when := ago(ev.Time(), now)
			verb := ev.Action
			if verb == "deploy" {
				verb = "deployed"
			} else if verb == "rollback" {
				verb = "rolled back"
			}
			if ev.OK() {
				last = verb + theme.Dim.Render(" "+when)
			} else {
				last = theme.Err.Render("failed") + theme.Dim.Render(" "+when)
			}
			if v := firstNonEmpty(ev.Version, ev.Image, shortSHA(ev.Commit)); v != "" {
				version = v
			}
		}
		row := glyph + " " + fit(k, nameW) + "  " + fit(health, 12) + "  " + fit(last, 20)
		if showVersion {
			row += "  " + version
		}
		add(row)
	}

	// Recent activity
	if len(st.History) > 0 && len(lines) < h-3 {
		add("", section("Recent activity"))
		evs := st.History
		n := min(len(evs), 5)
		for i := len(evs) - 1; i >= len(evs)-n; i-- {
			add(activityLine(evs[i], now, w))
		}
	}

	// Warnings
	warns := d.warnings()
	if len(warns) > 0 && len(lines) < h-2 {
		add("", section("Needs attention"))
		for _, wl := range warns {
			add(theme.Warning.Render(theme.GlyphWarn) + " " + trunc(wl, w-2))
		}
	}
	return block(lines, w, h)
}

func (d *dashboard) warnings() []string {
	st := d.env.State
	var out []string
	if l := st.DeployLock; l != nil && !d.localDeploy() {
		msg := "A deploy is running — started by " + l.Who()
		if l.Started != "" {
			msg += " at " + l.Started
		}
		if l.Services != "" {
			msg += " (" + l.Services + ")"
		}
		out = append(out, msg)
	} else if !d.localDeploy() {
		for _, k := range st.ServiceKeys() {
			if ss := st.Svc(k); ss.LockedBy != nil {
				out = append(out, k+" is being deployed by "+ss.LockedBy.Who())
			}
		}
	}
	if n := len(st.HookChanges); n > 0 {
		out = append(out, fmt.Sprintf("%s changed since approval — %s", plural(n, "hook file"), "approve"))
	}
	if st.DockerErr != nil && usesDocker(d.env) {
		out = append(out, "Docker isn't running — compose services can't deploy")
	}
	if st.Doctor != nil {
		for _, c := range st.Doctor.Checks {
			if c.Status == "fail" {
				out = append(out, c.Name)
			}
		}
		for _, c := range st.Doctor.Checks {
			if c.Status == "warn" && !strings.Contains(strings.ToLower(c.Name), "docker") {
				out = append(out, c.Name)
			}
		}
	}
	if len(out) > 4 {
		out = append(out[:3], fmt.Sprintf("%d more — doctor", len(out)-3))
	}
	return out
}

// localDeploy reports whether this TUI is the one deploying (the lock is ours).
func (d *dashboard) localDeploy() bool {
	for _, k := range d.env.State.ServiceKeys() {
		if d.env.State.Svc(k).Deploying {
			return true
		}
	}
	return false
}

func activityLine(ev engine.HistoryEvent, now time.Time, w int) string {
	t := ev.Time()
	when := t.Format("15:04")
	if t.YearDay() != now.YearDay() || t.Year() != now.Year() {
		when = t.Format("Jan 2")
	}
	status := theme.OK.Render(theme.GlyphOK + " ok")
	if !ev.OK() {
		status = theme.Err.Render(theme.GlyphFail + " " + firstNonEmpty(ev.Status, "failed"))
	}
	line := theme.Dim.Render(fit(when, 6)) + "  " + fit(ev.Service, 10) + " " + fit(ev.Action, 9) + " " + fit(status, 10)
	if c := shortSHA(ev.Commit); c != "" {
		line += "  " + theme.Dim.Render(c)
	}
	return trunc(line, w)
}

func shortSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}
