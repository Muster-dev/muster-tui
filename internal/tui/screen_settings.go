package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Muster-dev/muster-tui/internal/config"
	"github.com/Muster-dev/muster-tui/internal/jsonx"
	"github.com/Muster-dev/muster-tui/internal/theme"
	tea "github.com/charmbracelet/bubbletea"
)

// settingsScreen edits ~/.muster/settings.json and muster.json. Edits go to
// working copies; Ctrl+S validates and writes. Unknown keys are preserved.
type settingsScreen struct {
	env       *Env
	sections  []string // "global", "order", "svc:<key>"
	sec       int
	global    *jsonx.Object
	globalErr error
	proj      *config.Project // working copy
	form      *form
	orderSel  int
	dirtyG    bool
	dirtyP    bool
	escArmed  bool
	errs      []config.FieldError
	wantSvc   string
}

func newSettings(env *Env) *settingsScreen {
	s := &settingsScreen{env: env}
	s.reload()
	return s
}

func (s *settingsScreen) reload() {
	s.global, s.globalErr = config.LoadGlobal()
	if s.global == nil {
		s.global = jsonx.New()
	}
	s.sections = []string{"global"}
	s.proj = nil
	if p := s.env.State.Project; p != nil {
		s.proj = p.Clone()
		s.sections = append(s.sections, "order")
		for _, k := range s.proj.ServiceKeys() {
			s.sections = append(s.sections, "svc:"+k)
		}
	}
	s.dirtyG, s.dirtyP = false, false
	s.sec = clamp(s.sec, 0, len(s.sections)-1)
	s.buildForm()
}

func (s *settingsScreen) focusService(key string) {
	for i, sec := range s.sections {
		if sec == "svc:"+key {
			s.sec = i
			s.buildForm()
		}
	}
}

func (s *settingsScreen) Key() string   { return "settings" }
func (s *settingsScreen) Title() string { return "Settings" }
func (s *settingsScreen) Init() tea.Cmd { return nil }
func (s *settingsScreen) Capturing() bool {
	return s.form != nil && s.form.editing
}

func (s *settingsScreen) current() string { return s.sections[s.sec] }

func (s *settingsScreen) buildForm() {
	s.form = nil
	switch sec := s.current(); {
	case sec == "global":
		var fields []field
		for _, g := range config.GlobalFields {
			v := ""
			if raw, ok := s.global.Value(g.Key); ok {
				v = fmt.Sprint(raw)
			} else {
				v = fmt.Sprint(g.Default)
			}
			f := field{key: g.Key, label: g.Label, help: g.Help, value: v, opts: g.Options}
			switch g.Kind {
			case "enum":
				f.kind = fEnum
			case "int":
				f.kind = fInt
			case "bool":
				f.kind = fBool
			}
			fields = append(fields, f)
		}
		s.form = newForm(fields)
	case strings.HasPrefix(sec, "svc:"):
		s.form = newForm(s.serviceFields(strings.TrimPrefix(sec, "svc:")))
	}
	s.validate()
}

