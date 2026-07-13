package e2ee

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureConfigTightensExistingFilePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "e2ee.json")
	if err := os.WriteFile(path, []byte(`{"enabled":false,"node_id":"node"}`), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("Chmod setup returned error: %v", err)
	}

	if _, err := EnsureConfigAtPath(path, true); err != nil {
		t.Fatalf("EnsureConfigAtPath returned error: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat returned error: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("config mode = %o, want 600", got)
	}
	temps, err := filepath.Glob(filepath.Join(filepath.Dir(path), "e2ee.json.tmp-*"))
	if err != nil {
		t.Fatalf("Glob temp files returned error: %v", err)
	}
	if len(temps) != 0 {
		t.Fatalf("config temp files left behind: %#v", temps)
	}
}

func TestEnsureConfigTightensCompleteExistingFilePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "e2ee.json")
	if err := os.WriteFile(path, []byte(`{"enabled":true,"node_id":"node","pairing_secret":"secret"}`), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("Chmod setup returned error: %v", err)
	}

	result, err := EnsureConfigAtPath(path, true)
	if err != nil {
		t.Fatalf("EnsureConfigAtPath returned error: %v", err)
	}
	if result.Generated {
		t.Fatal("complete config was unexpectedly regenerated")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat returned error: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("config mode = %o, want 600", got)
	}
}
