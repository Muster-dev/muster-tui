package config

import (
	"fmt"
	"regexp"
	"strings"
)

// FieldError is a validation problem tied to a config path.
type FieldError struct {
	Path    string // e.g. "services.api.health.port"
	Message string
}

func (e FieldError) Error() string { return e.Path + ": " + e.Message }

// HealthTypes, CredModes and Stacks are the accepted enum values.
var (
	HealthTypes = []string{"http", "tcp", "command"}
	CredModes   = []string{"off", "save", "session", "always"}
	Stacks      = []string{"k8s", "compose", "docker", "bare", "dev"}
)

var (
	k8sName   = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	hostLike  = regexp.MustCompile(`^[A-Za-z0-9._:\-\[\]]+$`)
	userLike  = regexp.MustCompile(`^[A-Za-z0-9._\-]+$`)
	branchBad = regexp.MustCompile(`[\s~^:?*\[\\]`)
)

// Validate checks the document and returns every problem found.
func (p *Project) Validate() []FieldError {
	var errs []FieldError
	add := func(path, format string, a ...any) {
		errs = append(errs, FieldError{Path: path, Message: fmt.Sprintf(format, a...)})
	}
	svcs := p.Doc.Obj("services")
	if svcs == nil || svcs.Len() == 0 {
		add("services", "at least one service is required")
		return errs
	}
	for _, key := range svcs.Keys() {
		base := "services." + key
		if strings.TrimSpace(key) == "" {
			add(base, "service key cannot be empty")
		}
		if svcs.Obj(key) == nil {
			add(base, "must be an object")
			continue
		}
		s := p.Service(key)
		if s.Health.Enabled {
			switch s.Health.Type {
			case "http":
				if s.Health.Port < 1 || s.Health.Port > 65535 {
					add(base+".health.port", "port must be between 1 and 65535")
				}
				if s.Health.Endpoint != "" && !strings.HasPrefix(s.Health.Endpoint, "/") {
					add(base+".health.endpoint", "endpoint must start with /")
				}
			case "tcp":
				if s.Health.Port < 1 || s.Health.Port > 65535 {
					add(base+".health.port", "port must be between 1 and 65535")
				}
			case "command":
				if strings.TrimSpace(s.Health.Command) == "" {
					add(base+".health.command", "command is required for command health checks")
				}
			case "":
				// health hook decides; fine
			default:
				add(base+".health.type", "type must be one of %s", strings.Join(HealthTypes, ", "))
			}
			if s.Health.Timeout < 0 || s.Health.Timeout > 3600 {
				add(base+".health.timeout", "timeout must be between 0 and 3600 seconds")
			}
		}
		if !contains(CredModes, s.CredsMode) {
			add(base+".credentials.mode", "mode must be one of %s", strings.Join(CredModes, ", "))
		}
		if s.GitPull.Enabled && s.GitPull.Branch != "" && branchBad.MatchString(s.GitPull.Branch) {
			add(base+".git_pull.branch", "branch name contains invalid characters")
		}
		if s.Remote.Enabled {
			if s.Remote.Host == "" || !hostLike.MatchString(s.Remote.Host) {
				add(base+".remote.host", "a valid host is required when remote is enabled")
			}
			if s.Remote.User != "" && !userLike.MatchString(s.Remote.User) {
				add(base+".remote.user", "user contains invalid characters")
			}
			if s.Remote.Port != 0 && (s.Remote.Port < 1 || s.Remote.Port > 65535) {
				add(base+".remote.port", "port must be between 1 and 65535")
			}
		}
		if s.K8s.Deployment != "" && !k8sName.MatchString(s.K8s.Deployment) {
			add(base+".k8s.deployment", "must be lowercase letters, digits and hyphens")
		}
		if s.K8s.Namespace != "" && !k8sName.MatchString(s.K8s.Namespace) {
			add(base+".k8s.namespace", "must be lowercase letters, digits and hyphens")
		}
		if s.DeployTimeout < 0 {
			add(base+".deploy_timeout", "timeout cannot be negative")
		}
	}
	seen := map[string]bool{}
	for _, k := range p.DeployOrder() {
		if !p.HasService(k) {
			add("deploy_order", "%q is not a configured service", k)
		}
		if seen[k] {
			add("deploy_order", "%q appears twice", k)
		}
		seen[k] = true
	}
	return errs
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
