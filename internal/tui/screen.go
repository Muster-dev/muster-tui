package tui

import (
	"os/exec"

	tea "github.com/charmbracelet/bubbletea"
)

// Screen is what the main pane shows.
type Screen interface {
	Key() string
	Title() string
	Init() tea.Cmd
	// Update receives every non-key message (broadcast to all live screens).
	Update(msg tea.Msg) (Screen, tea.Cmd)
	// HandleKey receives keys when the screen is active; handled=false lets
	// the app apply its fallbacks (focus the prompt, insert the character…).
	HandleKey(k tea.KeyMsg) (s Screen, cmd tea.Cmd, handled bool)
	View(w, h int) string
	// Hint overrides the suggestion bar ("" = let the app decide).
	Hint() string
	Keys() []KeyHelp
}

// Capturer screens are in a text-entry mode: every key goes to them.
type Capturer interface{ Capturing() bool }

// Closer screens release resources (cancel their tasks) when evicted.
type Closer interface{ Close() }

// Persistent screens stay cached when you navigate away (deploys keep
// running in the background).
type Persistent interface{ Persistent() bool }

// KeyHelp is one help entry.
type KeyHelp struct{ Key, Desc string }

// Navigation and app-level messages screens can return.
type (
	navMsg     struct{ screen Screen }
	backMsg    struct{}
	runLineMsg struct{ line string }
	prefillMsg struct{ text string }
	toastMsg   struct {
		text  string
		level int // 0 info, 1 ok, 2 warn, 3 error
	}
	refreshMsg      struct{ what string } // "status", "history", "docker", "fleet", "all"
	reloadConfigMsg struct{}
	execMsg         struct {
		cmd   *exec.Cmd
		after func(error) tea.Msg
	}
	confirmMsg struct {
		title string
		body  []string
		yes   string
		onYes tea.Cmd
	}
	fullscreenMsg struct{ on bool }
)

func nav(s Screen) tea.Cmd        { return func() tea.Msg { return navMsg{s} } }
func runLine(line string) tea.Cmd { return func() tea.Msg { return runLineMsg{line} } }
func toast(level int, text string) tea.Cmd {
	return func() tea.Msg { return toastMsg{text: text, level: level} }
}
func refresh(what string) tea.Cmd { return func() tea.Msg { return refreshMsg{what} } }

const (
	lvlInfo = iota
	lvlOK
	lvlWarn
	lvlErr
)