func (s *settingsScreen) serviceFields(key string) []field {
	svc := s.proj.Service(key)
	itoa := func(n int) string {
		if n == 0 {
			return ""
		}
		return strconv.Itoa(n)
	}
	b := func(v bool) string { return strconv.FormatBool(v) }
	health := "none"
	if svc.Health.Enabled {
		health = firstNonEmpty(svc.Health.Type, "hook")
	}
	stack := s.proj.Stack(key)
	fields := []field{
		{key: "name", label: "Display name", kind: fText, value: svc.Name},
		{key: "skip_deploy", label: "Skip on deploy", kind: fBool, value: b(svc.SkipDeploy), help: "left out of `deploy` (still deployable by name)"},
		{label: "Health check", kind: fHeading},
		{key: "health.type", label: "Type", kind: fEnum, opts: []string{"none", "http", "tcp", "command"}, value: health},
		{key: "health.port", label: "Port", kind: fInt, value: itoa(svc.Health.Port)},
		{key: "health.endpoint", label: "Endpoint", kind: fText, value: svc.Health.Endpoint, help: "path, e.g. /health"},
		{key: "health.command", label: "Command", kind: fText, value: svc.Health.Command},
		{key: "health.timeout", label: "Timeout (s)", kind: fInt, value: itoa(svc.Health.Timeout)},
		{label: "Deploy", kind: fHeading},
		{key: "credentials.mode", label: "Credentials", kind: fEnum, opts: config.CredModes, value: svc.CredsMode, help: "off · save (keychain) · session · always ask"},
		{key: "deploy_timeout", label: "Deploy timeout (s)", kind: fInt, value: itoa(svc.DeployTimeout), help: "default 120"},
		{key: "git_pull.enabled", label: "Git pull first", kind: fBool, value: b(svc.GitPull.Enabled)},
		{key: "git_pull.remote", label: "  Remote", kind: fText, value: svc.GitPull.Remote, help: "default origin"},
		{key: "git_pull.branch", label: "  Branch", kind: fText, value: svc.GitPull.Branch, help: "default main"},
		{label: "Remote host", kind: fHeading},
		{key: "remote.enabled", label: "Deploy over SSH", kind: fBool, value: b(svc.Remote.Enabled)},
		{key: "remote.host", label: "  Host", kind: fText, value: svc.Remote.Host},
		{key: "remote.user", label: "  User", kind: fText, value: svc.Remote.User},
		{key: "remote.port", label: "  Port", kind: fInt, value: itoa(svc.Remote.Port), help: "default 22"},
		{key: "remote.identity_file", label: "  Key file", kind: fText, value: svc.Remote.IdentityFile},
		{key: "remote.project_dir", label: "  Project dir", kind: fText, value: svc.Remote.ProjectDir},
	}
	if stack == "k8s" || svc.K8s.Deployment != "" || svc.K8s.Namespace != "" {
		fields = append(fields,
			field{label: "Kubernetes", kind: fHeading},
			field{key: "k8s.deployment", label: "Deployment", kind: fText, value: svc.K8s.Deployment},
			field{key: "k8s.namespace", label: "Namespace", kind: fText, value: svc.K8s.Namespace})
	}
	s.applyVisibility(fields)
	return fields
}

func (s *settingsScreen) applyVisibility(fields []field) {
	get := func(k string) string {
		for _, f := range fields {
			if f.key == k {
				return f.value
			}
		}
		return ""
	}
	ht := get("health.type")
	for i := range fields {
		switch fields[i].key {
		case "health.port":
			fields[i].hidden = ht != "http" && ht != "tcp"
		case "health.endpoint":
			fields[i].hidden = ht != "http"
		case "health.command":
			fields[i].hidden = ht != "command"
		case "health.timeout":
			fields[i].hidden = ht == "none"
		case "git_pull.remote", "git_pull.branch":
			fields[i].hidden = get("git_pull.enabled") != "true"
		case "remote.host", "remote.user", "remote.port", "remote.identity_file", "remote.project_dir":
			fields[i].hidden = get("remote.enabled") != "true"
		}
	}
}

// commit writes the changed field into the working copy.
func (s *settingsScreen) commit() {
	f := s.form.fields[s.form.sel]
	if s.current() == "global" {
		var v any = f.value
		switch f.kind {
		case fInt:
			n, _ := strconv.Atoi(f.value)
			v = n
		case fBool:
			v = f.value == "true"
		}
		s.global.Set(v, f.key)
		s.dirtyG = true
		return
	}
	key := strings.TrimPrefix(s.current(), "svc:")
	base := []string{"services", key}
	path := append(base, strings.Split(f.key, ".")...)
	doc := s.proj.Doc
	switch {
	case f.key == "health.type":
		if f.value == "none" {
			doc.Set(false, append(base, "health", "enabled")...)
		} else {
			doc.Set(true, append(base, "health", "enabled")...)
			if f.value != "hook" {
				doc.Set(f.value, append(base, "health", "type")...)
			}
		}
		s.applyVisibility(s.form.fields)
	case f.kind == fBool:
		doc.Set(f.value == "true", path...)
		s.applyVisibility(s.form.fields)
	case f.kind == fInt:
		if f.value == "" {
			doc.Delete(path...)
		} else {
			n, _ := strconv.Atoi(f.value)
			doc.Set(n, path...)
		}
	default:
		if f.value == "" && f.key != "name" {
			doc.Delete(path...)
		} else {
			doc.Set(f.value, path...)
		}
	}
	s.dirtyP = true
}

func (s *settingsScreen) validate() {
	s.errs = nil
	if s.proj != nil {
		s.errs = s.proj.Validate()
	}
	if s.form == nil || !strings.HasPrefix(s.current(), "svc:") {
		return
	}
	prefix := "services." + strings.TrimPrefix(s.current(), "svc:") + "."
	for i := range s.form.fields {
		s.form.fields[i].err = ""
		for _, e := range s.errs {
			if e.Path == prefix+s.form.fields[i].key || (s.form.fields[i].key == "health.type" && e.Path == prefix+"health.type") {
				s.form.fields[i].err = e.Message
			}
		}
	}
}

