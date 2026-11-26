package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Muster-dev/muster-tui/internal/docker"
	"github.com/Muster-dev/muster-tui/internal/theme"
	tea "github.com/charmbracelet/bubbletea"
)

var dockerTabs = []string{"Containers", "Images", "Volumes", "Disk"}

// dockerScreen: containers (with CPU/mem), images, volumes, disk usage, and
// the everyday actions. Works outside a muster project too.
type dockerScreen struct {
	env          *Env
	tab          int
	sel          [4]int
	containers   []docker.Container
	stats        map[string]docker.Stat
	images       []docker.Image
	volumes      []docker.Volume
	df           []docker.DiskUsage
	loaded       bool
	err          error
	loading      int
	pendingPrune string
	gen          int
	ids          map[int]bool
}

type (
	dockerData struct {
		containers []docker.Container
		images     []docker.Image
		volumes    []docker.Volume
		df         []docker.DiskUsage
		err        error
	}
	dockerStats struct {
		stats map[string]docker.Stat
	}
	dockerTick   struct{ gen int }
	dockerActMsg struct {
		ok   string
		err  error
		then tea.Cmd
	}
)

func newDockerScreen(env *Env, tab int) *dockerScreen {
	return &dockerScreen{env: env, tab: tab, ids: map[int]bool{}}
}

func (s *dockerScreen) Key() string   { return "docker" }
func (s *dockerScreen) Title() string { return "Docker" }
func (s *dockerScreen) Close()        { s.gen++; s.env.Tasks.CancelOwner(s.Key()) }

func (s *dockerScreen) Init() tea.Cmd { return s.load() }

func (s *dockerScreen) load() tea.Cmd {
	d := s.env.Docker
	if d == nil {
		s.err, s.loaded = docker.ErrNotInstalled, true
		return nil
	}
	s.loading++
	id, cmd := s.env.Tasks.Run("reading docker", s.Key(), false, func(ctx context.Context) tea.Msg {
		var r dockerData
		r.containers, r.err = d.Containers(ctx)
		if r.err != nil {
			return r
		}
		r.images, _ = d.Images(ctx)
		r.volumes, _ = d.Volumes(ctx)
		r.df, _ = d.DiskUsage(ctx)
		return r
	})
	s.ids[id] = true
	sid, scmd := s.env.Tasks.Run("docker stats", s.Key(), false, func(ctx context.Context) tea.Msg {
		st, _ := d.Stats(ctx)
		return dockerStats{st}
	})
	s.ids[sid] = true
	gen := s.gen
	tick := tea.Tick(10*time.Second, func(time.Time) tea.Msg { return dockerTick{gen} })
	return tea.Batch(cmd, scmd, tick)
}

func (s *dockerScreen) act(label string, fn func(ctx context.Context, d *docker.Client) (string, error)) tea.Cmd {
	d := s.env.Docker
	if d == nil {
		return toast(lvlErr, "Docker isn't installed")
	}
	id, cmd := s.env.Tasks.Run(label, s.Key(), true, func(ctx context.Context) tea.Msg {
		ok, err := fn(ctx, d)
		return dockerActMsg{ok: ok, err: err}
	})
	s.ids[id] = true
	return tea.Batch(cmd, toast(lvlInfo, strings.ToUpper(label[:1])+label[1:]+"…"))
}

func (s *dockerScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch m := msg.(type) {
	case dockerTick:
		if m.gen == s.gen {
			return s, s.load()
		}
	case TaskMsg:
		if !s.ids[m.ID] {
			return s, nil
		}
		delete(s.ids, m.ID)
		switch in := m.Msg.(type) {
		case dockerData:
			s.loading = max(s.loading-1, 0)
			s.loaded, s.err = true, in.err
			if in.err == nil {
				s.containers, s.images, s.volumes, s.df = in.containers, in.images, in.volumes, in.df
				for i := range s.sel {
					s.sel[i] = clamp(s.sel[i], 0, max(s.count(i)-1, 0))
				}
				s.env.State.apply(dockerSummaryMsg{s: summarize(in.containers, in.df), cs: in.containers}, s.env.Now())
				if s.pendingPrune != "" {
					what := s.pendingPrune
					s.pendingPrune = ""
					return s, s.confirmPrune(what)
				}
			}
		case dockerStats:
			if in.stats != nil {
				s.stats = in.stats
			}
		case dockerActMsg:
			if in.err != nil {
				return s, tea.Batch(toast(lvlErr, explainDocker(in.err)), s.load())
			}
			return s, tea.Batch(toast(lvlOK, in.ok), s.load())
		}
	}
	return s, nil
}

