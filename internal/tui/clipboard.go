package tui

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// copyCmd puts text on the clipboard: pbcopy / wl-copy / xclip when present,
// otherwise an OSC 52 escape (works over SSH in most terminals).
func copyCmd(text, okMsg string) tea.Cmd {
	return func() tea.Msg {
		for _, c := range [][]string{{"pbcopy"}, {"wl-copy"}, {"xclip", "-selection", "clipboard"}, {"xsel", "--clipboard", "--input"}} {
			if _, err := exec.LookPath(c[0]); err != nil {
				continue
			}
			cmd := exec.Command(c[0], c[1:]...)
			cmd.Stdin = strings.NewReader(text)
			if cmd.Run() == nil {
				return toastMsg{level: lvlOK, text: okMsg}
			}
		}
		fmt.Fprintf(os.Stderr, "\x1b]52;c;%s\x07", base64.StdEncoding.EncodeToString([]byte(text)))
		return toastMsg{level: lvlOK, text: okMsg + " (via terminal)"}
	}
}
