package config

import (
	"os"
	"path/filepath"

	"github.com/Muster-dev/muster-tui/internal/jsonx"
)

// MusterHome returns ~/.muster.
func MusterHome() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".muster"
	}
	return filepath.Join(home, ".muster")
}

// GlobalPath is ~/.muster/settings.json.
func GlobalPath() string { return filepath.Join(MusterHome(), "settings.json") }

// GlobalField describes one editable global setting.
type GlobalField struct {
	Key     string
	Label   string
	Kind    string   // enum, int, bool
	Options []string // for enum
	Default any
	Help    string
}

// GlobalFields lists the global settings muster-tui can edit.
var GlobalFields = []GlobalField{
	{Key: "color_mode", Label: "Colour", Kind: "enum", Options: []string{"auto", "always", "never"}, Default: "auto", Help: "When the CLI uses colour"},
	{Key: "log_retention_days", Label: "Keep deploy logs (days)", Kind: "int", Default: 7, Help: "Older logs are removed by cleanup"},
	{Key: "default_stack", Label: "Default stack", Kind: "enum", Options: Stacks, Default: "bare", Help: "Used by setup when nothing is detected"},
	{Key: "default_health_timeout", Label: "Health timeout (s)", Kind: "int", Default: 10, Help: "Default health check timeout"},
	{Key: "update_check", Label: "Check for updates", Kind: "enum", Options: []string{"on", "off"}, Default: "on"},
	{Key: "tui_mode", Label: "Interactive UI", Kind: "enum", Options: []string{"go", "bash"}, Default: "go", Help: "go = muster-tui, bash = classic menus"},
	{Key: "minimal", Label: "Minimal mode", Kind: "bool", Default: false, Help: "Plain text output for the CLI"},
	{Key: "tui_mouse", Label: "Mouse in muster-tui", Kind: "enum", Options: []string{"on", "off"}, Default: "on", Help: "off = your terminal's own text selection (takes effect next start)"},
}

// LoadGlobal reads ~/.muster/settings.json (empty object when missing).
func LoadGlobal() (*jsonx.Object, error) {
	data, err := os.ReadFile(GlobalPath())
	if err != nil {
		if os.IsNotExist(err) {
			return jsonx.New(), nil
		}
		return nil, err
	}
	return jsonx.Parse(data)
}

// SaveGlobal writes settings atomically, keeping keys muster-tui doesn't know
// (cloud.*, scanner_exclude, …).
func SaveGlobal(o *jsonx.Object) error {
	if err := os.MkdirAll(MusterHome(), 0o700); err != nil {
		return err
	}
	data, err := o.Pretty()
	if err != nil {
		return err
	}
	return writeAtomic(GlobalPath(), data, 0o600)
}
