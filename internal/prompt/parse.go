package prompt

import (
	"fmt"
	"sort"
	"strings"
)

// Context is what the parser and completer know about the world.
type Context struct {
	Services   []string
	Machines   []string
	Groups     []string
	Containers []string
}

// Parsed is a successfully parsed input line.
type Parsed struct {
	Cmd   Command
	Args  []string
	Flags map[string]bool
	Input string
}

// Has reports whether a flag was given.
func (p Parsed) Has(flag string) bool { return p.Flags[flag] }

// Error is a parse failure with an optional corrected command line.
type Error struct {
	Msg        string
	Suggestion string
}

func (e *Error) Error() string {
	if e.Suggestion != "" {
		return fmt.Sprintf("%s — did you mean `%s`?", e.Msg, e.Suggestion)
	}
	return e.Msg
}

var fillers = map[string]bool{"for": true, "the": true, "on": true, "please": true, "of": true, "to": true, "service": true, "services": true, "my": true}

// Parse turns an input line into a command. It accepts slash forms
// ("/deploy"), multi-word commands ("fleet deploy"), unique prefixes
// ("dep api"), filler words ("logs for api") and "all"/"everything".
func Parse(input string, ctx Context) (Parsed, error) {
	line := strings.TrimSpace(input)
	line = strings.TrimPrefix(line, "/")
	if line == "" {
		return Parsed{}, &Error{Msg: "Type a command, or press / to see them"}
	}
	var words []string
	flags := map[string]bool{}
	for i, w := range strings.Fields(line) {
		if strings.HasPrefix(w, "-") && len(w) > 1 {
			flags[normalizeFlag(w)] = true
			continue
		}
		lw := strings.ToLower(w)
		if i > 0 && fillers[lw] {
			continue
		}
		words = append(words, w)
	}
	if len(words) == 0 {
		return Parsed{}, &Error{Msg: "Type a command, or press / to see them"}
	}
	cmd, n, ok := matchCommand(words)
	if !ok {
		return Parsed{}, unknownCommand(words, flags, ctx)
	}
	p := Parsed{Cmd: cmd, Args: words[n:], Flags: flags, Input: input}
	for f := range flags {
		if !containsStr(cmd.Flags, f) {
			return p, &Error{Msg: fmt.Sprintf("`%s` doesn't take %s", cmd.Name, f)}
		}
	}
	if p.Cmd.Name == "dry-run" {
		p.Flags["--dry-run"] = true
	}
	return p, validateArgs(p, ctx)
}

func normalizeFlag(f string) string {
	switch f {
	case "-n", "--dryrun", "--plan":
		return "--dry-run"
	case "-f":
		return "--force"
	}
	return strings.ToLower(f)
}

// matchCommand finds the command for the leading words; returns how many
// words it consumed.
func matchCommand(words []string) (Command, int, bool) {
	first := strings.ToLower(words[0])
	if len(words) >= 2 {
		if c, ok := Lookup(first + " " + strings.ToLower(words[1])); ok {
			return c, 2, true
		}
		// "fleet dep" → unique prefix among "fleet …" subcommands
		var hits []Command
		for _, c := range Commands {
			if strings.HasPrefix(c.Name, first+" ") && strings.HasPrefix(strings.TrimPrefix(c.Name, first+" "), strings.ToLower(words[1])) {
				hits = append(hits, c)
			}
		}
		if len(hits) == 1 {
			return hits[0], 2, true
		}
	}
	if c, ok := Lookup(first); ok {
		return c, 1, true
	}
	// unique prefix of a single-word command name
	var hits []Command
	for _, c := range Commands {
		if !strings.Contains(c.Name, " ") && strings.HasPrefix(c.Name, first) && len(first) >= 2 {
			hits = append(hits, c)
		}
	}
	if len(hits) == 1 {
		return hits[0], 1, true
	}
	return Command{}, 0, false
}

func unknownCommand(words []string, flags map[string]bool, ctx Context) error {
	first := strings.ToLower(words[0])
	best, bestD := "", 99
	for _, name := range allNames() {
		head := strings.Split(name, " ")[0]
		if d := distance(first, head); d < bestD {
			best, bestD = name, d
		}
	}
	msg := fmt.Sprintf("I don't know `%s`", words[0])
	// "api logs" → "logs api"
	if len(words) >= 2 && containsStr(ctx.Services, words[0]) {
		if c, ok := Lookup(strings.ToLower(words[1])); ok {
			return &Error{Msg: msg, Suggestion: c.Name + " " + words[0]}
		}
	}
	if best == "" || bestD > maxTypos(first) {
		return &Error{Msg: msg + " — type / to see commands"}
	}
	head := strings.Split(best, " ")[0]
	rest := words[1:]
	if strings.Contains(best, " ") {
		if len(rest) > 0 && strings.HasPrefix(best, head+" "+strings.ToLower(rest[0])) {
			rest = rest[1:]
		}
	}
	sugg := strings.Join(append([]string{best}, fixArgs(rest, ctx)...), " ")
	for _, f := range sortedKeys(flags) {
		sugg += " " + f
	}
	return &Error{Msg: msg, Suggestion: sugg}
}

