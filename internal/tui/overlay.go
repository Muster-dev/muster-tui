package tui

import (
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/Muster-dev/muster-tui/internal/prompt"
	"github.com/Muster-dev/muster-tui/internal/theme"
	tea "github.com/charmbracelet/bubbletea"
)

// overlay is a popup drawn in a centred box over the dimmed screen.
type overlay interface {
	HandleKey(k tea.KeyMsg) (overlay, tea.Cmd) // nil overlay = close
	// Size is the box's inner size for a body of bw×bh cells.
	Size(bw, bh int) (w, h int)
	View(w, h int) string
	Title() string
	Hint() string
}

// overlayClicker overlays react to clicks on regions they marked ("ov:…").
type overlayClicker interface {
	Click(id string) (overlay, tea.Cmd)
}

// overlayWheel overlays scroll with the mouse wheel.
type overlayWheel interface {
	Wheel(delta int) overlay
}

// ── Confirm ──

type confirm struct {
	env *Env
	msg confirmMsg
	yes bool // Enter picks Yes (the default)
}

func newConfirm(env *Env, m confirmMsg) *confirm { return &confirm{env: env, msg: m, yes: true} }

func (c *confirm) Title() string { return "" }

func (c *confirm) HandleKey(k tea.KeyMsg) (overlay, tea.Cmd) {
	switch k.String() {
	case "enter":
		if c.yes {
			return nil, c.msg.onYes
		}
		return nil, nil
	case "y":
		return nil, c.msg.onYes
	case "left", "right", "tab", "shift+tab", "h", "l":
		c.yes = !c.yes
	case "esc", "n", "q":
		return nil, nil
	}
	return c, nil
}

func (c *confirm) Click(id string) (overlay, tea.Cmd) {
	switch id {
	case "ov:yes":
		return nil, c.msg.onYes
	case "ov:no":
		return nil, nil
	}
	return c, nil
}

func (c *confirm) lines(w int) []string {
	lines := []string{theme.Title.Render(c.msg.title), ""}
	for _, b := range c.msg.body {
		for _, l := range wrap(b, w) {
			lines = append(lines, theme.Dim.Render(l))
		}
	}
	yes := firstNonEmpty(c.msg.yes, "Confirm")
	return append(lines, "", c.env.mark("ov:yes", button(yes, c.yes))+"  "+c.env.mark("ov:no", button("Cancel", !c.yes)))
}

func (c *confirm) Size(bw, bh int) (int, int) {
	w := min(60, bw-8)
	return w, min(len(c.lines(w)), bh-2)
}

func (c *confirm) View(w, h int) string { return block(c.lines(w), w, h) }

func (c *confirm) Hint() string {
	return keyHint("enter", strings.ToLower(firstNonEmpty(c.msg.yes, "confirm")), "esc", "cancel")
}

// ── Command palette (ctrl+k, / or :) ──

type palette struct {
	env    *Env
	hist   *prompt.History
	input  lineInput
	items  []paletteItem
	shown  []paletteItem
	sel    int
	top    int
	screen Screen
	rows   int // visible rows at the last draw

	errMsg, suggestion string
}

type paletteItem struct {
	label, desc, keys, group string
	line                     string  // command line to run
	cmd                      tea.Cmd // runs instead of line when set
	replay                   string  // screen key to replay
	needsArg                 bool    // fill the input with line instead of running it
	header                   bool    // group heading row (not selectable)
	score                    int
}

func newPalette(env *Env, active Screen, hist *prompt.History, text string) *palette {
	ti := newLineInput()
	ti.Placeholder = "Search actions, servers and services…"
	ti.Focus()
	p := &palette{env: env, hist: hist, input: ti, screen: active}
	p.items = paletteItems(env, active, hist)
	if text != "" {
		p.input.SetValue(text)
		p.input.CursorEnd()
	}
	p.filter()
	return p
}

func (p *palette) Title() string { return "Commands" }

