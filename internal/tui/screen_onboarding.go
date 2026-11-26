package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/Muster-dev/muster-tui/internal/config"
	"github.com/Muster-dev/muster-tui/internal/theme"
	tea "github.com/charmbracelet/bubbletea"
)

type obPhase int

const (
	obIntro obPhase = iota
	obScanning
	obReview
	obCustomize
	obCreating
	obCreated
	obFailed
)

// onboarding: explain, scan, review, customise, create, offer a dry run.
type onboarding struct {
	env    *Env
	dir    string
	phase  obPhase
	plan   config.Plan
	err    error
	btn    int // review buttons: Create, Customize, Cancel
	row    int
	col    int
	edit   bool
	input  lineInput
	out    []string
	taskID int
}

type scanDoneMsg struct {
	plan config.Plan
	err  error
}

var obButtons = []string{"Create", "Customize", "Cancel"}

// customize columns
const (
	colInclude = iota
	colHealth
	colPort
	colEndpoint
	colCreds
	colCount
)

func newOnboarding(env *Env, dir string) *onboarding {
	ti := newLineInput()
	ti.CharLimit = 200
	ti.Width = 21
	return &onboarding{env: env, dir: dir, input: ti}
}

func (o *onboarding) Key() string     { return "onboarding" }
func (o *onboarding) Title() string   { return "Set up" }
func (o *onboarding) Init() tea.Cmd   { return nil }
func (o *onboarding) Capturing() bool { return o.edit }
func (o *onboarding) Close()          { o.env.Tasks.Cancel(o.taskID) }

func (o *onboarding) scan() tea.Cmd {
	o.phase = obScanning
	dir := o.dir
	id, cmd := o.env.Tasks.Run("scanning "+shortPath(dir), o.Key(), true, func(ctx context.Context) tea.Msg {
		p, err := config.Scan(dir)
		return scanDoneMsg{p, err}
	})
	o.taskID = id
	return cmd
}

func (o *onboarding) create() tea.Cmd {
	if o.env.Engine == nil {
		o.phase, o.err = obFailed, fmt.Errorf("the muster CLI isn't installed — it creates the hooks")
		return nil
	}
	if !o.plan.Fallback && len(o.plan.Included()) == 0 {
		return toast(lvlWarn, "Pick at least one service")
	}
	o.phase = obCreating
	o.out = nil
	eng := o.env.Engine.WithDir(o.dir)
	args := o.plan.SetupArgs()
	id, cmd := o.env.Tasks.Stream("creating muster.json", o.Key(), true, func(ctx context.Context) (<-chan tea.Msg, error) {
		lines, wait, err := rawLines(ctx, eng.Command(ctx, args...))
		if err != nil {
			return nil, err
		}
		out := make(chan tea.Msg, 8)
		go func() {
			defer close(out)
			for l := range lines {
				out <- logBatchMsg{[]string{l}}
			}
			out <- logEndMsg{wait()}
		}()
		return out, nil
	})
	o.taskID = id
	return cmd
}

func (o *onboarding) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch m := msg.(type) {
	case TaskMsg:
		if m.ID != o.taskID {
			return o, nil
		}
		switch in := m.Msg.(type) {
		case scanDoneMsg:
			o.plan, o.err = in.plan, in.err
			if in.err != nil {
				o.phase = obFailed
			} else {
				o.phase, o.btn = obReview, 0
			}
		case logBatchMsg:
			o.out = append(o.out, in.lines...)
		case logEndMsg:
			if in.err != nil {
				o.phase, o.err = obFailed, in.err
			} else if config.FindIn(o.dir) == "" {
				o.phase, o.err = obFailed, fmt.Errorf("setup finished but no muster.json was written")
			} else {
				o.phase = obCreated
				return o, func() tea.Msg { return reloadConfigMsg{} }
			}
		case taskErrMsg:
			o.phase, o.err = obFailed, in.Err
		}
	case TaskEndedMsg:
		if m.ID == o.taskID && m.Canceled {
			if o.phase == obScanning || o.phase == obCreating {
				o.phase = obIntro
			}
		}
	}
	return o, nil
}

