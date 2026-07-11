package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalCLITokenStoreKeepsTokensByAddress(t *testing.T) {
	setTestConfigHome(t, t.TempDir())

	first, err := EnsureLocalCLIToken("127.0.0.1:7331")
	if err != nil {
		t.Fatalf("EnsureLocalCLIToken first: %v", err)
	}
	second, err := EnsureLocalCLIToken("127.0.0.1:9000")
	if err != nil {
		t.Fatalf("EnsureLocalCLIToken second: %v", err)
	}
	if first == second {
		t.Fatal("expected different tokens for different addresses")
	}

	gotFirst, err := ReadLocalCLIToken(":7331")
	if err != nil {
		t.Fatalf("ReadLocalCLIToken first: %v", err)
	}
	if gotFirst != first {
		t.Fatalf("first token = %q, want %q", gotFirst, first)
	}
	gotSecond, err := ReadLocalCLIToken("127.0.0.1:9000")
	if err != nil {
		t.Fatalf("ReadLocalCLIToken second: %v", err)
	}
	if gotSecond != second {
		t.Fatalf("second token = %q, want %q", gotSecond, second)
	}
}

func TestLocalCLITokenStoreWritesSinglePrivateFile(t *testing.T) {
	configRoot := t.TempDir()
	setTestConfigHome(t, configRoot)

	if _, err := EnsureLocalCLIToken(":7331"); err != nil {
		t.Fatalf("EnsureLocalCLIToken: %v", err)
	}
	path, err := localCLITokenStorePath()
	if err != nil {
		t.Fatalf("localCLITokenStorePath: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat token store: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("token store mode = %o, want 0600", info.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read token store: %v", err)
	}
	var store localCLITokenStore
	if err := json.Unmarshal(raw, &store); err != nil {
		t.Fatalf("unmarshal token store: %v", err)
	}
	if len(store.Tokens) != 1 {
		t.Fatalf("token count = %d, want 1", len(store.Tokens))
	}
}

func TestLocalCLITokenStoreTightensExistingFilePermissions(t *testing.T) {
	configRoot := t.TempDir()
	setTestConfigHome(t, configRoot)
	path, err := localCLITokenStorePath()
	if err != nil {
		t.Fatalf("localCLITokenStorePath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir token dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"tokens":{"127.0.0.1:7331":"old"}}`), 0o644); err != nil {
		t.Fatalf("seed token store: %v", err)
	}

	if _, err := EnsureLocalCLIToken("127.0.0.1:9000"); err != nil {
		t.Fatalf("EnsureLocalCLIToken: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat token store: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("token store mode = %o, want 0600", info.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read token store: %v", err)
	}
	var store localCLITokenStore
	if err := json.Unmarshal(raw, &store); err != nil {
		t.Fatalf("unmarshal token store: %v", err)
	}
	if store.Tokens["127.0.0.1:7331"] != "old" {
		t.Fatalf("existing token was not preserved: %#v", store.Tokens)
	}
	if store.Tokens["127.0.0.1:9000"] == "" {
		t.Fatalf("new token missing: %#v", store.Tokens)
	}
}

func setTestConfigHome(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	t.Setenv("AppData", dir)
}