// paletteItems is everything the palette can run, in display order.
func paletteItems(env *Env, active Screen, hist *prompt.History) []paletteItem {
	st := env.State
	var items []paletteItem
	add := func(it paletteItem) { items = append(items, it) }
	seen := map[string]bool{}

	if hist != nil {
		h := hist.Items()
		for i := len(h) - 1; i >= 0 && len(seen) < 3; i-- {
			if seen[h[i]] {
				continue
			}
			seen[h[i]] = true
			add(paletteItem{group: "Recent", label: h[i], desc: "run again", line: h[i]})
		}
	}

	proj := st.Project != nil
	type act struct {
		label, desc, line, keys string
		needsArg, project       bool
	}
	one := len(st.ServiceKeys()) == 1
	acts := []act{
		{"Deploy", "Deploy every service in this project", "deploy", "", false, true},
		{"Dry run", "Show the deploy plan without changing anything", "dry-run", "", false, true},
		{"Logs", "Follow a service's logs", "logs", "", !one, true},
		{"Status", "Check every service's health now", "status", "", false, true},
		{"Rollback", "Put a service back on its previous version", "rollback", "", !one, true},
		{"Restart", "Restart a service", "restart", "", !one, true},
		{"Explain last failure", "Why the last deploy failed, and the fix", "explain", "", false, true},
		{"Doctor", "Diagnose this project", "doctor", "", false, true},
		{"History", "Deploys and rollbacks over time", "history", "", false, true},
		{"Settings", "Project and global settings", "settings", "", false, false},
		{"Set up this folder", "Scan it and create muster.json", "setup", "", false, false},
		{"Servers", "Every server and how it's doing", "servers", "", false, false},
		{"Add server", "Connect a new server over SSH or Fleet Cloud", "fleet add", "", false, false},
		{"Pair server", "Let a server that runs muster trust this computer", "pair", "", true, false},
		{"Ask assistant", "Ask the Muster Assistant (Fleet Cloud)", "ask", "", false, true},
		{"Docker", "Containers, images and disk on this computer", "docker", "", false, false},
		{"Help", "Keys, mouse and commands", "help", "?", false, false},
		{"Quit", "Leave muster", "quit", "ctrl+c", false, false},
	}
	if st.Cloud.LoggedIn {
		acts = append(acts, act{"Fleet Cloud status", "Account and relay", "cloud status", "", false, false})
	} else {
		acts = append(acts, act{"Sign in to Fleet Cloud", "Opens your browser", "cloud login", "", false, false})
	}
	for _, a := range acts {
		if a.project && !proj {
			continue
		}
		if a.line == "setup" && proj {
			continue
		}
		if a.line == "pair" && len(st.Fleet.Machines) == 0 {
			continue
		}
		seen[a.line] = true
		add(paletteItem{group: "Actions", label: a.label, desc: a.desc, line: a.line, keys: a.keys, needsArg: a.needsArg})
	}

	for _, mc := range st.Fleet.Machines {
		ss := env.serverStatus(mc)
		desc := connection(mc)
		if w := plainText(ss.Word); w != "" {
			desc = w + " · " + desc
		}
		add(paletteItem{group: "Servers", label: "Go to " + mc.Name, desc: desc, line: "server " + mc.Name})
	}
	if proj {
		for _, k := range st.ServiceKeys() {
			add(paletteItem{group: "Services", label: "Open " + k, desc: "health, config and recent deploys", line: "open " + k})
		}
	}

	// What the screen you're on can do.
	if active != nil {
		for _, k := range active.Keys() {
			if !replayable(k.Key) {
				continue
			}
			add(paletteItem{group: "This screen", label: capitalize(k.Desc), keys: k.Key, replay: k.Key})
		}
	}

	// Combos only show up when you search ("deploy byr…").
	for _, mc := range st.Fleet.Machines {
		n := mc.Name
		add(paletteItem{group: "Server actions", label: "Deploy " + n, desc: "muster fleet deploy " + n, cmd: serverAction(env, "deploy", n)})
		add(paletteItem{group: "Server actions", label: "Status " + n, desc: "muster fleet status " + n, line: "fleet status " + n})
		add(paletteItem{group: "Server actions", label: "Test " + n, desc: "check the connection", line: "fleet test " + n})
		add(paletteItem{group: "Server actions", label: "Rollback " + n, desc: "muster fleet rollback " + n, cmd: serverAction(env, "rollback", n)})
		if canShell(mc) {
			add(paletteItem{group: "Server actions", label: "Shell on " + n, desc: "ssh " + mc.Target(), line: "ssh " + n})
		}
		if env.serverStatus(mc).Pairing {
			add(paletteItem{group: "Server actions", label: "Pair " + n, desc: "store a token from the server", line: "pair " + n})
		}
	}
	if proj {
		for _, k := range st.ServiceKeys() {
			add(paletteItem{group: "Service actions", label: "Deploy " + k, desc: "deploy only " + k, line: "deploy " + k})
			add(paletteItem{group: "Service actions", label: "Logs " + k, desc: "follow its logs", line: "logs " + k})
			add(paletteItem{group: "Service actions", label: "Restart " + k, line: "restart " + k})
			add(paletteItem{group: "Service actions", label: "Rollback " + k, line: "rollback " + k})
		}
	}

	// Every other command, as typed.
	for _, c := range prompt.Commands {
		if c.Hidden || seen[c.Name] || (!proj && projectOnly[c.Name]) {
			continue
		}
		it := paletteItem{group: "Commands", label: c.Name, desc: c.Summary, line: c.Name}
		switch c.Args {
		case prompt.ArgMachine, prompt.ArgContainer:
			it.needsArg = true
		case prompt.ArgService:
			it.needsArg = c.Name == "edit" && !one
		}
		add(it)
	}
	return items
}