func (o *onboarding) HandleKey(k tea.KeyMsg) (Screen, tea.Cmd, bool) {
	key := k.String()
	switch o.phase {
	case obIntro, obFailed:
		switch key {
		case "enter":
			return o, o.scan(), true
		case "esc":
			return o, func() tea.Msg { return backMsg{} }, true
		}
	case obReview:
		switch key {
		case "left", "h", "shift+tab":
			o.btn = (o.btn + len(obButtons) - 1) % len(obButtons)
		case "right", "l":
			o.btn = (o.btn + 1) % len(obButtons)
		case "c":
			return o, o.create(), true
		case "e":
			o.phase, o.row, o.col = obCustomize, 0, colHealth
		case "enter":
			switch o.btn {
			case 0:
				return o, o.create(), true
			case 1:
				if o.plan.Fallback {
					return o, toast(lvlInfo, "muster's own scanner will pick services — customise them in settings afterwards"), true
				}
				o.phase, o.row, o.col = obCustomize, 0, colHealth
			case 2:
				return o, func() tea.Msg { return backMsg{} }, true
			}
		case "esc", "q":
			return o, func() tea.Msg { return backMsg{} }, true
		case "r":
			return o, o.scan(), true
		default:
			return o, nil, false
		}
		return o, nil, true
	case obCustomize:
		return o.customizeKey(k)
	case obCreated:
		switch key {
		case "enter":
			return o, runLine("dry-run"), true
		case "d":
			return o, runLine("deploy"), true
		case "esc":
			return o, runLine("dashboard"), true
		}
	case obCreating, obScanning:
		if key == "esc" {
			o.env.Tasks.Cancel(o.taskID)
			return o, nil, true
		}
	}
	return o, nil, false
}

func (o *onboarding) customizeKey(k tea.KeyMsg) (Screen, tea.Cmd, bool) {
	svcs := o.plan.Services
	if len(svcs) == 0 {
		o.phase = obReview
		return o, nil, true
	}
	s := &o.plan.Services[o.row]
	if o.edit {
		switch k.String() {
		case "enter":
			val := strings.TrimSpace(o.input.Value())
			switch o.col {
			case colPort:
				n, err := strconv.Atoi(val)
				if err != nil || n < 1 || n > 65535 {
					return o, toast(lvlErr, "Port must be a number from 1 to 65535"), true
				}
				s.Health.Port = n
			case colEndpoint:
				if s.Health.Type == "command" {
					s.Health.Command = val
				} else {
					if val != "" && !strings.HasPrefix(val, "/") {
						val = "/" + val
					}
					s.Health.Endpoint = val
				}
			}
			o.edit = false
		case "esc":
			o.edit = false
		default:
			var cmd tea.Cmd
			o.input, cmd = o.input.Update(k)
			return o, cmd, true
		}
		return o, nil, true
	}
	switch k.String() {
	case "up", "k":
		o.row = clamp(o.row-1, 0, len(svcs)-1)
	case "down", "j":
		o.row = clamp(o.row+1, 0, len(svcs)-1)
	case "left", "h":
		o.col = clamp(o.col-1, 0, colCount-1)
	case "right", "l", "tab":
		o.col = clamp(o.col+1, 0, colCount-1)
	case "K", "shift+up":
		if o.row > 0 {
			svcs[o.row], svcs[o.row-1] = svcs[o.row-1], svcs[o.row]
			o.row--
		}
	case "J", "shift+down":
		if o.row < len(svcs)-1 {
			svcs[o.row], svcs[o.row+1] = svcs[o.row+1], svcs[o.row]
			o.row++
		}
	case " ", "enter":
		switch o.col {
		case colInclude:
			s.Include = !s.Include
		case colHealth:
			cycleHealth(&s.Health)
		case colCreds:
			s.Creds = cycle(config.CredModes, s.Creds)
		case colPort, colEndpoint:
			if !s.Health.Enabled || (o.col == colPort && s.Health.Type == "command") {
				return o, toast(lvlInfo, "Pick a health check type first"), true
			}
			o.edit = true
			v := s.Health.Endpoint
			if o.col == colPort {
				v = strconv.Itoa(s.Health.Port)
			} else if s.Health.Type == "command" {
				v = s.Health.Command
			}
			o.input.SetValue(v)
			o.input.CursorEnd()
			o.input.Focus()
		}
	case "esc", "ctrl+s", "d":
		o.phase, o.btn = obReview, 0
	case "c":
		return o, o.create(), true
	default:
		return o, nil, false
	}
	return o, nil, true
}