func summarize(cs []docker.Container, df []docker.DiskUsage) docker.Summary {
	sum := docker.Summary{Total: len(cs)}
	for _, c := range cs {
		if c.Running() {
			sum.Running++
		}
	}
	for _, d := range df {
		if d.Type == "Images" {
			sum.ImagesSize, sum.Reclaimable = d.Size, d.Reclaimable
		}
	}
	return sum
}

func explainDocker(err error) string {
	if errors.Is(err, docker.ErrDaemonDown) {
		return "Docker isn't running — start Docker Desktop (or colima start)"
	}
	return err.Error()
}

func (s *dockerScreen) count(tab int) int {
	switch tab {
	case 0:
		return len(s.containers)
	case 1:
		return len(s.images)
	case 2:
		return len(s.volumes)
	}
	return len(s.df)
}

func (s *dockerScreen) reclaimable(kind string) string {
	for _, d := range s.df {
		if d.Type == kind {
			return strings.TrimSpace(strings.Split(d.Reclaimable, "(")[0])
		}
	}
	return ""
}

func (s *dockerScreen) confirmPrune(what string) tea.Cmd {
	var title string
	var body []string
	switch what {
	case "containers":
		n := 0
		for _, c := range s.containers {
			if !c.Running() {
				n++
			}
		}
		if n == 0 {
			return toast(lvlInfo, "No stopped containers to remove")
		}
		title = fmt.Sprintf("Remove %s?", plural(n, "stopped container"))
		body = []string{"Running containers are not touched. Volumes are kept."}
	default:
		what = "images"
		n := 0
		for _, i := range s.images {
			if i.Dangling() {
				n++
			}
		}
		size := s.reclaimable("Images")
		title = fmt.Sprintf("Remove %s?", plural(n, "dangling image"))
		body = []string{"Untagged images no container uses. Tagged images are kept."}
		if size != "" {
			body = append(body, "Docker reports up to "+size+" reclaimable from images.")
		}
		if n == 0 {
			return toast(lvlInfo, "No dangling images to remove")
		}
	}
	return func() tea.Msg {
		return confirmMsg{title: title, body: body, yes: "Prune", onYes: s.act("pruning "+what, func(ctx context.Context, d *docker.Client) (string, error) {
			freed, err := d.Prune(ctx, what)
			return "Freed " + freed, err
		})}
	}
}

func (s *dockerScreen) HandleKey(k tea.KeyMsg) (Screen, tea.Cmd, bool) {
	key := k.String()
	switch key {
	case "left", "h", "shift+tab":
		s.tab = (s.tab + len(dockerTabs) - 1) % len(dockerTabs)
		return s, nil, true
	case "right":
		s.tab = (s.tab + 1) % len(dockerTabs)
		return s, nil, true
	case "1", "2", "3", "4":
		s.tab = int(key[0] - '1')
		return s, nil, true
	case "up", "k":
		s.sel[s.tab] = clamp(s.sel[s.tab]-1, 0, max(s.count(s.tab)-1, 0))
		return s, nil, true
	case "down", "j":
		s.sel[s.tab] = clamp(s.sel[s.tab]+1, 0, max(s.count(s.tab)-1, 0))
		return s, nil, true
	case "R", "ctrl+r":
		return s, s.load(), true
	case "P":
		if s.tab == 0 {
			return s, s.confirmPrune("containers"), true
		}
		return s, s.confirmPrune("images"), true
	}
	if s.err != nil {
		return s, nil, false
	}
	switch s.tab {
	case 0:
		if len(s.containers) == 0 {
			return s, nil, false
		}
		c := s.containers[s.sel[0]]
		name := c.Name()
		switch key {
		case "enter", "l":
			return s, nav(newContainerLogs(s.env, c.ID, name)), true
		case "r":
			return s, s.act("restarting "+name, func(ctx context.Context, d *docker.Client) (string, error) {
				return "Restarted " + name, d.Do(ctx, "restart", c.ID)
			}), true
		case "s":
			verb, done := "stop", "Stopped "
			if !c.Running() {
				verb, done = "start", "Started "
			}
			return s, s.act(verb+"ping "+name, func(ctx context.Context, d *docker.Client) (string, error) {
				return done + name, d.Do(ctx, verb, c.ID)
			}), true
		case "x":
			if !c.Running() {
				return s, toast(lvlInfo, name+" isn't running — s starts it"), true
			}
			return s, func() tea.Msg {
				return execMsg{cmd: s.env.Docker.ShellCmd(c.ID), after: func(err error) tea.Msg { return execDone("Shell in "+name, err) }}
			}, true
		case "d":
			return s, func() tea.Msg {
				return confirmMsg{title: "Remove container " + name + "?", body: []string{"It is stopped first if running. Its volumes are kept."}, yes: "Remove",
					onYes: s.act("removing "+name, func(ctx context.Context, d *docker.Client) (string, error) {
						return "Removed " + name, d.Do(ctx, "rm -f", c.ID)
					})}
			}, true
		}
	case 1:
		if len(s.images) == 0 {
			return s, nil, false
		}
		img := s.images[s.sel[1]]
		ref := img.Ref()
		switch key {
		case "p":
			if img.Dangling() {
				return s, toast(lvlInfo, "Untagged images can't be pulled"), true
			}
			return s, s.act("pulling "+ref, func(ctx context.Context, d *docker.Client) (string, error) {
				return "Pulled " + ref, d.Do(ctx, "pull", ref)
			}), true
		case "d":
			return s, func() tea.Msg {
				return confirmMsg{title: "Remove image " + ref + "?", body: []string{"Size " + img.Size + ". Fails if a container still uses it."}, yes: "Remove",
					onYes: s.act("removing "+ref, func(ctx context.Context, d *docker.Client) (string, error) {
						return "Removed " + ref, d.Do(ctx, "rmi", img.ID)
					})}
			}, true
		}
	case 2:
		if len(s.volumes) == 0 {
			return s, nil, false
		}
		v := s.volumes[s.sel[2]]
		if key == "d" {
			return s, func() tea.Msg {
				return confirmMsg{title: "Delete volume " + v.Name + "?", body: []string{"Its data is gone for good. Fails if a container uses it."}, yes: "Delete",
					onYes: s.act("removing volume "+v.Name, func(ctx context.Context, d *docker.Client) (string, error) {
						return "Deleted " + v.Name, d.Do(ctx, "volume rm", v.Name)
					})}
			}, true
		}
	case 3:
		switch key {
		case "i":
			return s, s.confirmPrune("images"), true
		case "c":
			return s, s.confirmPrune("containers"), true
		}
	}
	return s, nil, false
}

