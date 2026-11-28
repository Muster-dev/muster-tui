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

// dashboard is the home screen. In a project it shows the services, recent
// activity and a short servers section; with no muster.json it is the
// servers home (or a welcome when there are no servers either).
type dashboard struct {
	env      *Env
	projects []registry.Project // registered projects, shown when there's no project here
	srv      *serversScreen     // the servers list (home without a project)
	sel      int                // selected row: services, then servers
}

func newDashboard(env *Env) *dashboard {
	d := &dashboard{env: env, srv: newServers(env)}
	d.srv.key = "dashboard"
	if env.Project == nil {
		if pf, err := registry.Load(); err == nil {
			d.projects = pf.Projects
		}
	}
	return d
}

func (d *dashboard) Key() string   { return "dashboard" }
func (d *dashboard) Title() string { return "Home" }
func (d *dashboard) Init() tea.Cmd { return nil }
func (d *dashboard) Hint() string  { return "" }

// serversHome: no project here, so the servers are the home screen.
func (d *dashboard) serversHome() bool {
	st := d.env.State
	return st.Project == nil && (len(st.Fleet.Machines) > 0 || !st.FleetLoaded)
}

// Wide: the servers home goes master-detail on wide terminals.
func (d *dashboard) Wide() bool {
	return d.env.State.Project == nil && d.serversHome() && d.srv.Wide()
}

func (d *dashboard) Capturing() bool { return d.env.State.Project == nil && d.srv.Capturing() }

func (d *dashboard) Update(msg tea.Msg) (Screen, tea.Cmd) {
	_, cmd := d.srv.Update(msg)
	return d, cmd
}

// rows are the selectable rows of the project dashboard.
type dashRow struct{ kind, id string }

func (d *dashboard) rows() []dashRow {
	st := d.env.State
	var out []dashRow
	for _, k := range st.ServiceKeys() {
		out = append(out, dashRow{"service", k})
	}
	for _, mc := range st.Fleet.Machines {
		out = append(out, dashRow{"server", mc.Name})
	}
	return out
}

func (d *dashboard) openRow(r dashRow) tea.Cmd {
	if r.kind == "server" {
		return serverAction(d.env, "open", r.id)
	}
	return nav(newServiceScreen(d.env, r.id))
}

func (d *dashboard) HandleKey(k tea.KeyMsg) (Screen, tea.Cmd, bool) {
	st := d.env.State
	if st.Project == nil {
		if d.serversHome() || d.srv.adding != nil {
			_, cmd, ok := d.srv.HandleKey(k)
			return d, cmd, ok
		}
		switch k.String() {
		case "s", "enter":
			return d, runLine("setup"), true
		case "a":
			d.srv.startAdd(nil)
			return d, nil, true
		case "c":
			return d, runLine("cloud login"), true
		case "D":
			return d, runLine("docker"), true
		}
		return d, nil, false
	}
	rows := d.rows()
	var cur dashRow
	if d.sel < len(rows) {
		cur = rows[d.sel]
	}
	switch k.String() {
	case "j", "down":
		d.sel = clamp(d.sel+1, 0, len(rows)-1)
	case "k", "up":
		d.sel = clamp(d.sel-1, 0, len(rows)-1)
	case "enter", "right", "l":
		if cur.id != "" {
			return d, d.openRow(cur), true
		}
	case "d":
		if cur.kind == "server" {
			return d, serverAction(d.env, "deploy", cur.id), true
		}
		return d, runLine("deploy"), true
	case "L":
		if cur.kind == "service" {
			return d, runLine("logs " + cur.id), true
		}
	case "r":
		return d, tea.Batch(refresh("status"), d.env.loadFleetHealth()), true
	case "D":
		return d, runLine("docker"), true
	default:
		return d, nil, false
	}
	return d, nil, true
}

func (d *dashboard) Click(id string, double bool) (Screen, tea.Cmd, bool) {
	if d.env.State.Project == nil {
		if arg, ok := zoneArg(id, "home:"); ok {
			return d, runLine(arg), true
		}
		_, cmd, ok := d.srv.Click(id, double)
		return d, cmd, ok
	}
	if arg, ok := zoneArg(id, "dash:row:"); ok {
		for i, r := range d.rows() {
			if r.kind+":"+r.id == arg {
				if i == d.sel || double {
					d.sel = i
					return d, d.openRow(r), true
				}
				d.sel = i
				return d, nil, true
			}
		}
	}
	return d, nil, false
}

func (d *dashboard) Footer() []KeyHelp {
	st := d.env.State
	if st.Project == nil {
		if d.serversHome() || d.srv.adding != nil {
			return d.srv.Footer()
		}
		return []KeyHelp{{"s", "set up this folder"}, {"a", "add server"}, {"D", "docker"}}
	}
	out := []KeyHelp{{"enter", "open"}, {"d", "deploy"}}
	rows := d.rows()
	if d.sel < len(rows) && rows[d.sel].kind == "service" {
		out = append(out, KeyHelp{"L", "logs"})
	}
	return append(out, KeyHelp{"r", "refresh"})
}

func (d *dashboard) Keys() []KeyHelp {
	if d.env.State.Project == nil {
		if d.serversHome() {
			return d.srv.Keys()
		}
		return []KeyHelp{{"s", "set up this folder"}, {"a", "add a server"}, {"c", "sign in to Fleet Cloud"}, {"D", "Docker on this computer"}}
	}
	return []KeyHelp{{"↑ ↓ / j k", "select a service or server"}, {"enter", "open it"}, {"d", "deploy (servers ask first)"}, {"L", "logs of the selected service"}, {"r", "check health now"}, {"D", "Docker"}}
}

