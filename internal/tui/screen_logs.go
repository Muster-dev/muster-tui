package tui

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Muster-dev/muster-tui/internal/proc"
	"github.com/Muster-dev/muster-tui/internal/theme"
	tea "github.com/charmbracelet/bubbletea"
)

const maxLogLines = 10000

// logsTail is how many earlier lines the logs view starts with (muster ≥ 0.7).
const logsTail = 200

type lineOpener func(ctx context.Context) (<-chan string, func() error, error)

// logsScreen shows a streamed command's output: service logs, container
// logs, or one-shot command output (rollback, cleanup, fleet …).
type logsScreen struct {
	env     *Env
	key     string
	title   string
	follow  bool // "following" a log stream vs running a command
	fg      bool
	open    lineOpener
	lines   []string
	taskID  int
	running bool
	done    bool
	err     error

	top   int // first visible (filtered) row when not at end
	atEnd bool

	searching bool
	search    lineInput
	query     string
	cursor    int // filtered index of current match/line

	level int // 0 all, 1 warnings+errors, 2 errors
	wrap  bool
	onEnd func(error) tea.Cmd
}

type (
	logBatchMsg struct{ lines []string }
	logEndMsg   struct{ err error }
)

func newLogsScreen(env *Env, key, title string, follow, fg bool, open lineOpener) *logsScreen {
	ti := newLineInput()
	ti.Prompt = "/"
	return &logsScreen{env: env, key: key, title: title, follow: follow, fg: fg, open: open, atEnd: true, search: ti, cursor: -1}
}

// newOutput runs a command and shows its output.
func newOutput(env *Env, key, title string, fg bool, open lineOpener) *logsScreen {
	return newLogsScreen(env, "out:"+key, title, false, fg, open)
}

func newServiceLogs(env *Env, svc string) *logsScreen {
	eng := env.Eng()
	d := env.Docker
	var containerID string
	for _, c := range env.State.Containers {
		if c.ComposeService() == svc {
			containerID = c.ID
		}
	}
	v07 := env.V07()
	return newLogsScreen(env, "logs:"+svc, "Logs · "+svc, true, false, func(ctx context.Context) (<-chan string, func() error, error) {
		if eng != nil && v07 {
			// Last lines from the stack's own log source, then follow.
			s, err := eng.LogsStream(ctx, svc, logsTail, true)
			if err != nil {
				return nil, nil, err
			}
			return streamLines(s, logEventLines)
		}
		if eng != nil {
			return eng.Logs(ctx, svc)
		}
		if d != nil && containerID != "" {
			ls, err := d.Logs(ctx, containerID, 500, true)
			if err != nil {
				return nil, nil, err
			}
			return fromLineStream(ls)
		}
		return nil, nil, fmt.Errorf("can't read logs without the muster CLI")
	})
}

func newContainerLogs(env *Env, id, name string) *logsScreen {
	d := env.Docker
	return newLogsScreen(env, "clogs:"+name, "Logs · "+name, true, false, func(ctx context.Context) (<-chan string, func() error, error) {
		ls, err := d.Logs(ctx, id, 500, true)
		if err != nil {
			return nil, nil, err
		}
		return fromLineStream(ls)
	})
}

func fleetOutput(env *Env, sub string, args []string) *logsScreen {
	eng := env.Eng()
	target := strings.Join(args, " ")
	title := "Fleet " + sub
	if target != "" {
		title += " · " + target
	}
	return newOutput(env, "fleet-"+sub, title, sub != "status", func(ctx context.Context) (<-chan string, func() error, error) {
		return rawLines(ctx, eng.Command(ctx, append([]string{"fleet", sub}, args...)...))
	})
}

func rawLines(ctx context.Context, cmd *exec.Cmd) (<-chan string, func() error, error) {
	ls, err := proc.Lines(cmd)
	if err != nil {
		return nil, nil, err
	}
	return fromLineStream(ls)
}

func fromLineStream(ls *proc.LineStream) (<-chan string, func() error, error) {
	ch := make(chan string, 64)
	go func() {
		defer close(ch)
		for l := range ls.C {
			ch <- l.Text
		}
	}()
	return ch, ls.Wait, nil
}

func (s *logsScreen) Key() string   { return s.key }
func (s *logsScreen) Title() string { return s.title }
func (s *logsScreen) Capturing() bool {
	return s.searching
}
func (s *logsScreen) Close() { s.env.Tasks.Cancel(s.taskID) }

func (s *logsScreen) Init() tea.Cmd {
	open := s.open
	label := strings.ToLower(s.title)
	s.running, s.done, s.err = true, false, nil
	id, cmd := s.env.Tasks.Stream(label, s.key, s.fg, func(ctx context.Context) (<-chan tea.Msg, error) {
		lines, wait, err := open(ctx)
		if err != nil {
			return nil, err
		}
		out := make(chan tea.Msg, 8)
		go func() {
			defer close(out)
			batch := make([]string, 0, 64)
			flush := time.NewTicker(60 * time.Millisecond)
			defer flush.Stop()
			for {
				select {
				case l, ok := <-lines:
					if !ok {
						if len(batch) > 0 {
							out <- logBatchMsg{batch}
						}
						var werr error
						if wait != nil {
							werr = wait()
						}
						if ctx.Err() != nil {
							werr = nil
						}
						out <- logEndMsg{werr}
						return
					}
					batch = append(batch, l)
					if len(batch) >= 200 {
						out <- logBatchMsg{batch}
						batch = make([]string, 0, 64)
					}
				case <-flush.C:
					if len(batch) > 0 {
						out <- logBatchMsg{batch}
						batch = make([]string, 0, 64)
					}
				}
			}
		}()
		return out, nil
	})
	s.taskID = id
	return cmd
}

