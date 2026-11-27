package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/Muster-dev/muster-tui/internal/engine"
	"github.com/Muster-dev/muster-tui/internal/theme"
	tea "github.com/charmbracelet/bubbletea"
)

// streamLines adapts a deploy-style NDJSON stream (rollback, restart, logs
// --json) to the line stream the output screen shows.
func streamLines(s *engine.DeployStream, format func(engine.DeployEvent) []string) (<-chan string, func() error, error) {
	ch := make(chan string, 64)
	go func() {
		defer close(ch)
		for ev := range s.C {
			for _, l := range format(ev) {
				ch <- l
			}
		}
	}()
	return ch, s.Wait, nil
}

// logEventLines shows `muster logs --json` lines as-is.
func logEventLines(ev engine.DeployEvent) []string {
	switch ev.Event {
	case "log", "output", "stderr":
		return []string{ev.Line}
	case "error":
		return []string{theme.GlyphFail + " " + firstNonEmpty(ev.Message, ev.Reason, ev.Status)}
	}
	return nil
}

// actionEventLines renders rollback/restart events as readable output.
func actionEventLines(ev engine.DeployEvent) []string {
	switch ev.Event {
	case "start":
		switch ev.Action {
		case "rollback":
			return []string{"── Rolling back " + ev.Service + " ──"}
		case "restart":
			how := ""
			if ev.Method != "" {
				how = " (" + restartMethod(ev.Method) + ")"
			}
			return []string{"── Restarting " + ev.Service + how + " ──"}
		}
		return []string{"── Deploying " + ev.Service + " ──"}
	case "restart":
		return []string{theme.GlyphWarn + " " + firstNonEmpty(ev.Message, "No built-in restart — redeploying "+ev.Service)}
	case "log", "output", "stderr":
		return []string{ev.Line}
	case "done":
		switch ev.Status {
		case "success":
			return []string{theme.GlyphOK + " " + ev.Service + " done"}
		case "timeout":
			return []string{fmt.Sprintf("%s %s timed out after %ds", theme.GlyphFail, ev.Service, ev.Timeout)}
		case "blocked":
			return []string{theme.GlyphFail + " " + ev.Service + " was blocked by muster's hook security check"}
		}
		return []string{fmt.Sprintf("%s %s failed (exit %d)", theme.GlyphFail, ev.Service, ev.ExitCode)}
	case "health":
		switch ev.Status {
		case "checking":
			return []string{"  checking health…"}
		case "healthy":
			return []string{theme.GlyphOK + " healthy"}
		}
		return []string{theme.GlyphFail + " health check failed"}
	case "verify_warning":
		return []string{theme.GlyphWarn + " " + ev.Message}
	case "hint", "diagnosis":
		h := ev.Hint()
		if h == nil {
			return nil
		}
		var out []string
		if ev.Event == "diagnosis" && ev.PodStatus != "" {
			out = append(out, "Pod: "+ev.PodStatus)
		}
		if h.Title != "" {
			out = append(out, "Likely cause: "+h.Title)
		}
		if h.Try != "" {
			out = append(out, "Try: "+h.Try)
		}
		if h.Fix != nil {
			out = append(out, "Fix: "+h.Fix.Label+" — type `explain "+ev.Service+"`")
		}
		return out
	case "error":
		return []string{theme.GlyphFail + " " + errorEventText(ev)}
	case "skipped":
		return []string{theme.GlyphSkip + " " + ev.Service + " " + skipText(ev.Reason)}
	case "summary":
		if ev.Action == "rollback" && ev.RolledBack > 0 {
			return []string{theme.GlyphOK + " Rolled back " + plural(ev.RolledBack, "service")}
		}
		if ev.Action == "restart" && ev.Restarted > 0 {
			return []string{theme.GlyphOK + " Restarted " + plural(ev.Restarted, "service")}
		}
	}
	return nil
}

func restartMethod(m string) string {
	switch m {
	case "hook":
		return "restart hook"
	case "k8s":
		return "kubectl rollout restart"
	case "compose":
		return "docker compose restart"
	case "docker":
		return "docker restart"
	case "redeploy":
		return "redeploy"
	}
	return m
}

func errorEventText(ev engine.DeployEvent) string {
	switch ev.Reason {
	case "hooks_not_approved":
		return "Hooks changed since you approved them on this machine — type approve"
	case "no_hook":
		return ev.Service + " has no hook for this"
	case "locked":
		return firstNonEmpty(ev.Message, "Another deploy holds the lock")
	}
	return firstNonEmpty(ev.Message, ev.Reason, ev.Status, "failed")
}

// rollbackOutput opens an output screen for a rollback: JSON events on
// muster ≥ 0.7, raw output before.
func rollbackOutput(e *Env, svc string) Screen {
	eng := e.Eng()
	v07 := e.V07()
	return newOutput(e, "rollback:"+svc, "Rollback · "+svc, true, func(ctx context.Context) (<-chan string, func() error, error) {
		if v07 {
			s, err := eng.RollbackStream(ctx, svc)
			if err != nil {
				return nil, nil, err
			}
			return streamLines(s, actionEventLines)
		}
		return eng.Rollback(ctx, svc)
	})
}

// restartOutput runs `muster restart <svc> --json` (muster ≥ 0.7).
func restartOutput(e *Env, svc string) Screen {
	eng := e.Eng()
	s := newOutput(e, "restart:"+svc, "Restart · "+svc, true, func(ctx context.Context) (<-chan string, func() error, error) {
		st, err := eng.Restart(ctx, svc)
		if err != nil {
			return nil, nil, err
		}
		return streamLines(st, actionEventLines)
	})
	s.onEnd = func(error) tea.Cmd { return tea.Batch(refresh("status"), refresh("history")) }
	return s
}

// approvedFilesNote summarises pending hook files for a confirm body.
func approvedFilesNote(changes []string, max int) string {
	if len(changes) <= max {
		return strings.Join(changes, ", ")
	}
	return strings.Join(changes[:max], ", ") + fmt.Sprintf(" and %d more", len(changes)-max)
}
