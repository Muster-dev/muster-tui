// Package prompt implements the command line at the bottom of muster-tui:
// the command registry, a forgiving parser, fuzzy completion and history.
package prompt

// ArgKind says what a command's arguments are, for completion and checks.
type ArgKind int

const (
	ArgNone      ArgKind = iota
	ArgService           // exactly one service (optional when there's only one)
	ArgServices          // zero or more services
	ArgMachine           // one fleet machine
	ArgTarget            // a machine or group (optional)
	ArgContainer         // a docker container
	ArgText              // free text
	ArgSub               // subcommands only (e.g. "fleet")
)

// Command is one thing you can type.
type Command struct {
	Name    string
	Aliases []string
	Args    ArgKind
	Usage   string
	Summary string
	Keys    string   // keybinding shown in the palette, "" if none
	Flags   []string // accepted flags
	Group   string   // palette section
	Hidden  bool     // contextual commands (retry, skip…) not listed in help
}

// Commands is the registry, in palette order.
var Commands = []Command{
	{Name: "deploy", Args: ArgServices, Usage: "deploy [service…] [--dry-run] [--force]", Summary: "Deploy all services, or the ones you name", Flags: []string{"--dry-run", "--force"}, Group: "Deploy", Keys: "d"},
	{Name: "dry-run", Aliases: []string{"plan", "preview"}, Args: ArgServices, Usage: "dry-run [service…]", Summary: "Show the deploy plan without changing anything", Group: "Deploy"},
	{Name: "rollback", Args: ArgService, Usage: "rollback <service>", Summary: "Roll a service back to its previous version", Group: "Deploy", Keys: "b"},
	{Name: "restart", Args: ArgService, Usage: "restart <service>", Summary: "Restart a service's container (or redeploy it)", Group: "Deploy", Keys: "r"},
	{Name: "logs", Aliases: []string{"log", "tail"}, Args: ArgService, Usage: "logs <service>", Summary: "Follow a service's logs", Group: "Observe", Keys: "l"},
	{Name: "status", Aliases: []string{"health", "refresh"}, Usage: "status", Summary: "Check health of every service now", Group: "Observe", Keys: "ctrl+r"},
	{Name: "open", Aliases: []string{"service", "show", "info"}, Args: ArgService, Usage: "open <service>", Summary: "Service detail: config, health, recent deploys", Group: "Observe"},
	{Name: "history", Args: ArgService, Usage: "history [service]", Summary: "Timeline of deploys and rollbacks", Group: "Observe"},
	{Name: "doctor", Usage: "doctor [--fix]", Summary: "Diagnose the project and fix what can be fixed", Flags: []string{"--fix"}, Group: "Observe"},
	{Name: "dashboard", Aliases: []string{"home", "overview"}, Usage: "dashboard", Summary: "Back to the overview", Group: "Observe", Keys: "g"},
	{Name: "docker", Aliases: []string{"containers", "ps"}, Usage: "docker", Summary: "Containers, images, volumes and disk usage", Group: "Docker"},
	{Name: "images", Usage: "images", Summary: "Docker images (dangling ones marked)", Group: "Docker"},
	{Name: "volumes", Usage: "volumes", Summary: "Docker volumes", Group: "Docker"},
	{Name: "prune", Usage: "prune [images|containers]", Summary: "Free disk space (asks first, shows how much)", Args: ArgText, Group: "Docker"},
	{Name: "exec", Aliases: []string{"shell", "sh"}, Args: ArgContainer, Usage: "exec <container>", Summary: "Open a shell inside a container", Group: "Docker"},
	{Name: "servers", Aliases: []string{"fleet", "machines"}, Usage: "servers", Summary: "Fleet machines, groups and cloud agents", Group: "Servers"},
	{Name: "ssh", Args: ArgMachine, Usage: "ssh <machine>", Summary: "Open an SSH session (muster steps aside)", Group: "Servers"},
	{Name: "fleet deploy", Args: ArgTarget, Usage: "fleet deploy [machine|group]", Summary: "Deploy to fleet machines", Flags: []string{"--dry-run", "--parallel"}, Group: "Servers"},
	{Name: "fleet status", Args: ArgTarget, Usage: "fleet status [machine|group]", Summary: "Health across the fleet", Group: "Servers"},
	{Name: "fleet test", Args: ArgTarget, Usage: "fleet test [machine|group]", Summary: "Test SSH connectivity", Group: "Servers"},
	{Name: "fleet rollback", Args: ArgTarget, Usage: "fleet rollback [machine|group]", Summary: "Roll back across the fleet", Group: "Servers"},
	{Name: "fleet add", Args: ArgText, Usage: "fleet add <name> <user@host>", Summary: "Add a machine to the fleet", Group: "Servers"},
	{Name: "cloud login", Usage: "cloud login", Summary: "Sign in to Fleet Cloud (opens your browser)", Group: "Servers"},
	{Name: "cloud logout", Usage: "cloud logout", Summary: "Sign out of Fleet Cloud", Group: "Servers"},
	{Name: "cloud status", Usage: "cloud status", Summary: "Fleet Cloud account and relay", Group: "Servers"},
	{Name: "setup", Aliases: []string{"init", "scan"}, Usage: "setup", Summary: "Scan this folder and create muster.json", Group: "Project"},
	{Name: "settings", Aliases: []string{"config", "prefs"}, Args: ArgService, Usage: "settings [service]", Summary: "Edit project and global settings", Group: "Project", Keys: ","},
	{Name: "approve", Usage: "approve", Summary: "Review and approve changed hooks", Group: "Project"},
	{Name: "edit", Args: ArgService, Usage: "edit <service> [hook]", Summary: "Open a service's hook in $EDITOR", Group: "Project"},
	{Name: "cleanup", Usage: "cleanup", Summary: "Clean stuck processes and old logs", Group: "Project"},
	{Name: "help", Aliases: []string{"?", "keys"}, Usage: "help", Summary: "Keys and commands", Group: "General", Keys: "?"},
	{Name: "palette", Aliases: []string{"commands"}, Usage: "palette", Summary: "Every action, searchable", Group: "General", Keys: "ctrl+k"},
	{Name: "quit", Aliases: []string{"exit", "q"}, Usage: "quit", Summary: "Leave muster", Group: "General", Keys: "ctrl+c ×2"},
	{Name: "retry", Usage: "retry", Summary: "Retry the failed service", Hidden: true, Group: "Deploy"},
	{Name: "skip", Usage: "skip", Summary: "Skip the failed service and continue", Hidden: true, Group: "Deploy"},
	{Name: "abort", Aliases: []string{"cancel", "stop"}, Usage: "abort", Summary: "Stop the running task", Hidden: true, Group: "Deploy"},
	{Name: "back", Usage: "back", Summary: "Previous screen", Hidden: true, Group: "General"},
}

// Lookup finds a command by name or alias.
func Lookup(name string) (Command, bool) {
	for _, c := range Commands {
		if c.Name == name {
			return c, true
		}
		for _, a := range c.Aliases {
			if a == name {
				return c, true
			}
		}
	}
	return Command{}, false
}