func (s *settingsScreen) save() tea.Cmd {
	var cmds []tea.Cmd
	if s.dirtyG {
		if err := config.SaveGlobal(s.global); err != nil {
			return toast(lvlErr, "Couldn't save settings: "+err.Error())
		}
		s.dirtyG = false
		cmds = append(cmds, toast(lvlOK, "Saved ~/.muster/settings.json"))
	}
	if s.dirtyP && s.proj != nil {
		s.validate()
		if len(s.errs) > 0 {
			return toast(lvlErr, "Fix "+plural(len(s.errs), "problem")+" first: "+s.errs[0].Error())
		}
		if err := s.proj.Save(); err != nil {
			return toast(lvlErr, "Couldn't save: "+err.Error())
		}
		s.dirtyP = false
		cmds = append(cmds, toast(lvlOK, "Saved "+shortPath(s.proj.Path)+" — approve the change before the next deploy"), func() tea.Msg { return reloadConfigMsg{} })
	}
	if len(cmds) == 0 {
		return toast(lvlInfo, "Nothing to save")
	}
	return tea.Sequence(cmds...)
}

func (s *settingsScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	if _, ok := msg.(reloadConfigMsg); ok && !s.dirtyP && !s.dirtyG {
		sec := s.current()
		s.reload()
		for i, x := range s.sections {
			if x == sec {
				s.sec = i
			}
		}
		s.buildForm()
	}
	return s, nil
}

func (s *settingsScreen) switchSection(d int) {
	s.sec = (s.sec + d + len(s.sections)) % len(s.sections)
	s.buildForm()
}

func (s *settingsScreen) HandleKey(k tea.KeyMsg) (Screen, tea.Cmd, bool) {
	key := k.String()
	if key != "esc" {
		s.escArmed = false
	}
	if s.form != nil && s.form.editing {
		_, changed := s.form.HandleKey(k)
		if changed {
			s.commit()
			s.validate()
		}
		return s, nil, true
	}
	switch key {
	case "ctrl+s", "w":
		return s, s.save(), true
	case "]", "pgdown":
		s.switchSection(1)
		return s, nil, true
	case "[", "pgup":
		s.switchSection(-1)
		return s, nil, true
	case "e":
		path := config.GlobalPath()
		if s.current() != "global" && s.proj != nil {
			path = s.proj.Path
		}
		if s.dirtyG || s.dirtyP {
			return s, toast(lvlWarn, "Save or discard your changes before opening the editor"), true
		}
		return s, editPath(path), true
	case "esc":
		if (s.dirtyG || s.dirtyP) && !s.escArmed {
			s.escArmed = true
			return s, toast(lvlWarn, "Unsaved changes — Ctrl+S saves, Esc again discards"), true
		}
		if s.escArmed {
			s.reload()
			s.escArmed = false
		}
		return s, func() tea.Msg { return backMsg{} }, true
	}
	if s.current() == "order" {
		return s.orderKey(k)
	}
	if s.form != nil {
		handled, changed := s.form.HandleKey(k)
		if changed {
			s.commit()
			s.validate()
		}
		if handled {
			return s, nil, true
		}
	}
	return s, nil, false
}

func (s *settingsScreen) orderKey(k tea.KeyMsg) (Screen, tea.Cmd, bool) {
	order := s.proj.ServiceKeys()
	switch k.String() {
	case "j", "down":
		s.orderSel = clamp(s.orderSel+1, 0, len(order)-1)
	case "k", "up":
		s.orderSel = clamp(s.orderSel-1, 0, len(order)-1)
	case "K", "shift+up":
		if s.orderSel > 0 {
			order[s.orderSel], order[s.orderSel-1] = order[s.orderSel-1], order[s.orderSel]
			s.orderSel--
			s.proj.Doc.Set(order, "deploy_order")
			s.dirtyP = true
		}
	case "J", "shift+down":
		if s.orderSel < len(order)-1 {
			order[s.orderSel], order[s.orderSel+1] = order[s.orderSel+1], order[s.orderSel]
			s.orderSel++
			s.proj.Doc.Set(order, "deploy_order")
			s.dirtyP = true
		}
	case " ":
		key := order[s.orderSel]
		cur := s.proj.Service(key).SkipDeploy
		s.proj.Doc.Set(!cur, "services", key, "skip_deploy")
		s.dirtyP = true
	default:
		return s, nil, false
	}
	s.validate()
	return s, nil, true
}