// projectOnly commands need a muster.json; the palette hides them elsewhere.
var projectOnly = map[string]bool{
	"deploy": true, "dry-run": true, "rollback": true, "restart": true, "logs": true,
	"status": true, "open": true, "history": true, "explain": true, "ask": true,
	"doctor": true, "approve": true, "edit": true, "cleanup": true,
}

// searchOnly groups appear only once you type.
func searchOnly(group string) bool {
	return group == "Server actions" || group == "Service actions" || group == "Commands"
}

func replayable(k string) bool {
	if k == "enter" {
		return true
	}
	return len([]rune(k)) == 1 && k != " "
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	return strings.ToUpper(string(r[0])) + string(r[1:])
}

func plainText(s string) string { return strings.TrimSpace(stripANSI(s)) }

func (p *palette) query() string { return strings.TrimSpace(p.input.Value()) }

func (p *palette) filter() {
	q := p.query()
	p.shown = p.shown[:0]
	if q == "" {
		group := ""
		for _, it := range p.items {
			if searchOnly(it.group) {
				continue
			}
			if it.group != group {
				group = it.group
				p.shown = append(p.shown, paletteItem{header: true, label: group})
			}
			p.shown = append(p.shown, it)
		}
	} else {
		p.shown = append(p.shown, p.commandItems(q)...)
		have := map[string]bool{}
		for _, it := range p.shown {
			have[it.line] = true
		}
		nq := strings.ReplaceAll(strings.ToLower(q), " ", "")
		var hits []paletteItem
		for _, it := range p.items {
			if it.group == "Recent" || (it.line != "" && have[it.line]) {
				continue
			}
			sc, ok := prompt.FuzzyScore(nq, strings.ReplaceAll(it.label, " ", ""))
			if !ok {
				// Descriptions only match as plain text ("browser" → cloud login).
				if len(q) < 3 || !strings.Contains(strings.ToLower(it.desc), strings.ToLower(q)) {
					continue
				}
				sc = 1
			}
			if !searchOnly(it.group) {
				sc += 2
			}
			it.score = sc
			hits = append(hits, it)
		}
		sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
		p.shown = append(p.shown, hits...)
	}
	p.sel = clamp(p.sel, 0, max(len(p.shown)-1, 0))
	p.skipHeader(1)
}