// welcome is the launcher shown with no project and no servers: the
// wordmark, a tagline and the few things worth doing first, centred.
func (d *dashboard) welcome(w, h int) string {
	type opt struct{ id, key, label, desc string }
	opts := []opt{
		{"setup", "s", "Set up this folder", "scan it and create muster.json"},
		{"fleet add", "a", "Add a server", "deploy to it over SSH or Fleet Cloud"},
	}
	if !d.env.State.Cloud.LoggedIn {
		opts = append(opts, opt{"cloud login", "c", "Sign in to Fleet Cloud", "reach servers through the relay"})
	}
	opts = append(opts, opt{"docker", "D", "Docker", "containers, images and disk here"})
	keyW, labelW := 8, 24
	colW := min(w, keyW+labelW+36)
	var lines []string
	lines = append(lines, centerIn(theme.Brand.Render("m u s t e r"), colW), "")
	lines = append(lines, centerIn(theme.Dim.Render("Deploy anything, anywhere — from one place."), colW), "", "")
	for _, o := range opts {
		row := fit(theme.Key.Render(o.key), keyW) + fit(theme.Base.Render(o.label), labelW) + theme.Dim.Render(o.desc)
		lines = append(lines, d.env.mark("home:"+o.id, trunc(row, colW)))
	}
	lines = append(lines, d.env.mark("key:ctrl+k", fit(theme.Key.Render("ctrl+k"), keyW)+fit(theme.Base.Render("Commands"), labelW)+theme.Dim.Render("everything else, by name")))
	if len(d.projects) > 0 {
		lines = append(lines, "", "", theme.Dim.Render("Your projects"))
		for i, p := range d.projects {
			if i == 4 {
				lines = append(lines, theme.Dim.Render(fmt.Sprintf("… %d more · muster projects", len(d.projects)-4)))
				break
			}
			lines = append(lines, fit(p.Name, keyW+labelW-8)+"        "+theme.Dim.Render(trunc(p.Path, colW-keyW-labelW)))
		}
		lines = append(lines, theme.Dim.Render("Open one with ")+code("muster-tui -C <path>"))
	}
	top := max((h-len(lines))*2/5, 0)
	out := make([]string, 0, h)
	for i := 0; i < top; i++ {
		out = append(out, "")
	}
	return block(append(out, column(lines, colW, w)...), w, h)
}

// centerIn centres s in a w-cell line (padding both sides).
func centerIn(s string, w int) string { return fit(center(s, w), w) }

func (d *dashboard) View(w, h int) string {
	st := d.env.State
	now := d.env.Now()
	if st.Project == nil {
		if d.serversHome() || d.srv.adding != nil {
			return d.srv.View(w, h)
		}
		return d.welcome(w, h)
	}
	var lines []string
	add := func(s ...string) { lines = append(lines, s...) }

	right := ""
	switch {
	case st.AuthErr != nil:
		right = theme.Warning.Render("health unavailable")
	case !st.StatusAt.IsZero():
		right = theme.Dim.Render("checked " + ago(st.StatusAt, now))
	case !st.StatusLoaded && d.env.Engine != nil:
		right = theme.AccentFg.Render(d.env.Spin()) + theme.Dim.Render(" checking")
	}
	add(spread(section("Services"), right, w), "")

	keys := st.ServiceKeys()
	rows := d.rows()
	d.sel = clamp(d.sel, 0, max(len(rows)-1, 0))
	nameW := 7
	for _, k := range keys {
		nameW = max(nameW, width(k))
	}
	nameW = min(nameW, 16)
	showVersion := w >= 70
	header := "    " + fit("Service", nameW) + "  " + fit("Health", 12) + "  " + fit("Last deploy", 20)
	if showVersion {
		header += "  Version"
	}
	add(theme.Dim.Render(header))
	for i, k := range keys {
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
		cur := " "
		if i == d.sel {
			cur = theme.AccentFg.Render(theme.GlyphCursor)
		}
		row := cur + " " + glyph + " " + fit(k, nameW) + "  " + fit(health, 12) + "  " + fit(last, 20)
		if showVersion {
			row += "  " + version
		}
		add(d.env.mark("dash:row:service:"+k, fit(row, w)))
	}

	// Servers: a short section; Enter opens one.
	if machines := st.Fleet.Machines; len(machines) > 0 {
		add("", spread(section("Servers"), d.srv.statusLine(), w))
		var names []string
		for _, mc := range machines {
			names = append(names, mc.Name)
		}
		c := serverCols(w-2, names)
		for i, mc := range machines {
			ss := d.env.serverStatus(mc)
			cur := " "
			if len(keys)+i == d.sel {
				cur = theme.AccentFg.Render(theme.GlyphCursor)
			}
			row := cur + "   " + fit(mc.Name, c.name) + "  " + fit(ss.Glyph+" "+ss.Word, c.status) + "  " + fit(theme.Dim.Render(connection(mc)), c.conn)
			if c.svc > 0 && ss.Services != "" {
				row += "  " + ss.Services
			}
			add(d.env.mark("dash:row:server:"+mc.Name, fit(row, w)))
		}
	}

	// Recent activity
	if len(st.History) > 0 && len(lines) < h-4 {
		add("", section("Recent activity"))
		evs := st.History
		n := min(len(evs), max(min(5, h-len(lines)-3), 1))
		for i := len(evs) - 1; i >= len(evs)-n; i-- {
			add(" " + activityLine(evs[i], now, w-1))
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
	// The next useful thing, at the bottom (unless Needs attention says it).
	if len(warns) == 0 && len(lines) < h-1 {
		for len(lines) < h-1 {
			add("")
		}
		add(trunc(stateHint(d.env), w))
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
