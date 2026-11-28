package fleet

import (
	"os"
	"strings"
	"testing"
)

func TestParseTestJSON(t *testing.T) {
	b, err := os.ReadFile("testdata/fleet-test.json")
	if err != nil {
		t.Fatal(err)
	}
	r, err := ParseTest(b)
	if err != nil {
		t.Fatal(err)
	}
	if r.Passed != 1 || r.Failed != 1 || len(r.Servers) != 2 || r.FromText {
		t.Fatalf("%+v", r)
	}
	if s := r.Servers[0]; s.Name != "byricky" || !s.OK || s.LatencyMS != 120 || s.Transport != "cloud" || s.Muster != "ok" {
		t.Fatalf("%+v", s)
	}
	if s := r.Servers[1]; s.OK || s.Connect != "fail" || !strings.Contains(s.Error, "timed out") {
		t.Fatalf("%+v", s)
	}
	if _, err := ParseTest([]byte(`{"ok":true}`)); err == nil {
		t.Fatal("want an error without servers")
	}
}

func TestParseTestTextRealCloudOutput(t *testing.T) {
	// Captured from muster 0.7.0 under a pty: spinner frames, colours, box.
	b, err := os.ReadFile("testdata/fleet-test-cloud.txt")
	if err != nil {
		t.Fatal(err)
	}
	r := ParseTestText(string(b), []Machine{{Name: "byricky", Transport: "cloud", Mode: "muster"}})
	if !r.FromText || r.Passed != 1 || r.Failed != 0 || len(r.Servers) != 1 {
		t.Fatalf("%+v", r)
	}
	s := r.Servers[0]
	if s.Name != "byricky" || !s.OK || s.Transport != "cloud" || s.Connect != "ok" || s.Muster != "ok" || s.Error != "" {
		t.Fatalf("%+v", s)
	}
}

func TestParseTestTextMixed(t *testing.T) {
	b, err := os.ReadFile("testdata/fleet-test-mixed.txt")
	if err != nil {
		t.Fatal(err)
	}
	ms := []Machine{
		{Name: "10-0-2-100", Transport: "ssh", Mode: "muster"},
		{Name: "byricky", Transport: "cloud", Mode: "muster"},
		{Name: "a-very-long-server-name-indeed", Transport: "ssh", Mode: "push"},
	}
	r := ParseTestText(string(b), ms)
	if r.Passed != 1 || r.Failed != 2 || len(r.Servers) != 4 {
		t.Fatalf("%+v", r)
	}
	want := []struct {
		name    string
		ok      bool
		pairing bool
		connect string
	}{
		{"10-0-2-100", false, true, "ok"},
		{"byricky", true, false, "ok"},
		{"a-very-long-server-name-indeed", false, false, "fail"},
		{"edge", false, false, "ok"},
	}
	for i, w := range want {
		s := r.Servers[i]
		if s.Name != w.name || s.OK != w.ok || s.Pairing != w.pairing || s.Connect != w.connect {
			t.Errorf("%d: %+v", i, s)
		}
		if !s.OK && s.Error == "" {
			t.Errorf("%d: failed without a reason", i)
		}
	}
	if r.Servers[3].Transport != "ssh" || r.Servers[3].Muster != "fail" {
		t.Errorf("%+v", r.Servers[3])
	}
}

func TestCleanCLITextAndServerWords(t *testing.T) {
	got := CleanCLIText("\x1b[33m  │  ● web   ok│\x1b[0m")
	if strings.ContainsAny(got, "│\x1b") || !strings.Contains(got, "● web   ok") {
		t.Fatalf("%q", got)
	}
	for in, want := range map[string]string{
		"All 1 machine(s) passed":     "All 1 server passed",
		"All 3 machine(s) passed":     "All 3 servers passed",
		"Deploying to 2 machines":     "Deploying to 2 servers",
		"Machine byricky is unpaired": "Server byricky is unpaired",
		"no machine called x":         "no server called x",
		"machinery stays":             "machinery stays",
	} {
		if g := ServerWords(in); g != want {
			t.Errorf("%q → %q, want %q", in, g, want)
		}
	}
}