// commandItems turns typed text into runnable rows: the line itself when
// it parses, and completions for its last word.
func (p *palette) commandItems(q string) []paletteItem {
	ctx := p.env.State.PromptContext()
	raw := p.input.Value()
	var out []paletteItem
	if parsed, err := prompt.Parse(raw, ctx); err == nil && strings.Contains(raw, " ") || err == nil && exactCommand(q) {
		out = append(out, paletteItem{group: "Run", label: q, desc: parsed.Cmd.Summary, line: q})
	}
	if !strings.Contains(raw, " ") {
		return out
	}
	comp := prompt.Complete(raw, ctx)
	for i, it := range comp.Items {
		if i == 6 {
			break
		}
		line := strings.TrimSpace(comp.Apply(raw, i))
		if line == q {
			continue
		}
		out = append(out, paletteItem{group: "Run", label: line, desc: it.Desc, line: line, needsArg: needsArg(line)})
	}
	return out
}

func exactCommand(q string) bool {
	c, ok := prompt.Lookup(strings.ToLower(strings.TrimPrefix(q, "/")))
	return ok && !c.Hidden
}

func (p *palette) skipHeader(dir int) {
	for p.sel >= 0 && p.sel < len(p.shown) && p.shown[p.sel].header {
		p.sel += dir
	}
	if p.sel >= len(p.shown) {
		p.sel = len(p.shown) - 1
		for p.sel > 0 && p.shown[p.sel].header {
			p.sel--
		}
	}
	if p.sel < 0 {
		p.sel = 0
		for p.sel < len(p.shown)-1 && p.shown[p.sel].header {
			p.sel++
		}
	}
}

func (p *palette) move(delta int) {
	if len(p.shown) == 0 {
		return
	}
	p.sel = clamp(p.sel+delta, 0, len(p.shown)-1)
	dir := 1
	if delta < 0 {
		dir = -1
	}
	p.skipHeader(dir)
}

func (p *palette) Wheel(delta int) overlay {
	p.move(delta)
	return p
}

func (p *palette) HandleKey(k tea.KeyMsg) (overlay, tea.Cmd) {
	switch k.String() {
	case "esc", "ctrl+k":
		return nil, nil
	case "up", "ctrl+p":
		p.move(-1)
		return p, nil
	case "down", "ctrl+n":
		p.move(1)
		return p, nil
	case "pgup":
		p.move(-max(p.rows-1, 1))
		return p, nil
	case "pgdown":
		p.move(max(p.rows-1, 1))
		return p, nil
	case "tab":
		if p.suggestion != "" {
			p.setInput(p.suggestion)
			return p, nil
		}
		if p.sel < len(p.shown) && p.shown[p.sel].line != "" && !p.shown[p.sel].header {
			p.setInput(p.shown[p.sel].line + " ")
		}
		return p, nil
	case "enter":
		if p.sel < len(p.shown) && !p.shown[p.sel].header {
			return p.pick(p.shown[p.sel])
		}
		return p.runTyped()
	}
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(k)
	p.errMsg, p.suggestion = "", ""
	p.sel = 0
	p.filter()
	return p, cmd
}

func (p *palette) setInput(s string) {
	p.input.SetValue(s)
	p.input.CursorEnd()
	p.errMsg, p.suggestion = "", ""
	p.sel = 0
	p.filter()
}

func (p *palette) Click(id string) (overlay, tea.Cmd) {
	if arg, ok := zoneArg(id, "ov:pal:"); ok {
		i, err := strconv.Atoi(arg)
		if err == nil && i < len(p.shown) && !p.shown[i].header {
			p.sel = i
			return p.pick(p.shown[i])
		}
	}
	return p, nil
}

