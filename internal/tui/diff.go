package tui

import (
	"fmt"
	"strings"
)

// unifiedDiff returns unified-diff lines (without file headers) between a
// and b with ctx lines of context. Plain LCS: hook files are small.
func unifiedDiff(a, b []string, ctx int) []string {
	n, m := len(a), len(b)
	if n*m > 4_000_000 { // pathological sizes: show everything as replaced
		var out []string
		for _, l := range a {
			out = append(out, "-"+l)
		}
		for _, l := range b {
			out = append(out, "+"+l)
		}
		return out
	}
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	type op struct {
		kind byte // ' ', '-', '+'
		text string
		ai   int
		bi   int
	}
	var ops []op
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && a[i] == b[j]:
			ops = append(ops, op{' ', a[i], i, j})
			i++
			j++
		case i < n && (j == m || lcs[i+1][j] >= lcs[i][j+1]):
			ops = append(ops, op{'-', a[i], i, j})
			i++
		default:
			ops = append(ops, op{'+', b[j], i, j})
			j++
		}
	}
	// Group into hunks.
	var out []string
	for k := 0; k < len(ops); {
		if ops[k].kind == ' ' {
			k++
			continue
		}
		start := max(k-ctx, 0)
		end := k
		for end < len(ops) {
			if ops[end].kind != ' ' {
				end++
				continue
			}
			// look ahead: another change within 2*ctx keeps the hunk open
			next := end
			for next < len(ops) && ops[next].kind == ' ' && next-end < 2*ctx {
				next++
			}
			if next < len(ops) && ops[next].kind != ' ' {
				end = next
				continue
			}
			break
		}
		stop := min(end+ctx, len(ops))
		var aCount, bCount int
		for _, o := range ops[start:stop] {
			if o.kind != '+' {
				aCount++
			}
			if o.kind != '-' {
				bCount++
			}
		}
		out = append(out, fmt.Sprintf("@@ -%d,%d +%d,%d @@", ops[start].ai+1, aCount, ops[start].bi+1, bCount))
		for _, o := range ops[start:stop] {
			out = append(out, string(o.kind)+o.text)
		}
		k = stop
	}
	return out
}

func splitLines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
