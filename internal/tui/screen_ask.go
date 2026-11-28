package tui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/Muster-dev/muster-tui/internal/engine"
	"github.com/Muster-dev/muster-tui/internal/theme"
	tea "github.com/charmbracelet/bubbletea"
)

// assistantReady mirrors the CLI's _assistant_available (lib/core/
// assistant.sh): muster ≥ 0.7, not switched off, signed in to Fleet Cloud —
// and `muster ask` hasn't told us otherwise this session.
func (e *Env) assistantReady() (bool, string) {
	st := e.State
	switch {
	case e.Engine == nil || !e.V07():
		return false, "needs muster " + engine.EngineAPIVersion
	case st.Cloud.AssistantOff:
		return false, "turned off on this machine"
	case !st.Cloud.HasToken:
		return false, "needs a Fleet Cloud login"
	case st.AssistantUnavailable != "":
		return false, st.AssistantUnavailable
	}
	return true, ""
}

// hasAssistantConsent reads .muster/assistant.json ({"consent": true}).
func hasAssistantConsent(dir string) bool {
	data, err := os.ReadFile(filepath.Join(dir, ".muster", "assistant.json"))
	if err != nil {
		return false
	}
	var c struct {
		Consent bool `json:"consent"`
	}
	return json.Unmarshal(data, &c) == nil && c.Consent
}

type askPhase int

const (
	askConsent  askPhase = iota // asking to send redacted data
	askInput                    // typing the first question
	askRunning                  // streaming
	askApproval                 // an action waits for y/N
	askWaiting                  // answer finished: follow-up or finish
	askDone
	askFailed
)

type askEntry struct {
	kind string // you, text, tool, result, approval, answer, note, error
	text string
}

type (
	askSessionMsg struct{ sess *engine.AskSession }
	askEventMsg   struct{ ev engine.AskEvent }
	askExitMsg    struct{ err error }
	askSendErrMsg struct{ err error }
)

// askScreen drives `muster ask --json`: streamed answer text, read tools as
// dim progress lines, and actions that wait for y/N before they run.
type askScreen struct {
	env      *Env
	question string
	readOnly bool
	failEnv  []string
	consent  bool

	phase    askPhase
	sess     *engine.AskSession
	taskID   int
	entries  []askEntry
	plan     string
	left     string
	pending  *engine.AskEvent
	input    lineInput
	errCode  string
	errMsg   string
	upgrade  string
	scroll   int
	lastErrs []string // stderr, for unexpected exits
}

func newAsk(env *Env, question string) *askScreen {
	ti := newLineInput()
	ti.Prompt = "› "
	ti.Placeholder = "What do you need help with?"
	s := &askScreen{env: env, question: strings.TrimSpace(question), input: ti}
	if env.State.Project != nil {
		s.consent = hasAssistantConsent(env.State.Project.Dir)
	}
	switch {
	case !s.consent:
		s.phase = askConsent
	case s.question == "":
		s.phase = askInput
		s.input.Focus()
	default:
		s.phase = askRunning
	}
	return s
}

// newAskForFailure explains a failed deploy (the CLI's "Explain with AI"):
// read-only, with the failure as context.
func newAskForFailure(env *Env, svc string, exit int, logFile string) *askScreen {
	s := newAsk(env, "Why did the "+svc+" deploy fail, and how do I fix it?")
	s.readOnly = true
	s.failEnv = []string{"MUSTER_ASK_FAILURE_SERVICE=" + svc, "MUSTER_ASK_FAILURE_EXIT=" + strconv.Itoa(exit), "MUSTER_ASK_FAILURE_LOG=" + logFile}
	return s
}

func (s *askScreen) Key() string   { return "ask" }
func (s *askScreen) Title() string { return "Assistant" }
func (s *askScreen) Capturing() bool {
	return s.phase == askInput || s.phase == askWaiting || s.phase == askApproval || s.phase == askConsent
}
func (s *askScreen) Persistent() bool { return s.live() }
func (s *askScreen) Close() {
	if s.sess != nil {
		s.sess.Close()
	}
	s.env.Tasks.Cancel(s.taskID)
}