// pick runs an item (or fills the input when it still needs an argument).
func (p *palette) pick(it paletteItem) (overlay, tea.Cmd) {
	switch {
	case it.cmd != nil:
		return nil, it.cmd
	case it.replay != "":
		key := it.replay
		return nil, func() tea.Msg { return replayKeyMsg{key: key} }
	case it.needsArg:
		p.setInput(it.line + " ")
		return p, nil
	}
	return p.runLine(it.line)
}

// runTyped runs what was typed when no row matches.
func (p *palette) runTyped() (overlay, tea.Cmd) {
	q := p.query()
	if q == "" {
		return nil, nil
	}
	return p.runLine(q)
}

func (p *palette) runLine(line string) (overlay, tea.Cmd) {
	if _, err := prompt.Parse(line, p.env.State.PromptContext()); err != nil {
		var pe *prompt.Error
		if errors.As(err, &pe) {
			p.errMsg, p.suggestion = pe.Msg, pe.Suggestion
		} else {
			p.errMsg = err.Error()
		}
		return p, nil
	}
	if p.hist != nil {
		p.hist.Add(line)
	}
	return nil, runLine(line)
}

func (p *palette) Size(bw, bh int) (int, int) {
	want := len(p.shown) + 4 // input, gap, gap, footer
	if len(p.shown) == 0 {
		want++
	}
	if p.errMsg != "" {
		want++
	}
	return clamp(bw-10, 20, 76), clamp(want, 6, min(20, bh-6))
}

func (p *palette) View(w, h int) string {
	p.input.Width = w - 4
	lines := []string{paint(" "+theme.AccentFg.Render(theme.GlyphPrompt)+" "+p.input.View(), w, bgSeq(theme.Element)), ""}
	foot := 2
	if p.errMsg != "" {
		foot = 3
	}
	rows := max(h-len(lines)-foot, 1)
	p.rows = rows
	if p.sel < p.top {
		p.top = p.sel
		if p.top > 0 && p.shown[p.top-1].header {
			p.top--
		}
	}
	if p.sel >= p.top+rows {
		p.top = p.sel - rows + 1
	}
	p.top = clamp(p.top, 0, max(len(p.shown)-rows, 0))
	keyW := 7
	labelW := clamp(w*2/5, 12, 30)
	for i := p.top; i < len(p.shown) && i < p.top+rows; i++ {
		it := p.shown[i]
		if it.header {
			lines = append(lines, theme.Dim.Render(it.label))
			continue
		}
		descW := max(w-labelW-keyW-6, 0)
		label := fit(it.label, labelW)
		if it.group == "Run" {
			label = fit(code(it.label), labelW)
		}
		row := fit(label+"  "+theme.Dim.Render(fit(it.desc, descW)), w-keyW-3) + " " + fitLeft(theme.Key.Render(it.keys), keyW)
		if i == p.sel {
			row = band(theme.Title.Render(fit(it.label, labelW))+"  "+fit(it.desc, descW)+" "+fitLeft(theme.Key.Render(it.keys), keyW), w)
		} else {
			row = "  " + row
		}
		lines = append(lines, p.env.mark("ov:pal:"+strconv.Itoa(i), fit(row, w)))
	}
	if len(p.shown) == 0 {
		lines = append(lines, theme.Dim.Render("  Nothing matches — Enter runs it as a command"))
	}
	for len(lines) < h-foot {
		lines = append(lines, "")
	}
	if p.errMsg != "" {
		msg := " " + theme.Err.Render(theme.GlyphFail+" ") + p.errMsg
		if p.suggestion != "" {
			msg += theme.Dim.Render(" — did you mean ") + code(p.suggestion) + theme.Dim.Render("? tab")
		}
		lines = append(lines, trunc(msg, w))
	}
	lines = append(lines, "", keyHint("↑↓", "move", "enter", "run", "tab", "complete", "esc", "close"))
	return block(lines, w, h)
}