func (s *logsScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch m := msg.(type) {
	case TaskMsg:
		if m.ID != s.taskID {
			return s, nil
		}
		switch in := m.Msg.(type) {
		case logBatchMsg:
			s.lines = append(s.lines, in.lines...)
			if over := len(s.lines) - maxLogLines; over > 0 {
				s.lines = s.lines[over:]
				s.top = max(s.top-over, 0)
			}
		case logEndMsg:
			s.err = in.err
		case taskErrMsg:
			s.err = in.Err
		}
	case TaskEndedMsg:
		if m.ID == s.taskID {
			s.running, s.done = false, true
			if s.onEnd != nil {
				return s, s.onEnd(s.err)
			}
		}
	}
	return s, nil
}

var (
	errRe  = regexp.MustCompile(`(?i)\b(error|err|fatal|panic|exception|failed|failure|traceback|crash)\b`)
	warnRe = regexp.MustCompile(`(?i)\b(warn|warning|deprecated)\b`)
)

func lineLevel(l string) int {
	switch {
	case errRe.MatchString(l):
		return 2
	case warnRe.MatchString(l):
		return 1
	}
	return 0
}

// visible returns indices into s.lines that pass the level filter.
func (s *logsScreen) visible() []int {
	out := make([]int, 0, len(s.lines))
	for i, l := range s.lines {
		if s.level == 0 || lineLevel(l) >= s.level {
			out = append(out, i)
		}
	}
	return out
}

func (s *logsScreen) bodyHeight() int { return max(s.env.MainH-2, 1) }

func (s *logsScreen) HandleKey(k tea.KeyMsg) (Screen, tea.Cmd, bool) {
	if s.searching {
		switch k.String() {
		case "esc":
			s.searching = false
			return s, nil, true
		case "enter":
			s.searching = false
			s.query = s.search.Value()
			s.jumpMatch(1, true)
			return s, nil, true
		}
		var cmd tea.Cmd
		s.search, cmd = s.search.Update(k)
		return s, cmd, true
	}
	vis := s.visible()
	h := s.bodyHeight()
	maxTop := max(len(vis)-h, 0)
	if s.atEnd {
		s.top = maxTop
	}
	switch k.String() {
	case "up", "k":
		s.atEnd = false
		s.top = clamp(s.top-1, 0, maxTop)
	case "down", "j":
		s.top = clamp(s.top+1, 0, maxTop)
		s.atEnd = s.top == maxTop
	case "pgup", "ctrl+u":
		s.atEnd = false
		s.top = clamp(s.top-h, 0, maxTop)
	case "pgdown", "ctrl+d", " ":
		s.top = clamp(s.top+h, 0, maxTop)
		s.atEnd = s.top == maxTop
	case "home", "g":
		s.atEnd, s.top = false, 0
	case "end", "G", "f":
		s.atEnd = true
	case "/":
		s.searching = true
		s.search.SetValue(s.query)
		s.search.CursorEnd()
		s.search.Focus()
		return s, nil, true
	case "n":
		s.jumpMatch(1, false)
	case "N":
		s.jumpMatch(-1, false)
	case "e":
		s.jumpError(1)
	case "E":
		s.jumpError(-1)
	case "L":
		s.level = (s.level + 1) % 3
		s.atEnd = true
	case "w":
		s.wrap = !s.wrap
	case "y":
		text := strings.Join(tail(s.filteredText(), 2000), "\n")
		return s, copyCmd(text, fmt.Sprintf("Copied %s", plural(min(len(s.filteredText()), 2000), "line"))), true
	case "r":
		if !s.running {
			s.lines = nil
			s.top, s.atEnd = 0, true
			return s, s.Init(), true
		}
	case "esc":
		if s.query != "" {
			s.query, s.cursor = "", -1
			return s, nil, true
		}
		if s.running && s.fg {
			s.env.Tasks.Cancel(s.taskID)
			return s, toast(lvlWarn, "Cancelled "+strings.ToLower(s.title)), true
		}
		return s, nil, false
	default:
		return s, nil, false
	}
	return s, nil, true
}

func (s *logsScreen) filteredText() []string {
	var out []string
	for _, i := range s.visible() {
		out = append(out, s.lines[i])
	}
	return out
}