func (s *askScreen) live() bool { return s.env.Tasks.Running(s.taskID) }

func (s *askScreen) Init() tea.Cmd {
	if s.phase == askRunning && s.taskID == 0 {
		return s.start()
	}
	return nil
}

func (s *askScreen) start() tea.Cmd {
	eng := s.env.Eng()
	if eng == nil {
		s.fail("no_cli", "The muster CLI isn't installed", "")
		return nil
	}
	s.phase = askRunning
	s.entries = append(s.entries, askEntry{"you", s.question})
	opt := engine.AskOptions{Question: s.question, Consent: s.consent, ReadOnly: s.readOnly, Env: s.failEnv}
	id, cmd := s.env.Tasks.Stream("asking the assistant", s.Key(), true, func(ctx context.Context) (<-chan tea.Msg, error) {
		sess, err := eng.Ask(ctx, opt)
		if err != nil {
			return nil, err
		}
		out := make(chan tea.Msg, 32)
		go func() {
			defer close(out)
			out <- askSessionMsg{sess}
			for ev := range sess.C {
				out <- askEventMsg{ev}
			}
			out <- askExitMsg{sess.Wait()}
		}()
		return out, nil
	})
	s.taskID = id
	return cmd
}

func (s *askScreen) fail(code, msg, upgrade string) {
	s.phase, s.errCode, s.errMsg, s.upgrade = askFailed, code, msg, upgrade
	switch code {
	case "not_logged_in", "unauthorized":
		s.env.State.AssistantUnavailable = "needs a Fleet Cloud login"
	case "disabled":
		s.env.State.AssistantUnavailable = "turned off on this machine"
	}
}

func (s *askScreen) send(f func(*engine.AskSession) error) tea.Cmd {
	sess := s.sess
	if sess == nil {
		return nil
	}
	return func() tea.Msg {
		if err := f(sess); err != nil {
			return askSendErrMsg{err}
		}
		return nil
	}
}

func (s *askScreen) addText(delta string) {
	if n := len(s.entries); n > 0 && s.entries[n-1].kind == "text" {
		s.entries[n-1].text += delta
		return
	}
	s.entries = append(s.entries, askEntry{"text", delta})
}

func (s *askScreen) onEvent(ev engine.AskEvent) tea.Cmd {
	switch ev.Type {
	case "session":
		s.plan, s.left = ev.Plan, ev.RemainingSessions()
	case "text":
		s.addText(ev.Delta)
		s.scroll = 0
	case "thinking_summary":
		if t := strings.TrimSpace(ev.Delta); t != "" {
			s.entries = append(s.entries, askEntry{"note", t})
		}
	case "tool_running":
		txt := firstNonEmpty(ev.Summary, "Running "+ev.Name+"…")
		if n := len(s.entries); n > 0 && s.entries[n-1].kind == "answer" && strings.HasSuffix(s.entries[n-1].text, ": "+txt) {
			break // the approved action is already on screen
		}
		s.entries = append(s.entries, askEntry{"tool", txt})
	case "tool_result":
		line := strings.TrimSpace(firstLineOf(ev.Content))
		if ev.IsError {
			s.entries = append(s.entries, askEntry{"error", ev.Name + ": " + line})
		} else if ev.Name != "" {
			s.entries = append(s.entries, askEntry{"result", ev.Name})
		}
	case "approval_needed":
		e := ev
		s.pending = &e
		s.phase = askApproval
		s.scroll = 0
		return toastIfAway(s.env, s.Key(), "The assistant wants to run: "+ev.Summary+" — type ask to answer")
	case "awaiting_input":
		s.phase = askWaiting
		s.input.SetValue("")
		s.input.Placeholder = "Ask a follow-up, or press Enter to finish"
		s.input.Focus()
	case "done":
		s.phase = askDone
	case "cancelled":
		s.entries = append(s.entries, askEntry{"note", "Cancelled."})
		s.phase = askDone
	case "error":
		code := ev.Err()
		if code == "consent_required" {
			s.phase, s.consent = askConsent, false
			return nil
		}
		s.fail(code, firstNonEmpty(ev.Message, code), ev.UpgradeURL)
	case "stderr":
		if t := strings.TrimSpace(ev.Message); t != "" {
			s.lastErrs = append(s.lastErrs, t)
			if len(s.lastErrs) > 3 {
				s.lastErrs = s.lastErrs[1:]
			}
		}
	}
	return nil
}