func fixArgs(args []string, ctx Context) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = a
		if !containsStr(ctx.Services, a) {
			if s, ok := closest(a, ctx.Services); ok {
				out[i] = s
			}
		}
	}
	return out
}

func validateArgs(p Parsed, ctx Context) error {
	switch p.Cmd.Args {
	case ArgNone, ArgSub:
		if len(p.Args) > 0 {
			return &Error{Msg: fmt.Sprintf("`%s` doesn't take arguments", p.Cmd.Name), Suggestion: p.Cmd.Name}
		}
	case ArgService, ArgServices:
		if p.Cmd.Args == ArgService && len(p.Args) > 1 && p.Cmd.Name != "edit" {
			return &Error{Msg: fmt.Sprintf("`%s` takes one service", p.Cmd.Name), Suggestion: p.Cmd.Name + " " + p.Args[0]}
		}
		for i, a := range p.Args {
			if p.Cmd.Name == "edit" && i > 0 {
				break
			}
			if (a == "all" || a == "everything") && p.Cmd.Args == ArgServices {
				continue
			}
			if len(ctx.Services) == 0 {
				return &Error{Msg: "No services yet — run `setup` first"}
			}
			if !containsStr(ctx.Services, a) {
				fixed := append([]string{p.Cmd.Name}, fixArgs(p.Args, ctx)...)
				e := &Error{Msg: fmt.Sprintf("There's no service called `%s`", a)}
				if strings.Join(fixed[1:], " ") != strings.Join(p.Args, " ") {
					e.Suggestion = strings.Join(fixed, " ")
				} else {
					e.Msg += " — services: " + strings.Join(ctx.Services, ", ")
				}
				return e
			}
		}
	case ArgMachine:
		if len(p.Args) != 1 {
			if len(ctx.Machines) == 0 {
				return &Error{Msg: "No fleet machines yet — `fleet add <name> <user@host>`"}
			}
			return &Error{Msg: "Which machine? " + strings.Join(ctx.Machines, ", ")}
		}
		if !containsStr(ctx.Machines, p.Args[0]) {
			e := &Error{Msg: fmt.Sprintf("There's no machine called `%s`", p.Args[0])}
			if s, ok := closest(p.Args[0], ctx.Machines); ok {
				e.Suggestion = p.Cmd.Name + " " + s
			}
			return e
		}
	case ArgTarget:
		if len(p.Args) > 1 {
			return &Error{Msg: fmt.Sprintf("`%s` takes one machine or group", p.Cmd.Name)}
		}
		if len(p.Args) == 1 && !containsStr(ctx.Machines, p.Args[0]) && !containsStr(ctx.Groups, p.Args[0]) {
			e := &Error{Msg: fmt.Sprintf("There's no machine or group called `%s`", p.Args[0])}
			if s, ok := closest(p.Args[0], append(append([]string{}, ctx.Machines...), ctx.Groups...)); ok {
				e.Suggestion = p.Cmd.Name + " " + s
			}
			return e
		}
	case ArgContainer:
		if len(p.Args) != 1 {
			return &Error{Msg: "Which container? Try `docker` to see them"}
		}
		if len(ctx.Containers) > 0 && !containsStr(ctx.Containers, p.Args[0]) {
			e := &Error{Msg: fmt.Sprintf("There's no container called `%s`", p.Args[0])}
			if s, ok := closest(p.Args[0], ctx.Containers); ok {
				e.Suggestion = p.Cmd.Name + " " + s
			}
			return e
		}
	}
	return nil
}

func allNames() []string {
	var out []string
	for _, c := range Commands {
		out = append(out, c.Name)
		out = append(out, c.Aliases...)
	}
	return out
}

func closest(word string, options []string) (string, bool) {
	best, bestD := "", 99
	for _, o := range options {
		d := distance(strings.ToLower(word), strings.ToLower(o))
		if strings.HasPrefix(strings.ToLower(o), strings.ToLower(word)) {
			d = 0
		}
		if d < bestD {
			best, bestD = o, d
		}
	}
	return best, best != "" && bestD <= maxTypos(word)
}

func maxTypos(word string) int {
	switch {
	case len(word) <= 3:
		return 1
	case len(word) <= 6:
		return 2
	}
	return 3
}

// distance is the optimal-string-alignment (Damerau–Levenshtein) distance.
func distance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	d := make([][]int, len(ra)+1)
	for i := range d {
		d[i] = make([]int, len(rb)+1)
		d[i][0] = i
	}
	for j := range d[0] {
		d[0][j] = j
	}
	for i := 1; i <= len(ra); i++ {
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)
			if i > 1 && j > 1 && ra[i-1] == rb[j-2] && ra[i-2] == rb[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}
	return d[len(ra)][len(rb)]
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
