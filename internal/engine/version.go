package engine

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Version is a parsed `muster --version`.
type Version struct {
	Major, Minor, Patch int
	Raw                 string
}

var versionRe = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

// ParseVersion reads "v0.7.0", "muster 0.5.60", "0.7.0-dev" and the like.
func ParseVersion(s string) (Version, bool) {
	m := versionRe.FindStringSubmatch(s)
	if m == nil {
		return Version{Raw: s}, false
	}
	a, _ := strconv.Atoi(m[1])
	b, _ := strconv.Atoi(m[2])
	c, _ := strconv.Atoi(m[3])
	return Version{Major: a, Minor: b, Patch: c, Raw: s}, true
}

// AtLeast reports v ≥ major.minor.patch.
func (v Version) AtLeast(major, minor, patch int) bool {
	if v.Major != major {
		return v.Major > major
	}
	if v.Minor != minor {
		return v.Minor > minor
	}
	return v.Patch >= patch
}

func (v Version) String() string { return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch) }

// EngineAPIVersion is the first muster with the engine API muster-tui 1.1
// builds on: hooks status/approve --json, explain, restart, logs --tail,
// rollback --json, deploy --stop-on-failure/--from and muster ask --json.
const EngineAPIVersion = "0.7.0"

// Caps says which CLI features are available.
type Caps struct {
	Known   bool    // the version was detected
	Version Version // valid when Known
	// EngineAPI: muster ≥ 0.7.0.
	EngineAPI bool
	// Fleet commands, detected from `muster fleet --help` (see FleetCapsFor):
	// FleetHelpKnown is set once the help text was read.
	FleetHelpKnown bool
	FleetTestJSON  bool // fleet test [target] --json
	FleetLogs      bool // fleet logs <server> <service> [--tail N] [--json]
	FleetRestart   bool // fleet restart <server> [service...] --yes [--json]
}

// WithFleetHelp records which fleet commands `muster fleet --help` lists.
// New fleet subcommands land between releases, so they're detected from
// the help text instead of the version number.
func (c Caps) WithFleetHelp(help string) Caps {
	c.FleetHelpKnown = true
	c.FleetTestJSON, c.FleetLogs, c.FleetRestart = false, false, false
	for _, l := range strings.Split(help, "\n") {
		f := strings.Fields(l)
		if len(f) == 0 {
			continue
		}
		switch f[0] {
		case "test":
			c.FleetTestJSON = strings.Contains(l, "--json")
		case "logs":
			c.FleetLogs = true
		case "restart":
			c.FleetRestart = true
		}
	}
	return c
}

// CapsFor derives capabilities from `muster --version` output.
func CapsFor(versionOutput string) Caps {
	v, ok := ParseVersion(versionOutput)
	if !ok {
		return Caps{}
	}
	return Caps{Known: true, Version: v, EngineAPI: v.AtLeast(0, 7, 0)}
}
