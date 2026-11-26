package prompt

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

var ctx = Context{
	Services:   []string{"api", "web", "redis"},
	Machines:   []string{"prod-1", "edge-1"},
	Groups:     []string{"prod"},
	Containers: []string{"acme-api-1", "acme-redis-1"},
}

func TestParse(t *testing.T) {
	cases := []struct {
		in, cmd string
		args    []string
		flags   []string
	}{
		{"deploy", "deploy", nil, nil},
		{"deploy api web", "deploy", []string{"api", "web"}, nil},
		{"/deploy api", "deploy", []string{"api"}, nil},
		{"deploy --dry-run", "deploy", nil, []string{"--dry-run"}},
		{"deploy -n web", "deploy", []string{"web"}, []string{"--dry-run"}},
		{"plan web", "dry-run", []string{"web"}, []string{"--dry-run"}},
		{"logs for api", "logs", []string{"api"}, nil},
		{"Logs the web", "logs", []string{"web"}, nil},
		{"dep api", "deploy", []string{"api"}, nil},
		{"fleet deploy prod", "fleet deploy", []string{"prod"}, nil},
		{"fleet dep prod-1", "fleet deploy", []string{"prod-1"}, nil},
		{"cloud login", "cloud login", nil, nil},
		{"ssh prod-1", "ssh", []string{"prod-1"}, nil},
		{"exit", "quit", nil, nil},
		{"doctor --fix", "doctor", nil, []string{"--fix"}},
		{"deploy all", "deploy", []string{"all"}, nil},
		{"prune images", "prune", []string{"images"}, nil},
	}
	for _, c := range cases {
		p, err := Parse(c.in, ctx)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if p.Cmd.Name != c.cmd || strings.Join(p.Args, ",") != strings.Join(c.args, ",") {
			t.Errorf("%q: got %s %v", c.in, p.Cmd.Name, p.Args)
		}
		for _, f := range c.flags {
			if !p.Has(f) {
				t.Errorf("%q: missing flag %s", c.in, f)
			}
		}
	}
}

func TestDidYouMean(t *testing.T) {
	cases := map[string]string{
		"dpeloy api":       "deploy api",
		"lgos api":         "logs api",
		"logs apu":         "logs api",
		"deploy wbe":       "deploy web",
		"api logs":         "logs api",
		"rollbak redis":    "rollback redis",
		"ssh prod1":        "ssh prod-1",
		"fleet deploy prd": "fleet deploy prod",
		"histroy":          "history",
		"exec acme-api":    "exec acme-api-1",
		"stauts":           "status",
	}
	for in, want := range cases {
		_, err := Parse(in, ctx)
		var pe *Error
		if !errors.As(err, &pe) {
			t.Errorf("%q: expected error, got %v", in, err)
			continue
		}
		if pe.Suggestion != want {
			t.Errorf("%q: suggestion %q, want %q (%s)", in, pe.Suggestion, want, pe.Msg)
		}
	}
	_, err := Parse("xyzzy", ctx)
	var pe *Error
	if !errors.As(err, &pe) || pe.Suggestion != "" || !strings.Contains(pe.Msg, "xyzzy") {
		t.Fatalf("gibberish: %#v", err)
	}
	if _, err := Parse("status api", ctx); err == nil {
		t.Fatal("status takes no args")
	}
	if _, err := Parse("deploy --nope", ctx); err == nil {
		t.Fatal("unknown flag should fail")
	}
}

func TestComplete(t *testing.T) {
	c := Complete("dep", ctx)
	if len(c.Items) == 0 || c.Items[0].Label != "deploy" || c.Apply("dep", 0) != "deploy " {
		t.Fatalf("%+v", c.Items)
	}
	c = Complete("/lo", ctx)
	if c.Items[0].Label != "logs" || c.Apply("/lo", 0) != "/logs " {
		t.Fatalf("%+v", c)
	}
	c = Complete("logs r", ctx)
	if len(c.Items) != 1 || c.Apply("logs r", 0) != "logs redis " {
		t.Fatalf("%+v", c.Items)
	}
	c = Complete("deploy api ", ctx)
	for _, it := range c.Items {
		if it.Label == "api" {
			t.Fatal("already-used service offered again")
		}
	}
	c = Complete("fleet ", ctx)
	if len(c.Items) < 4 {
		t.Fatalf("fleet subcommands: %+v", c.Items)
	}
	c = Complete("fleet deploy ", ctx)
	if len(c.Items) != 3 {
		t.Fatalf("targets: %+v", c.Items)
	}
	c = Complete("deploy --d", ctx)
	if len(c.Items) != 1 || c.Items[0].Label != "--dry-run" {
		t.Fatalf("flags: %+v", c.Items)
	}
	c = Complete("exec acme-r", ctx)
	if len(c.Items) != 1 || c.Items[0].Label != "acme-redis-1" {
		t.Fatalf("containers: %+v", c.Items)
	}
	if len(Complete("", ctx).Items) == 0 {
		t.Fatal("empty input should list commands")
	}
	c = Complete("dk", ctx) // fuzzy: d…k → docker
	if len(c.Items) == 0 || c.Items[0].Label != "docker" {
		t.Fatalf("fuzzy: %+v", c.Items)
	}
}

func TestHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tui_history")
	h := LoadHistory(path)
	h.Add("deploy api")
	h.Add("logs api")
	h.Add("logs api")
	h.Add("status")
	h2 := LoadHistory(path)
	if got := h2.Items(); len(got) != 3 {
		t.Fatalf("persisted %v", got)
	}
	if s, _ := h2.Prev("draft"); s != "status" {
		t.Fatal(s)
	}
	if s, _ := h2.Prev(""); s != "logs api" {
		t.Fatal(s)
	}
	if s, _ := h2.Next(); s != "status" {
		t.Fatal(s)
	}
	if s, _ := h2.Next(); s != "draft" {
		t.Fatalf("draft restore: %q", s)
	}
	i, s := h2.Search("api", 3)
	if i != 1 || s != "logs api" {
		t.Fatal(i, s)
	}
	i, s = h2.Search("api", i)
	if i != 0 || s != "deploy api" {
		t.Fatal(i, s)
	}
	if i, _ = h2.Search("api", i); i != -1 {
		t.Fatal("expected no more matches")
	}
}