func cycleHealth(h *config.Health) {
	switch {
	case !h.Enabled:
		h.Enabled, h.Type = true, "http"
		if h.Endpoint == "" {
			h.Endpoint = "/"
		}
	case h.Type == "http":
		h.Type = "tcp"
	case h.Type == "tcp":
		h.Type = "command"
	default:
		h.Enabled, h.Type = false, ""
	}
	if h.Timeout == 0 {
		h.Timeout = 10
	}
}

func cycle(opts []string, cur string) string {
	for i, o := range opts {
		if o == cur {
			return opts[(i+1)%len(opts)]
		}
	}
	return opts[0]
}

func plannedHealth(s config.PlannedService) string {
	h := s.Health
	if !h.Enabled {
		return "none"
	}
	switch h.Type {
	case "http":
		return fmt.Sprintf("http :%d %s", h.Port, firstNonEmpty(h.Endpoint, "/"))
	case "tcp":
		return fmt.Sprintf("tcp :%d", h.Port)
	case "command":
		return "command " + h.Command
	}
	return h.Type
}

func kindOf(s config.PlannedService) string {
	switch {
	case s.Infra:
		return "infra"
	case s.Build != "":
		return "build"
	case s.Image != "":
		return "image"
	}
	return "app"
}

func (o *onboarding) View(w, h int) string {
	var lines []string
	add := func(s ...string) { lines = append(lines, s...) }
	name := shortPath(o.dir)
	switch o.phase {
	case obIntro:
		add(section("Set up "+name), "")
		add(wrap("There's no muster.json here yet. muster will look at this folder — compose files, Dockerfiles, Kubernetes manifests — and propose services, health checks and a deploy order. Nothing is written until you say so.", w)...)
		add("", keyHint("Enter", "scan this folder", "Esc", "not now"))
	case obScanning:
		add(section("Set up "+name), "", theme.AccentFg.Render(o.env.Spin())+" Scanning "+name+"…")
	case obFailed:
		add(section("Set up "+name), "", theme.Err.Render(theme.GlyphFail+" ")+trunc(errText(o.err), w-2))
		for _, l := range tail(o.out, h-8) {
			add(theme.Dim.Render("  " + trunc(l, w-2)))
		}
		add("", keyHint("Enter", "scan again", "Esc", "back"))
	case obReview, obCustomize:
		add(o.reviewLines(w)...)
	case obCreating:
		add(section("Set up "+name), "", theme.AccentFg.Render(o.env.Spin())+" Creating muster.json and hooks…", "")
		for _, l := range tail(o.out, h-5) {
			add(theme.Dim.Render("  " + trunc(l, w-2)))
		}
	case obCreated:
		n := 0
		if o.env.State.Project != nil {
			n = len(o.env.State.Project.ServiceKeys())
		}
		add(section("Set up "+name), "", theme.OK.Render(theme.GlyphOK+" Created muster.json")+theme.Dim.Render(fmt.Sprintf(" with %s and hooks in .muster/hooks/", plural(n, "service"))), "")
		add("Deploy now? A dry run shows exactly what will happen first.", "")
		add(keyHint("Enter", "dry run", "d", "deploy for real", "Esc", "later"))
	}
	return block(lines, w, h)
}