func (s *dockerScreen) View(w, h int) string {
	var tabs []string
	for i, t := range dockerTabs {
		label := fmt.Sprintf("%s %d", t, s.count(i))
		if i == 3 {
			label = t
		}
		if i == s.tab {
			tabs = append(tabs, button(label, true))
		} else {
			tabs = append(tabs, theme.Dim.Render(" "+label+" "))
		}
	}
	right := ""
	if s.loading > 0 {
		right = theme.AccentFg.Render(s.env.Spin())
	}
	lines := []string{spread(strings.Join(tabs, ""), right, w), ""}
	switch {
	case !s.loaded:
		lines = append(lines, theme.AccentFg.Render(s.env.Spin())+theme.Dim.Render(" Asking Docker…"))
		return block(lines, w, h)
	case errors.Is(s.err, docker.ErrNotInstalled):
		lines = append(lines, theme.Warning.Render(theme.GlyphWarn)+" Docker isn't installed on this machine.", "", theme.Dim.Render("Install Docker Desktop, OrbStack or colima, then come back."))
		return block(lines, w, h)
	case s.err != nil:
		lines = append(lines, theme.Warning.Render(theme.GlyphWarn)+" Docker isn't running.", "")
		lines = append(lines, wrap("Start Docker Desktop (or `colima start` / OrbStack), then press R to try again.", w)...)
		lines = append(lines, "", theme.Dim.Render(trunc(s.err.Error(), w)))
		return block(lines, w, h)
	}
	var rows []string
	header := ""
	switch s.tab {
	case 0:
		header, rows = s.containerRows(w)
	case 1:
		header, rows = s.imageRows(w)
	case 2:
		header, rows = s.volumeRows(w)
	case 3:
		header, rows = s.diskRows(w)
	}
	if header != "" {
		lines = append(lines, theme.Dim.Render(" "+header))
	}
	avail := h - len(lines) - 2
	sel := s.sel[s.tab]
	start := clamp(sel-avail+1, 0, max(len(rows)-avail, 0))
	for i := start; i < len(rows) && i < start+avail; i++ {
		r := " " + rows[i]
		if i == sel && s.tab != 3 {
			r = theme.AccentFg.Render(theme.GlyphCursor) + rows[i]
		}
		lines = append(lines, r)
	}
	if len(rows) == 0 {
		lines = append(lines, theme.Dim.Render(" nothing here"))
	}
	for len(lines) < h-2 {
		lines = append(lines, "")
	}
	lines = append(lines, rule(w), s.actionsLine())
	return block(lines, w, h)
}

