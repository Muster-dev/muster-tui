package tui

import (
	"fmt"
	"os"
	"strings"

	"github.com/Muster-dev/muster-tui/internal/theme"
	tea "github.com/charmbracelet/bubbletea"
)

// serviceScreen: config summary, live health, recent deploys and actions.
type serviceScreen struct {
	env *Env
	svc string
}

func newServiceScreen(env *Env, svc string) *serviceScreen { return &serviceScreen{env: env, svc: svc} }

func (s *serviceScreen) Key() string                      { return "service:" + s.svc }
func (s *serviceScreen) Title() string                    { return s.svc }
func (s *serviceScreen) Init() tea.Cmd                    { return nil }
func (s *serviceScreen) Update(tea.Msg) (Screen, tea.Cmd) { return s, nil }

func (s *serviceScreen) HandleKey(k tea.KeyMsg) (Screen, tea.Cmd, bool) {
	switch k.String() {
	case "d":
		return s, runLine("deploy " + s.svc), true
	case "p":
		return s, runLine("dry-run " + s.svc), true
	case "l":
		return s, runLine("logs " + s.svc), true
	case "b":
		return s, runLine("rollback " + s.svc), true
	case "r":
		return s, runLine("restart " + s.svc), true
	case "e":
		return s, runLine("edit " + s.svc), true
	case "h":
		return s, runLine("edit " + s.svc + " health"), true
	case "s", ",":
		return s, runLine("settings " + s.svc), true
	case "H":
		return s, runLine("history " + s.svc), true
	}
	return s, nil, false
}

