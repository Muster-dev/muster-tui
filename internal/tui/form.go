package tui

import (
	"strconv"
	"strings"

	"github.com/Muster-dev/muster-tui/internal/theme"
	tea "github.com/charmbracelet/bubbletea"
)

type fieldKind int

const (
	fText fieldKind = iota
	fInt
	fEnum
	fBool
	fHeading // non-editable section label
)

// field is one form row. Values are strings; bools are "true"/"false".
type field struct {
	key, label string
	kind       fieldKind
	opts       []string
	value      string
	help       string
	err        string
	hidden     bool
}

// form is a keyboard-driven list of fields.
type form struct {
	fields  []field
	sel     int
	editing bool
	input   lineInput
	dirty   bool
}

func newForm(fields []field) *form {
	f := &form{fields: fields, input: newLineInput()}
	f.sel = f.next(-1, 1)
	return f
}

func (f *form) visible(i int) bool {
	return i >= 0 && i < len(f.fields) && !f.fields[i].hidden && f.fields[i].kind != fHeading
}

func (f *form) next(from, dir int) int {
	for i := from + dir; i >= 0 && i < len(f.fields); i += dir {
		if f.visible(i) {
			return i
		}
	}
	if f.visible(from) {
		return from
	}
	return max(from, 0)
}

func (f *form) get(key string) string {
	for _, fl := range f.fields {
		if fl.key == key {
			return fl.value
		}
	}
	return ""
}

func (f *form) field(key string) *field {
	for i := range f.fields {
		if f.fields[i].key == key {
			return &f.fields[i]
		}
	}
	return nil
}

// HandleKey returns (handled, changed).
func (f *form) HandleKey(k tea.KeyMsg) (bool, bool) {
	if f.sel >= len(f.fields) {
		return false, false
	}
	fl := &f.fields[f.sel]
	if f.editing {
		switch k.String() {
		case "enter", "tab":
			v := strings.TrimSpace(f.input.Value())
			if fl.kind == fInt && v != "" {
				if _, err := strconv.Atoi(v); err != nil {
					fl.err = "must be a whole number"
					return true, false
				}
			}
			changed := v != fl.value
			fl.value, fl.err = v, ""
			f.editing = false
			if changed {
				f.dirty = true
			}
			return true, changed
		case "esc":
			f.editing = false
			return true, false
		}
		f.input, _ = f.input.Update(k)
		return true, false
	}
	switch k.String() {
	case "down", "j":
		f.sel = f.next(f.sel, 1)
	case "up", "k":
		f.sel = f.next(f.sel, -1)
	case "enter", " ", "right", "l", "left", "h":
		dir := 1
		if s := k.String(); s == "left" || s == "h" {
			dir = -1
		}
		switch fl.kind {
		case fEnum:
			fl.value = cycleDir(fl.opts, fl.value, dir)
			f.dirty = true
			return true, true
		case fBool:
			if fl.value == "true" {
				fl.value = "false"
			} else {
				fl.value = "true"
			}
			f.dirty = true
			return true, true
		case fText, fInt:
			if k.String() == "enter" || k.String() == " " {
				f.editing = true
				f.input.SetValue(fl.value)
				f.input.CursorEnd()
				f.input.Focus()
				return true, false
			}
			return false, false
		}
	default:
		return false, false
	}
	return true, false
}

func cycleDir(opts []string, cur string, dir int) string {
	if len(opts) == 0 {
		return cur
	}
	for i, o := range opts {
		if o == cur {
			return opts[(i+dir+len(opts))%len(opts)]
		}
	}
	return opts[0]
}

// Lines renders the form. focused=false dims the cursor.
func (f *form) Lines(w, labelW int, focused bool) []string {
	var out []string
	for i, fl := range f.fields {
		if fl.hidden {
			continue
		}
		if fl.kind == fHeading {
			if len(out) > 0 {
				out = append(out, "")
			}
			out = append(out, theme.Title.Render(fl.label))
			continue
		}
		val := fl.value
		switch fl.kind {
		case fBool:
			if val == "true" {
				val = theme.OK.Render("● on")
			} else {
				val = theme.Dim.Render("○ off")
			}
		case fEnum:
			val = theme.AccentFg.Render("‹ ") + val + theme.AccentFg.Render(" ›")
		default:
			if val == "" {
				val = theme.Dim.Render("—")
			}
		}
		cur := "  "
		label := theme.Dim.Render(fit(fl.label, labelW))
		if i == f.sel && focused {
			cur = theme.AccentFg.Render(theme.GlyphCursor) + " "
			label = theme.Base.Render(fit(fl.label, labelW))
			if f.editing {
				f.input.Width = max(w-labelW-4, 8)
				val = f.input.View()
			}
		}
		line := cur + label + " " + val
		var below string
		switch {
		case fl.err != "":
			line += "  " + theme.Err.Render(theme.GlyphFail+" "+fl.err)
		case i == f.sel && focused && fl.help != "" && !f.editing:
			// The help goes on its own line when it wouldn't fit beside.
			if width(line)+2+width(fl.help) <= w {
				line += "  " + theme.Dim.Render(fl.help)
			} else {
				below = strings.Repeat(" ", labelW+3) + theme.Dim.Render(fl.help)
			}
		}
		out = append(out, trunc(line, w))
		if below != "" {
			out = append(out, trunc(below, w))
		}
	}
	return out
}