func sectionLabel(sec string) string {
	switch {
	case sec == "global":
		return "Global"
	case sec == "order":
		return "Deploy order"
	}
	return strings.TrimPrefix(sec, "svc:")
}

func (s *settingsScreen) View(w, h int) string {
	listW := 0
	if w >= 70 {
		listW = 18
	}
	var right []string
	title := sectionLabel(s.current())
	status := ""
	switch {
	case s.dirtyG || s.dirtyP:
		status = theme.Warning.Render("unsaved · Ctrl+S")
	case s.current() == "global":
		status = theme.Dim.Render("~/.muster/settings.json")
	case s.proj != nil:
		status = theme.Dim.Render(shortPath(s.proj.Path))
	}
	fw := w - listW - 2
	if listW == 0 {
		fw = w
		title = "‹ " + title + " ›"
	}
	right = append(right, spread(section(title), status, fw), "")
	switch {
	case s.current() == "order":
		right = append(right, theme.Dim.Render("Services deploy top to bottom. J/K move, Space toggles skip."), "")
		for i, k := range s.proj.ServiceKeys() {
			cur := "  "
			if i == s.orderSel {
				cur = theme.AccentFg.Render(theme.GlyphCursor) + " "
			}
			skip := ""
			if s.proj.Service(k).SkipDeploy {
				skip = theme.Dim.Render("  skipped")
			}
			right = append(right, fmt.Sprintf("%s%s  %s%s", cur, theme.Dim.Render(fmt.Sprintf("%2d", i+1)), k, skip))
		}
	case s.form != nil:
		if s.current() == "global" && s.globalErr != nil {
			right = append(right, theme.Err.Render(theme.GlyphFail+" settings.json: "+s.globalErr.Error()), "")
		}
		right = append(right, s.form.Lines(fw, 20, true)...)
	}
	if n := len(s.errs); n > 0 && s.current() != "global" {
		right = append(right, "", theme.Err.Render(fmt.Sprintf("%s %s", theme.GlyphFail, plural(n, "problem"))))
		for _, e := range s.errs[:min(n, 3)] {
			right = append(right, theme.Err.Render("  "+trunc(e.Error(), fw-2)))
		}
	}
	// scroll the form so the cursor stays visible
	bodyH := h - 2
	if s.form != nil && len(right) > bodyH {
		selLine := 2
		count := 0
		for i, f := range s.form.fields {
			if f.hidden {
				continue
			}
			if f.kind == fHeading {
				count++
			}
			count++
			if i == s.form.sel {
				selLine = count + 1
				break
			}
		}
		off := clamp(selLine-bodyH+3, 0, len(right)-bodyH)
		right = append(right[:2], right[2+off:]...)
	}
	for len(right) < bodyH {
		right = append(right, "")
	}
	right = right[:bodyH]
	right = append(right, rule(fw), keyHint("↑↓", "field", "Enter/←→", "change", "[ ]", "section", "Ctrl+S", "save", "e", "$EDITOR"))
	if listW == 0 {
		return block(right, w, h)
	}
	var left []string
	for i, sec := range s.sections {
		label := sectionLabel(sec)
		if strings.HasPrefix(sec, "svc:") {
			label = "  " + label
		}
		if i == s.sec {
			left = append(left, theme.AccentFg.Render(theme.GlyphCursor)+theme.Title.Render(fit(label, listW-1)))
		} else {
			left = append(left, " "+theme.Dim.Render(fit(label, listW-1)))
		}
		if sec == "order" {
			left = append(left, theme.Dim.Render(" Services"))
		}
	}
	out := make([]string, h)
	for i := 0; i < h; i++ {
		l, r := "", ""
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		out[i] = fit(l, listW) + "  " + fit(r, fw)
	}
	return strings.Join(out, "\n")
}

func (s *settingsScreen) Hint() string {
	if s.form != nil && s.form.editing {
		return keyHint("Enter", "set", "Esc", "cancel")
	}
	return ""
}

func (s *settingsScreen) Keys() []KeyHelp {
	return []KeyHelp{{"[ ] / PgUp PgDn", "previous / next section"}, {"↑ ↓", "field"}, {"Enter / Space", "edit or toggle"}, {"← →", "cycle choices"}, {"J / K", "reorder (deploy order)"}, {"Ctrl+S / w", "validate and save"}, {"e", "open the file in $EDITOR"}, {"Esc", "back (asks about unsaved changes)"}}
}