func (o *onboarding) reviewLines(w int) []string {
	p := o.plan
	var lines []string
	found := strings.Join(p.Found, ", ")
	right := theme.Dim.Render(strings.TrimSpace(p.Stack + " · " + found))
	if p.Stack == "" {
		right = theme.Dim.Render(found)
	}
	lines = append(lines, spread(section("Set up "+p.Name), right, w), "")
	if p.Fallback {
		msg := "Nothing muster-tui recognises directly (no compose file, Dockerfile or k8s manifests)."
		if len(p.Found) > 0 {
			msg = "Found " + found + "."
		}
		lines = append(lines, wrap(msg+" muster's own scanner will detect services and dev commands when you create.", w)...)
		lines = append(lines, "", o.buttons())
		return lines
	}
	inc := len(p.Included())
	lines = append(lines, wrap(fmt.Sprintf("Found %s. Health checks and order are a best guess — change anything.", plural(len(p.Services), "service")), w)...)
	lines = append(lines, "")
	nameW := 8
	for _, s := range p.Services {
		nameW = max(nameW, width(s.Key))
	}
	nameW = min(nameW, 16)
	showCreds := w >= 64
	hdr := "    " + fit("#", 3) + fit("SERVICE", nameW+2) + fit("KIND", 7) + fit("HEALTH CHECK", 22)
	if showCreds {
		hdr += "CREDENTIALS"
	}
	lines = append(lines, theme.Dim.Render(hdr))
	n := 0
	for i, s := range p.Services {
		num := theme.Dim.Render("–")
		if s.Include {
			n++
			num = fmt.Sprint(n)
		}
		check := theme.OK.Render("✓")
		if !s.Include {
			check = theme.Dim.Render("·")
		}
		cells := []string{check, fit(s.Key, nameW), kindOf(s), plannedHealth(s), s.Creds}
		cust := o.phase == obCustomize && i == o.row
		if cust {
			hl := func(col int, v string, wd int) string {
				if col == o.col {
					if o.edit {
						return theme.AccentFg.Render(fit(o.input.View(), wd))
					}
					return theme.Selected.Render(fit(v, wd))
				}
				return fit(v, wd)
			}
			healthCell := plannedHealth(s)
			switch o.col {
			case colPort, colEndpoint:
				healthCell = hl(o.col, plannedHealth(s), 21)
			case colHealth:
				healthCell = hl(colHealth, plannedHealth(s), 21)
			}
			row := theme.AccentFg.Render(theme.GlyphCursor) + " " + hl(colInclude, cells[0], 1) + " " + fit(num, 3) + fit(s.Key, nameW+2) + fit(kindOf(s), 7) + fit(healthCell, 22)
			if showCreds {
				row += hl(colCreds, s.Creds, 9)
			}
			lines = append(lines, row)
			continue
		}
		row := "  " + check + " " + fit(num, 3) + fit(s.Key, nameW+2) + theme.Dim.Render(fit(kindOf(s), 7)) + fit(plannedHealth(s), 22)
		if showCreds {
			row += theme.Dim.Render(s.Creds)
		}
		if !s.Include {
			row = theme.Dim.Render(fmt.Sprintf("  · –  %s skipped", fit(s.Key, nameW)))
		}
		lines = append(lines, row)
	}
	lines = append(lines, "")
	for _, l := range wrap(fmt.Sprintf("Deploys %s in this order. Nothing is written until you create.", plural(inc, "service")), w) {
		lines = append(lines, theme.Dim.Render(l))
	}
	lines = append(lines, "")
	if o.phase == obCustomize {
		col := []string{"include", "health check type", "port", "endpoint / command", "credentials"}[o.col]
		lines = append(lines, theme.Dim.Render("Editing ")+theme.AccentFg.Render(col)+theme.Dim.Render(" · ")+keyHint("←→", "field", "Space", "change", "J/K", "move", "Esc", "done"))
	} else {
		lines = append(lines, o.buttons())
	}
	return lines
}

func (o *onboarding) buttons() string {
	var b []string
	for i, label := range obButtons {
		b = append(b, button(label, i == o.btn))
	}
	return strings.Join(b, " ")
}

func (o *onboarding) Hint() string {
	switch o.phase {
	case obReview:
		return keyHint("Enter", "choose", "c", "create", "e", "customize", "Esc", "cancel")
	case obScanning, obCreating:
		return theme.AccentFg.Render(o.env.Spin()) + " Working — " + keyHint("Esc", "cancel")
	}
	return ""
}

func (o *onboarding) Keys() []KeyHelp {
	return []KeyHelp{{"Enter", "scan / choose"}, {"c", "create"}, {"e", "customize"}, {"←→ ↑↓", "move (customize)"}, {"Space", "toggle / cycle"}, {"J / K", "reorder"}, {"Esc", "back"}}
}

func errText(err error) string {
	if err == nil {
		return "something went wrong"
	}
	return err.Error()
}
