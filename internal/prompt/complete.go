package prompt

import (
	"sort"
	"strings"
	"unicode"
)

// Item is one completion candidate.
type Item struct {
	Value string // text that replaces the word being completed
	Label string // what the popup shows
	Desc  string
	Score int
}

// Completion is the candidate list for the word at the end of the input.
type Completion struct {
	Start int // byte offset in the input where Value replaces text
	Items []Item
}

// Apply returns the input with item i applied.
func (c Completion) Apply(input string, i int) string {
	if i < 0 || i >= len(c.Items) || c.Start > len(input) {
		return input
	}
	return input[:c.Start] + c.Items[i].Value
}

// Complete returns fuzzy-ranked candidates for the last word of input.
func Complete(input string, ctx Context) Completion {
	start := 0
	if strings.HasPrefix(input, "/") {
		start = 1
	}
	body := input[start:]
	lastSpace := strings.LastIndexAny(body, " \t")
	if lastSpace < 0 {
		return Completion{Start: start, Items: completeCommands(body)}
	}
	word := body[lastSpace+1:]
	wordStart := start + lastSpace + 1
	words := strings.Fields(body[:lastSpace])
	var kept []string
	for i, w := range words {
		if strings.HasPrefix(w, "-") || (i > 0 && fillers[strings.ToLower(w)]) {
			continue
		}
		kept = append(kept, w)
	}
	if len(kept) == 0 {
		return Completion{Start: wordStart}
	}
	head := strings.ToLower(kept[0])
	// Multi-word command family ("fleet ", "cloud ")
	if len(kept) == 1 {
		var subs []Item
		for _, c := range Commands {
			if rest, ok := strings.CutPrefix(c.Name, head+" "); ok {
				if s, ok := fuzzyScore(word, rest); ok {
					val := rest
					if c.Args != ArgNone {
						val += " "
					}
					subs = append(subs, Item{Value: val, Label: rest, Desc: c.Summary, Score: s})
				}
			}
		}
		if len(subs) > 0 {
			return Completion{Start: wordStart, Items: rank(subs)}
		}
	}
	cmd, n, ok := matchCommand(kept)
	if !ok {
		return Completion{Start: wordStart}
	}
	used := map[string]bool{}
	for _, a := range kept[n:] {
		used[a] = true
	}
	var pool []string
	var desc string
	switch cmd.Args {
	case ArgService, ArgServices:
		if cmd.Args == ArgService && len(kept) > n && cmd.Name != "edit" {
			break
		}
		if cmd.Name == "edit" && len(kept) > n {
			pool, desc = []string{"deploy", "health", "rollback", "logs", "cleanup"}, "hook"
			break
		}
		pool, desc = ctx.Services, "service"
	case ArgMachine:
		if len(kept) == n {
			pool, desc = ctx.Machines, "machine"
		}
	case ArgTarget:
		if len(kept) == n {
			pool = append(append([]string{}, ctx.Machines...), ctx.Groups...)
			desc = "machine or group"
		}
	case ArgContainer:
		if len(kept) == n {
			pool, desc = ctx.Containers, "container"
		}
	case ArgText:
		if cmd.Name == "prune" && len(kept) == n {
			pool, desc = []string{"images", "containers"}, "what to prune"
		}
	}
	var items []Item
	if strings.HasPrefix(word, "-") {
		for _, f := range cmd.Flags {
			if s, ok := fuzzyScore(word, f); ok {
				items = append(items, Item{Value: f + " ", Label: f, Desc: "flag", Score: s})
			}
		}
		return Completion{Start: wordStart, Items: rank(items)}
	}
	for _, p := range pool {
		if used[p] {
			continue
		}
		if s, ok := fuzzyScore(word, p); ok {
			items = append(items, Item{Value: p + " ", Label: p, Desc: desc, Score: s})
		}
	}
	return Completion{Start: wordStart, Items: rank(items)}
}

func completeCommands(word string) []Item {
	var items []Item
	for _, c := range Commands {
		if c.Hidden && word == "" {
			continue
		}
		best, ok := fuzzyScore(word, c.Name)
		for _, a := range c.Aliases {
			if s, ok2 := fuzzyScore(word, a); ok2 && (!ok || s > best) {
				best, ok = s-1, true // aliases rank just under real names
			}
		}
		if !ok {
			continue
		}
		val := c.Name
		if c.Args != ArgNone {
			val += " "
		}
		items = append(items, Item{Value: val, Label: c.Name, Desc: c.Summary, Score: best})
	}
	if word == "" {
		return items // registry order
	}
	return rank(items)
}

func rank(items []Item) []Item {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Score != items[j].Score {
			return items[i].Score > items[j].Score
		}
		return len(items[i].Label) < len(items[j].Label)
	})
	return items
}

// fuzzyScore matches query as a subsequence of target (case-insensitive).
// Prefix matches, consecutive runs and word-boundary hits score higher.
func fuzzyScore(query, target string) (int, bool) {
	if query == "" {
		return 0, true
	}
	q := []rune(strings.ToLower(query))
	t := []rune(strings.ToLower(target))
	score, qi, prev := 0, 0, -2
	for ti := 0; ti < len(t) && qi < len(q); ti++ {
		if t[ti] != q[qi] {
			continue
		}
		// The first letter has to start a word ("lo" → logs, not deploy).
		if qi == 0 && ti > 0 && (unicode.IsLetter(t[ti-1]) || unicode.IsDigit(t[ti-1])) {
			continue
		}
		s := 1
		if ti == prev+1 {
			s += 5
		}
		if ti == 0 {
			s += 10
		} else if !unicode.IsLetter(t[ti-1]) && !unicode.IsDigit(t[ti-1]) {
			s += 6
		}
		score += s
		prev = ti
		qi++
	}
	if qi < len(q) {
		return 0, false
	}
	if strings.HasPrefix(string(t), string(q)) {
		score += 20
	}
	score -= len(t) - len(q) // tighter matches first
	return score, true
}

// FuzzyScore matches query as a subsequence of target (case-insensitive),
// ranking prefix, word-start and consecutive hits higher.
func FuzzyScore(query, target string) (int, bool) { return fuzzyScore(query, target) }