func (s *dockerScreen) actionsLine() string {
	switch s.tab {
	case 0:
		return keyHint("l", "logs", "r", "restart", "s", "stop/start", "x", "shell", "d", "remove", "P", "prune")
	case 1:
		return keyHint("p", "pull", "d", "remove", "P", "prune dangling")
	case 2:
		return keyHint("d", "delete")
	}
	return keyHint("i", "prune images", "c", "prune containers", "R", "refresh")
}

func (s *dockerScreen) containerRows(w int) (string, []string) {
	nameW := clamp(w/4, 12, 26)
	statusW := 18
	showImage := w >= 100
	showPorts := w >= 72
	showStats := w >= 58
	hdr := " " + fit("NAME", nameW) + " " + fit("STATUS", statusW)
	if showPorts {
		hdr += " " + fit("PORTS", 14)
	}
	if showStats {
		hdr += " " + fit("CPU", 7) + fit("MEM", 10)
	}
	if showImage {
		hdr += " IMAGE"
	}
	var rows []string
	for _, c := range s.containers {
		glyph := theme.OK.Render(theme.GlyphHealthy)
		if !c.Running() {
			glyph = theme.Dim.Render(theme.GlyphUnknown)
		}
		if strings.Contains(c.Status, "unhealthy") {
			glyph = theme.Err.Render(theme.GlyphUnhealthy)
		}
		status := c.Status
		if c.Running() {
			status = strings.TrimPrefix(status, "Up ")
			status = "up " + status
		} else {
			status = strings.ToLower(status)
		}
		row := glyph + fit(c.Name(), nameW) + " " + theme.Dim.Render(fit(status, statusW))
		if showPorts {
			row += " " + fit(c.ShortPorts(), 14)
		}
		if showStats {
			st, ok := s.stats[c.Name()]
			cpu, mem := "", ""
			if ok {
				cpu, mem = st.CPUPerc, strings.TrimSpace(strings.Split(st.MemUsage, "/")[0])
			}
			row += " " + fit(cpu, 7) + fit(mem, 10)
		}
		if showImage {
			row += " " + theme.Dim.Render(c.Image)
		}
		rows = append(rows, row)
	}
	return hdr, rows
}

func (s *dockerScreen) imageRows(w int) (string, []string) {
	refW := clamp(w-34, 16, 48)
	hdr := " " + fit("IMAGE", refW) + " " + fit("SIZE", 9) + " CREATED"
	var rows []string
	for _, img := range s.images {
		ref := img.Ref()
		glyph := " "
		if img.Dangling() {
			ref = img.ID + theme.Dim.Render(" dangling")
			glyph = theme.Warning.Render(theme.GlyphWarn)
		}
		rows = append(rows, glyph+fit(ref, refW)+" "+fit(img.Size, 9)+" "+theme.Dim.Render(img.CreatedSince))
	}
	return hdr, rows
}

func (s *dockerScreen) volumeRows(w int) (string, []string) {
	hdr := " " + fit("VOLUME", clamp(w-12, 16, 60)) + " DRIVER"
	var rows []string
	for _, v := range s.volumes {
		rows = append(rows, " "+fit(v.Name, clamp(w-12, 16, 60))+" "+theme.Dim.Render(v.Driver))
	}
	return hdr, rows
}

func (s *dockerScreen) diskRows(w int) (string, []string) {
	hdr := " " + fit("TYPE", 15) + fit("TOTAL", 7) + fit("ACTIVE", 8) + fit("SIZE", 10) + "RECLAIMABLE"
	var rows []string
	for _, d := range s.df {
		rows = append(rows, " "+fit(d.Type, 15)+fit(d.Count(), 7)+fit(fmt.Sprint(d.Active), 8)+fit(d.Size, 10)+theme.Warning.Render(d.Reclaimable))
	}
	return hdr, rows
}

func (s *dockerScreen) Hint() string {
	if s.err != nil && !errors.Is(s.err, docker.ErrNotInstalled) {
		return theme.Warning.Render(theme.GlyphWarn+" ") + "Docker isn't running" + theme.Dim.Render(" · ") + keyHint("R", "retry")
	}
	return keyHint("←→ 1-4", "tabs", "↑↓", "select", "R", "refresh")
}

func (s *dockerScreen) Keys() []KeyHelp {
	return []KeyHelp{
		{"← → / 1-4", "switch tab"}, {"↑ ↓", "select"}, {"l / Enter", "container logs"}, {"r", "restart container"},
		{"s", "stop / start"}, {"x", "shell inside (muster steps aside)"}, {"d", "remove (asks first)"},
		{"p", "pull image"}, {"P", "prune (shows reclaimable space)"}, {"R", "refresh"},
	}
}
