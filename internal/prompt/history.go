package prompt

import (
	"os"
	"path/filepath"
	"strings"
)

// History is the prompt's command history, persisted one entry per line.
type History struct {
	path  string
	items []string
	max   int
	pos   int // len(items) = not browsing
	draft string
}

// LoadHistory reads path ("" = in-memory only).
func LoadHistory(path string) *History {
	h := &History{path: path, max: 500}
	if path != "" {
		if data, err := os.ReadFile(path); err == nil {
			for _, l := range strings.Split(string(data), "\n") {
				if l = strings.TrimSpace(l); l != "" {
					h.items = append(h.items, l)
				}
			}
		}
	}
	if len(h.items) > h.max {
		h.items = h.items[len(h.items)-h.max:]
	}
	h.pos = len(h.items)
	return h
}

// Items returns entries oldest first.
func (h *History) Items() []string { return append([]string(nil), h.items...) }

// Add appends an entry (skipping consecutive duplicates) and saves.
func (h *History) Add(s string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return
	}
	if n := len(h.items); n == 0 || h.items[n-1] != s {
		h.items = append(h.items, s)
		if len(h.items) > h.max {
			h.items = h.items[len(h.items)-h.max:]
		}
		h.save()
	}
	h.Reset()
}

// Reset stops browsing.
func (h *History) Reset() { h.pos, h.draft = len(h.items), "" }

// Prev returns the previous entry, remembering the current draft.
func (h *History) Prev(current string) (string, bool) {
	if h.pos == 0 || len(h.items) == 0 {
		return current, false
	}
	if h.pos == len(h.items) {
		h.draft = current
	}
	h.pos--
	return h.items[h.pos], true
}

// Next moves forward; past the newest entry it restores the draft.
func (h *History) Next() (string, bool) {
	if h.pos >= len(h.items) {
		return "", false
	}
	h.pos++
	if h.pos == len(h.items) {
		return h.draft, true
	}
	return h.items[h.pos], true
}

// Search finds the newest entry containing query, starting below index
// before (use len(Items()) to start from the newest). Returns -1 if none.
func (h *History) Search(query string, before int) (int, string) {
	if before > len(h.items) {
		before = len(h.items)
	}
	q := strings.ToLower(query)
	for i := before - 1; i >= 0; i-- {
		if strings.Contains(strings.ToLower(h.items[i]), q) {
			return i, h.items[i]
		}
	}
	return -1, ""
}

func (h *History) save() {
	if h.path == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(h.path), 0o700)
	_ = os.WriteFile(h.path, []byte(strings.Join(h.items, "\n")+"\n"), 0o600)
}