func (s *logsScreen) jumpMatch(dir int, fromTop bool) {
	if s.query == "" {
		return
	}
	vis := s.visible()
	q := strings.ToLower(s.query)
	start := s.cursor
	if fromTop || start < 0 {
		start = s.top - dir
		if s.atEnd {
			start = len(vis)
		}
		if dir > 0 && s.atEnd {
			start = -1
		}
	}
	for n := 1; n <= len(vis); n++ {
		i := ((start+dir*n)%len(vis) + len(vis)) % len(vis)
		if strings.Contains(strings.ToLower(s.lines[vis[i]]), q) {
			s.cursor = i
			s.reveal(i, len(vis))
			return
		}
	}
	s.cursor = -1
}

func (s *logsScreen) jumpError(dir int) {
	vis := s.visible()
	start := s.cursor
	if start < 0 {
		start = s.top
	}
	for i := start + dir; i >= 0 && i < len(vis); i += dir {
		if lineLevel(s.lines[vis[i]]) == 2 {
			s.cursor = i
			s.reveal(i, len(vis))
			return
		}
	}
}

func (s *logsScreen) reveal(i, n int) {
	h := s.bodyHeight()
	s.atEnd = false
	s.top = clamp(i-h/3, 0, max(n-h, 0))
}

func (s *logsScreen) View(w, h int) string {
	state := ""
	switch {
	case s.running && s.follow:
		state = theme.OK.Render("● following")
		if !s.atEnd {
			state = theme.Dim.Render("○ paused · End to follow")
		}
	case s.running:
		state = theme.AccentFg.Render(s.env.Spin() + " running")
	case s.err != nil:
		state = theme.Err.Render(theme.GlyphFail + " " + trunc(s.err.Error(), w/2))
	case s.done && s.follow:
		state = theme.Dim.Render("stream ended · r restart")
	case s.done:
		state = theme.OK.Render(theme.GlyphOK + " done")
	}
	var tags []string
	if s.level == 1 {
		tags = append(tags, "warnings+")
	} else if s.level == 2 {
		tags = append(tags, "errors only")
	}
	if s.wrap {
		tags = append(tags, "wrap")
	}
	if s.query != "" {
		tags = append(tags, "/"+s.query)
	}
	head := section(s.title)
	if len(tags) > 0 {
		head += "  " + theme.Dim.Render(strings.Join(tags, " · "))
	}
	lines := []string{spread(head, state, w)}
	bodyH := h - 1
	if s.searching {
		bodyH--
	}
	vis := s.visible()
	top := s.top
	if s.atEnd {
		top = max(len(vis)-bodyH, 0)
	}
	if len(s.lines) == 0 {
		msg := "waiting for output…"
		if s.done {
			msg = "no output"
		}
		lines = append(lines, theme.Dim.Render(msg))
	}
	for i := top; i < len(vis) && len(lines) < bodyH+1; i++ {
		raw := s.lines[vis[i]]
		rows := []string{raw}
		if s.wrap {
			rows = wrap(raw, w-1)
		}
		for _, r := range rows {
			if len(lines) >= bodyH+1 {
				break
			}
			lines = append(lines, s.styleLine(r, i == s.cursor, w))
		}
	}
	for len(lines) < bodyH+1 {
		lines = append(lines, "")
	}
	if s.searching {
		s.search.Width = w
		lines = append(lines, s.search.View())
	}
	return block(lines, w, h)
}

func (s *logsScreen) styleLine(l string, current bool, w int) string {
	l = strings.ReplaceAll(l, "\t", "    ")
	l = trunc(l, w)
	style := theme.Base
	switch lineLevel(l) {
	case 2:
		style = theme.Err
	case 1:
		style = theme.Warning
	}
	if s.query != "" {
		q := strings.ToLower(s.query)
		low := strings.ToLower(l)
		var b strings.Builder
		pos := 0
		for {
			i := strings.Index(low[pos:], q)
			if i < 0 || q == "" {
				break
			}
			b.WriteString(style.Render(l[pos : pos+i]))
			b.WriteString(theme.Highlight.Render(l[pos+i : pos+i+len(q)]))
			pos += i + len(q)
		}
		b.WriteString(style.Render(l[pos:]))
		out := b.String()
		if current {
			return theme.AccentFg.Render("▌") + out
		}
		return out
	}
	if current {
		return theme.AccentFg.Render("▌") + style.Render(l)
	}
	return style.Render(l)
}

func (s *logsScreen) Hint() string {
	if s.searching {
		return keyHint("Enter", "find", "Esc", "cancel")
	}
	if s.running && s.fg {
		return theme.AccentFg.Render(s.env.Spin()) + " " + s.title + theme.Dim.Render(" — Esc cancel")
	}
	return keyHint("/", "search", "n/N", "next", "e", "next error", "L", "level", "w", "wrap", "y", "copy")
}

func (s *logsScreen) Keys() []KeyHelp {
	return []KeyHelp{
		{"↑↓ PgUp PgDn", "scroll"}, {"End / f", "follow"}, {"/", "search"}, {"n / N", "next / previous match"},
		{"e / E", "next / previous error"}, {"L", "level: all → warnings → errors"}, {"w", "wrap long lines"},
		{"y", "copy visible lines"}, {"r", "restart stream"}, {"Esc", "clear search / back"},
	}
}

func shortPath(p string) string {
	if p == "" {
		return ""
	}
	return filepath.Base(p)
}
