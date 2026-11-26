package docker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fake(t *testing.T, mode string) (*Client, string) {
	t.Helper()
	bin, _ := filepath.Abs("testdata/fake-docker")
	log := filepath.Join(t.TempDir(), "argv")
	return &Client{Bin: bin, Env: []string{"FAKE_DOCKER=" + mode, "FAKE_LOG=" + log}}, log
}

func TestContainersStatsImages(t *testing.T) {
	c, _ := fake(t, "")
	ctx := context.Background()
	cs, err := c.Containers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 3 || cs[0].Name() != "acme-api-1" || cs[2].Running() {
		t.Fatalf("%+v", cs)
	}
	if cs[1].ComposeService() != "redis" || cs[1].ShortPorts() != ":6379→6379" {
		t.Fatalf("labels/ports: %q %q", cs[1].ComposeService(), cs[1].ShortPorts())
	}
	st, err := c.Stats(ctx)
	if err != nil || st["acme-api-1"].CPUPerc != "3.10%" {
		t.Fatalf("%v %+v", err, st)
	}
	imgs, _ := c.Images(ctx)
	if len(imgs) != 3 || !imgs[2].Dangling() || imgs[0].Ref() != "acme/api:4f2a9c1" {
		t.Fatalf("%+v", imgs)
	}
	sum, err := c.Summary(ctx)
	if err != nil || sum.Running != 2 || sum.Total != 3 || sum.ImagesSize != "927MB" {
		t.Fatalf("%v %+v", err, sum)
	}
}

func TestDaemonDown(t *testing.T) {
	c, _ := fake(t, "down")
	_, err := c.Containers(context.Background())
	if !errors.Is(err, ErrDaemonDown) {
		t.Fatalf("want ErrDaemonDown, got %v", err)
	}
}

func TestPruneAndActions(t *testing.T) {
	c, log := fake(t, "")
	got, err := c.Prune(context.Background(), "images")
	if err != nil || got != "398MB" {
		t.Fatalf("%q %v", got, err)
	}
	if err := c.Do(context.Background(), "restart", "acme-api-1"); err != nil {
		t.Fatal(err)
	}
	argv, _ := os.ReadFile(log)
	if !strings.Contains(string(argv), "image prune -f\nrestart acme-api-1") {
		t.Fatalf("argv:\n%s", argv)
	}
}

func TestParseSize(t *testing.T) {
	for in, want := range map[string]float64{"1.2GB": 1.2e9, "398MB (42%)": 398e6, "12kB": 12e3, "0B": 0, "N/A": 0} {
		if got := ParseSize(in); got != want {
			t.Errorf("%s = %v want %v", in, got, want)
		}
	}
}