func toastIfAway(env *Env, key, text string) tea.Cmd {
	if env.Active == key {
		return nil
	}
	return toast(lvlWarn, text)
}

func firstLineOf(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func (s *askScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch m := msg.(type) {
	case TaskMsg:
		if m.ID != s.taskID {
			return s, nil
		}
		switch in := m.Msg.(type) {
		case askSessionMsg:
			s.sess = in.sess
		case askEventMsg:
			return s, s.onEvent(in.ev)
		case askExitMsg:
			if in.err != nil && s.phase != askFailed && s.phase != askConsent && s.phase != askDone {
				msg := in.err.Error()
				if len(s.lastErrs) > 0 {
					msg = s.lastErrs[len(s.lastErrs)-1]
				}
				s.fail("exit", msg, "")
			}
		case askSendErrMsg:
			s.entries = append(s.entries, askEntry{"error", in.err.Error()})
		case taskErrMsg:
			s.fail("start", in.Err.Error(), "")
		}
	case askSendErrMsg:
		s.entries = append(s.entries, askEntry{"error", m.err.Error()})
	case TaskEndedMsg:
		if m.ID == s.taskID {
			if s.sess != nil {
				s.sess.Close()
			}
			if m.Canceled && s.phase != askFailed {
				s.entries = append(s.entries, askEntry{"note", "Cancelled."})
			}
			if s.phase == askRunning || s.phase == askApproval || s.phase == askWaiting {
				s.phase = askDone
			}
		}
	}
	return s, nil
}

func (s *askScreen) HandleKey(k tea.KeyMsg) (Screen, tea.Cmd, bool) {
	key := k.String()
	switch s.phase {
	case askConsent:
		switch key {
		case "y", "Y":
			s.consent = true
			if s.question == "" {
				s.phase = askInput
				s.input.Focus()
				return s, nil, true
			}
			return s, s.start(), true
		case "n", "N", "esc", "enter":
			return s, tea.Batch(toast(lvlInfo, "Nothing was sent."), func() tea.Msg { return backMsg{} }), true
		}
		return s, nil, true
	case askInput, askWaiting:
		switch key {
		case "enter":
			q := strings.TrimSpace(s.input.Value())
			if s.phase == askInput {
				if q == "" {
					return s, nil, true
				}
				s.question = q
				s.input.SetValue("")
				return s, s.start(), true
			}
			if q == "" {
				s.phase = askDone
				return s, s.send(func(a *engine.AskSession) error { return a.End() }), true
			}
			s.input.SetValue("")
			s.entries = append(s.entries, askEntry{"you", q})
			s.phase = askRunning
			s.scroll = 0
			return s, s.send(func(a *engine.AskSession) error { return a.Message(q) }), true
		case "esc":
			if s.phase == askWaiting {
				s.phase = askDone
				return s, tea.Batch(s.send(func(a *engine.AskSession) error { return a.End() }), func() tea.Msg { return backMsg{} }), true
			}
			return s, func() tea.Msg { return backMsg{} }, true
		case "pgup":
			s.scroll += 5
			return s, nil, true
		case "pgdown":
			s.scroll = max(s.scroll-5, 0)
			return s, nil, true
		}
		var cmd tea.Cmd
		s.input, cmd = s.input.Update(k)
		return s, cmd, true
	case askApproval:
		if s.pending == nil {
			s.phase = askRunning
			return s, nil, true
		}
		id, summary := s.pending.ToolUseID, s.pending.Summary
		switch key {
		case "y", "Y":
			s.pending, s.phase = nil, askRunning
			s.entries = append(s.entries, askEntry{"answer", "Approved: " + summary})
			return s, s.send(func(a *engine.AskSession) error { return a.Approve(id) }), true
		case "n", "N", "enter", "esc":
			s.pending, s.phase = nil, askRunning
			s.entries = append(s.entries, askEntry{"answer", "Declined: " + summary})
			return s, s.send(func(a *engine.AskSession) error { return a.Decline(id) }), true
		case "pgup":
			s.scroll += 5
		case "pgdown":
			s.scroll = max(s.scroll-5, 0)
		}
		return s, nil, true
	case askRunning:
		switch key {
		case "esc":
			s.env.Tasks.Cancel(s.taskID)
			return s, toast(lvlWarn, "Stopping the assistant…"), true
		case "pgup":
			s.scroll += 5
			return s, nil, true
		case "pgdown":
			s.scroll = max(s.scroll-5, 0)
			return s, nil, true
		}
	case askDone, askFailed:
		switch key {
		case "esc", "enter":
			return s, func() tea.Msg { return backMsg{} }, true
		case "n":
			return s, nav(newAsk(s.env, "")), true
		case "l":
			if s.errCode == "not_logged_in" || s.errCode == "unauthorized" {
				return s, runLine("cloud login"), true
			}
		case "c":
			if s.upgrade != "" {
				return s, copyCmd(s.upgrade, "Copied the upgrade link"), true
			}
		case "pgup":
			s.scroll += 5
			return s, nil, true
		case "pgdown":
			s.scroll = max(s.scroll-5, 0)
			return s, nil, true
		}
	}
	return s, nil, false
}

var (
	mdBold = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	mdCode = regexp.MustCompile("`([^`]+)`")
)

// mdLine renders the little markdown the assistant uses (bold, code,
// headings, bullets) on one already-wrapped line.
func mdLine(l string) string {
	t := strings.TrimSpace(l)
	if strings.HasPrefix(t, "#") {
		return theme.Title.Render(strings.TrimSpace(strings.TrimLeft(t, "#")))
	}
	l = mdBold.ReplaceAllStringFunc(l, func(m string) string { return theme.Title.Render(m[2 : len(m)-2]) })
	l = mdCode.ReplaceAllStringFunc(l, func(m string) string { return code(m[1 : len(m)-1]) })
	return l
}

func (s *askScreen) transcript(w int) []string {
	var out []string
	for _, e := range s.entries {
		switch e.kind {
		case "you":
			out = append(out, "")
			for i, l := range wrap(e.text, w-4) {
				lead := "    "
				if i == 0 {
					lead = theme.AccentFg.Render("you ")
				}
				out = append(out, lead+theme.Base.Render(l))
			}
			out = append(out, "")
		case "text":
			for _, para := range strings.Split(strings.TrimRight(e.text, "\n"), "\n") {
				if strings.TrimSpace(para) == "" {
					out = append(out, "")
					continue
				}
				indent := ""
				if strings.HasPrefix(strings.TrimSpace(para), "- ") || strings.HasPrefix(strings.TrimSpace(para), "* ") {
					para = "• " + strings.TrimSpace(para)[2:]
					indent = "  "
				}
				for i, l := range wrap(para, w-2-len(indent)) {
					if i > 0 {
						l = indent + l
					}
					out = append(out, "  "+mdLine(l))
				}
			}
		case "tool":
			out = append(out, "  "+theme.Dim.Render(trunc(e.text, w-2)))
		case "result":
			out = append(out, "  "+theme.Dim.Render(theme.GlyphOK+" "+trunc(e.text, w-4)))
		case "error":
			out = append(out, "  "+theme.Err.Render(theme.GlyphFail+" ")+theme.Dim.Render(trunc(e.text, w-4)))
		case "answer":
			out = append(out, "  "+theme.Dim.Render(trunc(e.text, w-2)))
		case "note":
			out = append(out, "  "+theme.Dim.Render(trunc(e.text, w-2)))
		}
	}
	return out
}

// Status: plan and sessions left, on the right of the header.
func (s *askScreen) Status() string {
	right := ""
	if s.plan != "" {
		right = s.plan + " plan"
	}
	if s.left != "" {
		right += " · " + s.left + " sessions left"
	}
	if s.readOnly {
		right += " · read-only"
	}
	return theme.Dim.Render(strings.TrimPrefix(right, " · "))
}

func (s *askScreen) View(w, h int) string {
	head := []string{""}

	var foot []string
	switch s.phase {
	case askConsent:
		name := "this project"
		if p := s.env.State.Project; p != nil {
			name = p.Name()
		}
		lines := append(head, "")
		lines = append(lines, wrap("Send redacted logs and config from "+name+" to the Muster Assistant?", w)...)
		lines = append(lines, "", theme.Dim.Render("Secrets, tokens and .env values are removed on this machine before anything is sent."),
			theme.Dim.Render("Your answer is saved in .muster/assistant.json (kept out of git)."))
		return block(lines, w, h)
	case askInput:
		s.input.Width = w
		lines := append(head, "", theme.Title.Render("What do you need help with?"), s.input.View(), "",
			theme.Dim.Render("e.g. why did api fail? · is web healthy? · what changed since the last deploy?"))
		return block(lines, w, h)
	case askApproval:
		if p := s.pending; p != nil {
			foot = append(foot, rule(w), theme.Warning.Render("▸ ")+theme.Title.Render(trunc(p.Summary, w-2)))
			if p.Diff != "" {
				for _, l := range tail(splitLines(p.Diff), 8) {
					foot = append(foot, styleDiff(l, w))
				}
			}
			foot = append(foot, theme.Warning.Render("? ")+"Run this? "+theme.Dim.Render("[y/N]"))
		}
	case askWaiting:
		s.input.Width = w
		foot = append(foot, rule(w), s.input.View())
	case askRunning:
		foot = append(foot, theme.AccentFg.Render(s.env.Spin())+theme.Dim.Render(" thinking…"))
	case askFailed:
		foot = append(foot, rule(w))
		foot = append(foot, theme.Err.Render(theme.GlyphFail+" ")+trunc(s.errMsg, w-2))
		switch s.errCode {
		case "not_logged_in", "unauthorized":
			foot = append(foot, "  Sign in to Fleet Cloud: "+code("cloud login")+theme.Dim.Render(" · l"))
		case "payment_required":
			foot = append(foot, "  Upgrade to keep going: "+theme.Base.Render(trunc(s.upgrade, w-26))+theme.Dim.Render(" · c copy"))
		case "disabled":
			foot = append(foot, theme.Dim.Render("  Turn it on: muster settings --global assistant on"))
		case "network_error":
			foot = append(foot, theme.Dim.Render("  Check your connection and try again."))
		}
	}

	body := s.transcript(w)
	avail := max(h-len(head)-len(foot), 0)
	end := max(len(body)-s.scroll, 0)
	start := max(end-avail, 0)
	lines := append(head, body[start:end]...)
	for len(lines) < h-len(foot) {
		lines = append(lines, "")
	}
	return block(append(lines, foot...), w, h)
}

func (s *askScreen) Hint() string { return "" }

func (s *askScreen) Footer() []KeyHelp {
	switch s.phase {
	case askConsent:
		return []KeyHelp{{"y", "send redacted data"}, {"n", "cancel — nothing is sent"}}
	case askInput:
		return []KeyHelp{{"enter", "ask"}, {"esc", "back"}}
	case askApproval:
		return []KeyHelp{{"y", "run it"}, {"n", "decline"}}
	case askRunning:
		return []KeyHelp{{"pgup", "scroll"}, {"esc", "stop"}}
	case askWaiting:
		return []KeyHelp{{"enter", "send / finish"}, {"pgup", "scroll"}, {"esc", "finish"}}
	case askFailed:
		switch s.errCode {
		case "not_logged_in", "unauthorized":
			return []KeyHelp{{"l", "cloud login"}, {"esc", "back"}}
		case "payment_required":
			return []KeyHelp{{"c", "copy upgrade link"}, {"esc", "back"}}
		}
		return []KeyHelp{{"n", "try again"}, {"esc", "back"}}
	case askDone:
		return []KeyHelp{{"n", "new question"}, {"pgup", "scroll"}, {"esc", "back"}}
	}
	return nil
}

func (s *askScreen) Keys() []KeyHelp {
	return []KeyHelp{{"y / n", "run / decline a proposed action"}, {"Enter", "send a follow-up (empty: finish)"}, {"PgUp PgDn", "scroll"}, {"n", "new question (when finished)"}, {"Esc", "stop / back"}}
}