func (s *serviceScreen) View(w, h int) string {
	st := s.env.State
	p := st.Project
	if p == nil || !p.HasService(s.svc) {
		return block([]string{section(s.svc), "", theme.Dim.Render("This service is no longer in muster.json.")}, w, h)
	}
	cfg := p.Service(s.svc)
	ss := st.Svc(s.svc)
	now := s.env.Now()
	var lines []string
	add := func(x ...string) { lines = append(lines, x...) }

	health := healthGlyph(ss.Shown()) + " " + healthWord(ss.Shown())
	if ss.Deploying {
		health = theme.AccentFg.Render(s.env.Spin() + " deploying")
	} else if ss.LockedBy != nil {
		health = theme.Warning.Render(theme.GlyphWarn + " being deployed by " + ss.LockedBy.Who())
	} else if !ss.Since.IsZero() && (ss.Shown() == "healthy" || ss.Shown() == "unhealthy") {
		health += theme.Dim.Render(" since " + ago(ss.Since, now))
	}
	title := section(s.svc)
	if cfg.Name != s.svc {
		title += theme.Dim.Render("  " + cfg.Name)
	}
	add(spread(title, health, w), "")

	kv := func(k, v string) { add(theme.Dim.Render(fit(k, 14)) + v) }
	kv("Stack", firstNonEmpty(ss.Stack, p.Stack(s.svc), "unknown"))
	if ss.LockedBy != nil {
		l := ss.LockedBy
		v := "now, by " + l.Who()
		if l.Source != "" && l.Source != "local" {
			v += " (" + l.Source + ")"
		}
		if l.Started != "" {
			v += theme.Dim.Render(" since " + l.Started)
		}
		if l.PID > 0 {
			v += theme.Dim.Render(fmt.Sprintf(" · pid %d", l.PID))
		}
		kv("Deploying", theme.Warning.Render(v))
	}
	if ld := ss.CLILast; ld != nil {
		v := theme.OK.Render(firstNonEmpty(ld.Status, "ok"))
		if ld.Status != "" && ld.Status != "ok" {
			v = theme.Err.Render(ld.Status)
		}
		v += theme.Dim.Render(" " + ld.TS)
		if c := shortSHA(ld.Commit); c != "" {
			v += theme.Dim.Render(" · ") + c
		}
		if ld.Source != "" && ld.Source != "local" {
			v += theme.Dim.Render(" · from " + ld.Source)
		}
		kv("Last deploy", v)
	}
	if r := ss.Replicas; r != nil {
		v := fmt.Sprintf("%d/%d ready", r.Ready, r.Desired)
		if r.Ready < r.Desired {
			v = theme.Warning.Render(v)
		}
		kv("Replicas", v)
	}
	hc := cfg.HealthLabel()
	if cfg.Health.Enabled && cfg.Health.Type == "http" && cfg.Health.Endpoint != "" {
		hc += " " + cfg.Health.Endpoint
	}
	if cfg.Health.Enabled && cfg.Health.Timeout > 0 {
		hc += theme.Dim.Render(fmt.Sprintf("  timeout %ds", cfg.Health.Timeout))
	}
	if ss.Target != "" && cfg.Health.Enabled {
		hc += theme.Dim.Render("  → " + ss.Target)
	}
	kv("Health check", hc)
	kv("Credentials", cfg.CredsMode)
	if cfg.GitPull.Enabled {
		kv("Git pull", firstNonEmpty(cfg.GitPull.Remote, "origin")+"/"+firstNonEmpty(cfg.GitPull.Branch, "main"))
	} else {
		kv("Git pull", theme.Dim.Render("off"))
	}
	if cfg.Remote.Enabled {
		r := cfg.Remote.Host
		if cfg.Remote.User != "" {
			r = cfg.Remote.User + "@" + r
		}
		if cfg.Remote.ProjectDir != "" {
			r += ":" + cfg.Remote.ProjectDir
		}
		kv("Remote", r)
	} else {
		kv("Remote", theme.Dim.Render("local"))
	}
	if cfg.K8s.Deployment != "" || cfg.K8s.Namespace != "" {
		kv("Kubernetes", firstNonEmpty(cfg.K8s.Deployment, s.svc)+theme.Dim.Render(" in ")+firstNonEmpty(cfg.K8s.Namespace, "default"))
	}
	if cfg.DeployTimeout > 0 {
		kv("Deploy timeout", fmt.Sprintf("%ds", cfg.DeployTimeout))
	}
	if cfg.SkipDeploy {
		kv("Deploy", theme.Warning.Render("skipped (skip_deploy)"))
	}

	// Hooks
	var hooks []string
	changed := map[string]bool{}
	for _, c := range st.HookChanges {
		changed[c.Key] = true
	}
	for _, hk := range []string{"deploy", "health", "rollback", "logs", "cleanup"} {
		if _, err := os.Stat(p.HookPath(s.svc, hk)); err == nil {
			name := hk
			if changed[s.svc+"/"+hk+".sh"] {
				name = theme.Warning.Render(hk + "*")
			}
			hooks = append(hooks, name)
		}
	}
	kv("Hooks", strings.Join(hooks, theme.Dim.Render(" · ")))

	// Recent deploys
	var recent []string
	for i := len(st.History) - 1; i >= 0 && len(recent) < 5; i-- {
		if ev := st.History[i]; ev.Service == s.svc {
			recent = append(recent, "  "+activityLine(ev, now, w-2))
		}
	}
	add("", section("Recent deploys"))
	if len(recent) == 0 {
		add(theme.Dim.Render("  none yet"))
	}
	add(recent...)
	for len(lines) < h-2 {
		add("")
	}
	add(rule(w), keyHint("d", "deploy", "p", "dry run", "l", "logs", "b", "rollback", "r", "restart", "e", "edit hook", "s", "settings"))
	return block(lines, w, h)
}

func (s *serviceScreen) Hint() string { return "" }

func (s *serviceScreen) Keys() []KeyHelp {
	return []KeyHelp{{"d", "deploy"}, {"p", "dry run"}, {"l", "logs"}, {"b", "rollback"}, {"r", "restart"}, {"e", "edit deploy hook"}, {"h", "edit health hook"}, {"s", "settings"}, {"H", "history"}}
}