func (p *palette) Hint() string { return "" }

// replayKeyMsg asks the app to send a key to the active screen.
type replayKeyMsg struct{ key string }

// ── Help (?) ──

type help struct {
	env    *Env
	screen Screen
	top    int
}

func newHelp(env *Env, s Screen) *help { return &help{env: env, screen: s} }

func (hp *help) Title() string { return "Help" }

func (hp *help) HandleKey(k tea.KeyMsg) (overlay, tea.Cmd) {
	switch k.String() {
	case "esc", "?", "q", "enter":
		return nil, nil
	case "down", "j":
		hp.top++
	case "up", "k":
		hp.top = max(hp.top-1, 0)
	}
	return hp, nil
}

func (hp *help) Wheel(delta int) overlay {
	hp.top = max(hp.top+delta, 0)
	return hp
}

var globalKeys = []KeyHelp{
	{"ctrl+k", "commands (also / or :)"},
	{"?", "this help"},
	{"esc", "close · cancel · back"},
	{"ctrl+o", "full-screen deploy log"},
	{"ctrl+c", "cancel the task · twice to quit"},
}

var mouseHelp = []string{
	"Click a row to select it, click again to open",
	"Wheel scrolls lists and logs",
	"Click the hints at the bottom to run them",
	"Click outside a popup to close it",
	"To select text: hold Option (iTerm2, Terminal) or Shift",
	"Turn the mouse off: --no-mouse or tui_mouse: off",
}

func (hp *help) Size(bw, bh int) (int, int) { return clamp(bw-10, 20, 100), max(bh-6, 4) }

func (hp *help) View(w, h int) string {
	var left, right []string
	if hp.screen != nil {
		if keys := hp.screen.Keys(); len(keys) > 0 {
			left = append(left, section(hp.screen.Title()))
			for _, k := range keys {
				left = append(left, fit(theme.Key.Render(k.Key), 12)+" "+k.Desc)
			}
			left = append(left, "")
		}
	}
	left = append(left, section("Everywhere"))
	for _, k := range globalKeys {
		left = append(left, fit(theme.Key.Render(k.Key), 12)+" "+k.Desc)
	}
	left = append(left, "", theme.Dim.Render("⌘K works only if your terminal forwards it"), theme.Dim.Render("as ctrl+k (Ghostty: keybind = cmd+k=text:\\x0b)."))
	right = append(right, section("Mouse"))
	if hp.env.NoMouse {
		right = append(right, theme.Dim.Render("Off — start without --no-mouse to click"))
	}
	for _, l := range mouseHelp {
		right = append(right, theme.Dim.Render("· ")+l)
	}
	right = append(right, "", section("Type a command in ctrl+k"))
	for _, name := range []string{"deploy", "logs", "rollback", "status", "servers", "docker", "settings", "doctor", "setup"} {
		c, _ := prompt.Lookup(name)
		right = append(right, fit(code(c.Usage), 26)+" "+theme.Dim.Render(c.Summary))
	}
	var lines []string
	if w >= 90 {
		colW := w/2 - 2
		for i := 0; i < max(len(left), len(right)); i++ {
			l, r := "", ""
			if i < len(left) {
				l = left[i]
			}
			if i < len(right) {
				r = right[i]
			}
			lines = append(lines, fit(l, colW)+"    "+r)
		}
	} else {
		for _, l := range left {
			lines = append(lines, l)
		}
		lines = append(lines, "")
		for _, r := range right {
			lines = append(lines, r)
		}
	}
	hp.top = clamp(hp.top, 0, max(len(lines)-h, 0))
	return block(lines[hp.top:], w, h)
}

func (hp *help) Hint() string { return keyHint("esc", "close", "ctrl+k", "commands") }
