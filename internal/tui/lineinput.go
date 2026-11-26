package tui

import (
	"strings"
	"unicode"

	"github.com/Muster-dev/muster-tui/internal/theme"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

// lineInput is a minimal single-line editor. (bubbles/textinput queries the
// terminal background at package init, which can stall startup.)
type lineInput struct {
	value       []rune
	pos         int
	focused     bool
	Prompt      string
	Placeholder string
	Width       int
	CharLimit   int
	TextStyle   lipgloss.Style
}

func newLineInput() lineInput {
	return lineInput{TextStyle: theme.Base, CharLimit: 512}
}

func (l *lineInput) Value() string { return string(l.value) }

func (l *lineInput) SetValue(s string) {
	l.value = []rune(s)
	if l.CharLimit > 0 && len(l.value) > l.CharLimit {
		l.value = l.value[:l.CharLimit]
	}
	l.pos = clamp(l.pos, 0, len(l.value))
}

func (l *lineInput) CursorEnd() { l.pos = len(l.value) }
func (l *lineInput) Focus()     { l.focused = true }
func (l *lineInput) Blur()      { l.focused = false }

func (l *lineInput) insert(rs []rune) {
	var clean []rune
	for _, r := range rs {
		if r == '\n' || r == '\r' || r == '\t' {
			r = ' '
		}
		if unicode.IsPrint(r) {
			clean = append(clean, r)
		}
	}
	if l.CharLimit > 0 && len(l.value)+len(clean) > l.CharLimit {
		clean = clean[:max(l.CharLimit-len(l.value), 0)]
	}
	l.value = append(l.value[:l.pos], append(clean, l.value[l.pos:]...)...)
	l.pos += len(clean)
}

func (l *lineInput) wordLeft() int {
	i := l.pos
	for i > 0 && l.value[i-1] == ' ' {
		i--
	}
	for i > 0 && l.value[i-1] != ' ' {
		i--
	}
	return i
}

func (l *lineInput) wordRight() int {
	i := l.pos
	for i < len(l.value) && l.value[i] == ' ' {
		i++
	}
	for i < len(l.value) && l.value[i] != ' ' {
		i++
	}
	return i
}

// Update applies an editing key.
func (l lineInput) Update(msg tea.Msg) (lineInput, tea.Cmd) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return l, nil
	}
	switch k.String() {
	case "left", "ctrl+b":
		l.pos = max(l.pos-1, 0)
	case "right", "ctrl+f":
		l.pos = min(l.pos+1, len(l.value))
	case "home", "ctrl+a":
		l.pos = 0
	case "end", "ctrl+e":
		l.pos = len(l.value)
	case "alt+left", "alt+b", "ctrl+left":
		l.pos = l.wordLeft()
	case "alt+right", "alt+f", "ctrl+right":
		l.pos = l.wordRight()
	case "backspace", "ctrl+h":
		if l.pos > 0 {
			l.value = append(l.value[:l.pos-1], l.value[l.pos:]...)
			l.pos--
		}
	case "delete", "ctrl+d":
		if l.pos < len(l.value) {
			l.value = append(l.value[:l.pos], l.value[l.pos+1:]...)
		}
	case "ctrl+w", "alt+backspace":
		i := l.wordLeft()
		l.value = append(l.value[:i], l.value[l.pos:]...)
		l.pos = i
	case "ctrl+u":
		l.value = l.value[l.pos:]
		l.pos = 0
	default:
		if k.Type == tea.KeyRunes || k.Type == tea.KeySpace {
			rs := k.Runes
			if k.Type == tea.KeySpace {
				rs = []rune{' '}
			}
			l.insert(rs)
		}
	}
	return l, nil
}

// View renders the input, scrolled so the cursor stays visible.
func (l lineInput) View() string {
	prompt := l.Prompt
	w := l.Width - runewidth.StringWidth(prompt)
	if w <= 0 {
		w = 80
	}
	if len(l.value) == 0 {
		cur := ""
		if l.focused {
			cur = theme.Highlight.Render(" ")
			if theme.Plain() {
				cur = "▏"
			}
		}
		ph := ""
		if l.Placeholder != "" {
			ph = theme.Dim.Render(trunc(l.Placeholder, w-1))
		}
		return prompt + cur + ph
	}
	// Window of runes around the cursor that fits in w cells.
	start := 0
	for runewidth.StringWidth(string(l.value[start:l.pos]))+1 > w && start < l.pos {
		start++
	}
	end := l.pos
	for end < len(l.value) && runewidth.StringWidth(string(l.value[start:end+1]))+1 <= w {
		end++
	}
	before := string(l.value[start:l.pos])
	var b strings.Builder
	b.WriteString(prompt)
	b.WriteString(l.TextStyle.Render(before))
	if l.focused && theme.Plain() {
		b.WriteString("▏")
		b.WriteString(l.TextStyle.Render(string(l.value[l.pos:end])))
	} else if l.focused {
		ch := " "
		if l.pos < len(l.value) {
			ch = string(l.value[l.pos])
		}
		b.WriteString(theme.Highlight.Render(ch))
		if l.pos < end {
			b.WriteString(l.TextStyle.Render(string(l.value[l.pos+1 : end])))
		}
	} else {
		b.WriteString(l.TextStyle.Render(string(l.value[l.pos:end])))
	}
	return b.String()
}
