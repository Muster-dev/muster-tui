package auth

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTokenLookupOrder(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("MUSTER_TOKEN", "")
	if _, _, err := LoadToken(); err == nil {
		t.Fatal("expected no token")
	}
	_ = os.MkdirAll(filepath.Join(h, ".muster-tui"), 0o700)
	_ = os.WriteFile(LegacyTokenConfigPath(), []byte(`{"token":"legacy"}`), 0o600)
	if tok, src, _ := LoadToken(); tok != "legacy" || src != LegacyTokenConfigPath() {
		t.Fatalf("got %q from %q", tok, src)
	}
	if err := SaveToken("fresh"); err != nil {
		t.Fatal(err)
	}
	if tok, src, _ := LoadToken(); tok != "fresh" || src != TokenConfigPath() {
		t.Fatalf("got %q from %q", tok, src)
	}
	if data, _ := os.ReadFile(LegacyTokenConfigPath()); string(data) != `{"token":"fresh"}` {
		t.Fatalf("legacy not synced: %s", data)
	}
	if st, _ := os.Stat(TokenConfigPath()); st.Mode().Perm() != 0o600 {
		t.Fatalf("perm %v", st.Mode())
	}
	t.Setenv("MUSTER_TOKEN", "env")
	if tok, src, _ := LoadToken(); tok != "env" || src != "env" {
		t.Fatal("env should win")
	}
}

func TestMachineHasTokens(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	if MachineHasTokens() {
		t.Fatal("no tokens yet")
	}
	_ = os.MkdirAll(filepath.Join(h, ".muster", "tokens"), 0o700)
	_ = os.WriteFile(filepath.Join(h, ".muster", "tokens", "auth.json"), []byte(`{"tokens":[{"name":"x"}]}`), 0o600)
	if !MachineHasTokens() {
		t.Fatal("expected tokens")
	}
}

func TestRepairEmptyStore(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	if ok, _ := RepairEmptyStore(); ok {
		t.Fatal("missing file must not be created")
	}
	_ = os.MkdirAll(filepath.Join(h, ".muster", "tokens"), 0o700)
	_ = os.WriteFile(TokenStorePath(), []byte(`{"tokens":[{"name":"keep"}]}`), 0o600)
	if ok, _ := RepairEmptyStore(); ok {
		t.Fatal("non-empty store must be left alone")
	}
	_ = os.WriteFile(TokenStorePath(), nil, 0o600)
	if ok, err := RepairEmptyStore(); !ok || err != nil {
		t.Fatalf("empty store should be repaired: %v", err)
	}
	if data, _ := os.ReadFile(TokenStorePath()); string(data) != "{\"tokens\":[]}\n" {
		t.Fatalf("got %q", data)
	}
}
