// Package theme holds muster-tui's colours, styles and glyphs.
//
// Every colour is a lipgloss.AdaptiveColor so the UI reads well on light and
// dark terminals. Mustard (#E8B931) is the single accent; everything else is
// neutral. NO_COLOR is honoured by switching lipgloss to the ASCII profile.
package theme

import (
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// Static disables animation (spinners render a fixed frame). Tests set it so
// snapshots are deterministic.
var Static bool

var (
	Accent  = lipgloss.AdaptiveColor{Light: "#9A7400", Dark: "#E8B931"}
	Text    = lipgloss.AdaptiveColor{Light: "#1F1F1F", Dark: "#E6E6E6"}
	Muted   = lipgloss.AdaptiveColor{Light: "#6B6B6B", Dark: "#8A8A8A"}
	Faint   = lipgloss.AdaptiveColor{Light: "#A8A8A8", Dark: "#4A4A4A"}
	Good    = lipgloss.AdaptiveColor{Light: "#1E7D32", Dark: "#7BC67E"}
	Bad     = lipgloss.AdaptiveColor{Light: "#C62828", Dark: "#EF6B6B"}
	Warn    = lipgloss.AdaptiveColor{Light: "#A15C00", Dark: "#F0A84A"}
	SelBg   = lipgloss.AdaptiveColor{Light: "#F3E7C0", Dark: "#3A3222"}
	AddLine = lipgloss.AdaptiveColor{Light: "#1E7D32", Dark: "#7BC67E"}
	DelLine = lipgloss.AdaptiveColor{Light: "#C62828", Dark: "#EF6B6B"}
)

var (
	Base      = lipgloss.NewStyle().Foreground(Text)
	Dim       = lipgloss.NewStyle().Foreground(Muted)
	Rule      = lipgloss.NewStyle().Foreground(Faint)
	Bold      = lipgloss.NewStyle().Bold(true)
	Title     = lipgloss.NewStyle().Bold(true).Foreground(Text)
	Brand     = lipgloss.NewStyle().Bold(true).Foreground(Accent)
	AccentFg  = lipgloss.NewStyle().Foreground(Accent)
	OK        = lipgloss.NewStyle().Foreground(Good)
	Err       = lipgloss.NewStyle().Foreground(Bad)
	Warning   = lipgloss.NewStyle().Foreground(Warn)
	Selected  = lipgloss.NewStyle().Background(SelBg).Foreground(Text)
	Key       = lipgloss.NewStyle().Foreground(Accent)
	Code      = lipgloss.NewStyle().Foreground(Accent)
	Highlight = lipgloss.NewStyle().Reverse(true)
)

// Glyphs. Shapes differ per state so meaning never depends on colour alone.
const (
	GlyphHealthy   = "●"
	GlyphUnhealthy = "✕"
	GlyphUnknown   = "○"
	GlyphDisabled  = "○"
	GlyphDeploying = "↻"
	GlyphOK        = "✓"
	GlyphFail      = "✕"
	GlyphPending   = "·"
	GlyphSkip      = "–"
	GlyphWarn      = "!"
	GlyphPrompt    = "›"
	GlyphCursor    = "▸"
)

// SpinnerFrames is a braille dot spinner.
var SpinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Spinner returns the frame for tick n (frame 2 when Static).
func Spinner(n int) string {
	if Static {
		return SpinnerFrames[2]
	}
	if n < 0 {
		n = -n
	}
	return SpinnerFrames[n%len(SpinnerFrames)]
}

// Init configures the colour profile and background from the environment.
// MUSTER_THEME=light|dark or COLORFGBG skip the terminal background query
// (which can stall on terminals that never answer it).
func Init() {
	if NoColor() {
		lipgloss.SetColorProfile(termenv.Ascii)
	}
	if dark, ok := darkFromEnv(); ok {
		lipgloss.SetHasDarkBackground(dark)
	}
}

func darkFromEnv() (dark, ok bool) {
	switch strings.ToLower(os.Getenv("MUSTER_THEME")) {
	case "light":
		return false, true
	case "dark":
		return true, true
	}
	if v := os.Getenv("COLORFGBG"); v != "" {
		parts := strings.Split(v, ";")
		if n, err := strconv.Atoi(parts[len(parts)-1]); err == nil {
			return n < 7 || n == 8, true
		}
	}
	return false, false
}

// NoColor reports whether colour output is disabled via NO_COLOR or
// MUSTER_COLOR=never.
func NoColor() bool {
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return true
	}
	return os.Getenv("MUSTER_COLOR") == "never"
}

// Plain reports whether output has no colour or attributes (NO_COLOR or an
// ASCII terminal), so widgets must mark focus with characters instead.
func Plain() bool { return lipgloss.ColorProfile() == termenv.Ascii }
