// Package auth stores the MUSTER_TOKEN muster-tui passes to the CLI.
//
// The CLI gates every --json command behind a token (`_json_auth_gate`).
// When `muster` launches muster-tui it creates an admin token named
// "muster-tui" and saves it with `muster-tui --set-token`, so the normal path
// needs no setup. Lookup order: $MUSTER_TOKEN, ~/.muster/tui/config.json
// (current CLI location), ~/.muster-tui/config.json (legacy).
package auth

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// TUIConfig is the token file format.
type TUIConfig struct {
	Token string `json:"token"`
}

func home() string {
	h, _ := os.UserHomeDir()
	return h
}

// TokenConfigPath is where tokens are saved (~/.muster/tui/config.json).
func TokenConfigPath() string { return filepath.Join(home(), ".muster", "tui", "config.json") }

// LegacyTokenConfigPath is the pre-0.5 location (~/.muster-tui/config.json).
func LegacyTokenConfigPath() string { return filepath.Join(home(), ".muster-tui", "config.json") }

// LoadToken returns the token and where it came from ("env", or a path).
func LoadToken() (token, source string, err error) {
	if t := os.Getenv("MUSTER_TOKEN"); t != "" {
		return t, "env", nil
	}
	for _, p := range []string{TokenConfigPath(), LegacyTokenConfigPath()} {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var cfg TUIConfig
		if json.Unmarshal(data, &cfg) == nil && cfg.Token != "" {
			return cfg.Token, p, nil
		}
	}
	return "", "", errors.New("no token configured")
}

// SaveToken writes the token (0600) to the current location, and to the
// legacy location too if that file already exists so older CLIs stay in sync.
func SaveToken(token string) error {
	data, err := json.Marshal(TUIConfig{Token: token})
	if err != nil {
		return err
	}
	paths := []string{TokenConfigPath()}
	if _, err := os.Stat(LegacyTokenConfigPath()); err == nil {
		paths = append(paths, LegacyTokenConfigPath())
	}
	for _, p := range paths {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(p, data, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// MachineHasTokens reports whether the CLI already has any API tokens. When
// it has none, `muster auth create` works without auth (bootstrap).
func MachineHasTokens() bool {
	for _, p := range []string{
		filepath.Join(home(), ".muster", "tokens", "auth.json"),
		filepath.Join(home(), ".muster", "tokens.json"),
	} {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var f struct {
			Tokens []json.RawMessage `json:"tokens"`
		}
		if json.Unmarshal(data, &f) == nil && len(f.Tokens) > 0 {
			return true
		}
	}
	return false
}

// TokenStorePath is the CLI's API token store.
func TokenStorePath() string { return filepath.Join(home(), ".muster", "tokens", "auth.json") }

// RepairEmptyStore restores a token store that exists but is empty (0 bytes
// or whitespace) to `{"tokens":[]}`. An empty file holds no tokens, but the
// CLI can't bootstrap from it (it only initialises a missing file); concurrent
// --json calls in muster ≤ 0.5.61 can leave it empty. Anything else is left
// alone. Reports whether it repaired the file.
func RepairEmptyStore() (bool, error) {
	p := TokenStorePath()
	data, err := os.ReadFile(p)
	if err != nil || len(strings.TrimSpace(string(data))) > 0 {
		return false, nil
	}
	return true, os.WriteFile(p, []byte("{\"tokens\":[]}\n"), 0o600)
}
