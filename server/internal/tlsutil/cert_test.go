package tlsutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestEnsureCertTightensExistingKeyPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not reliable on windows")
	}
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)

	mindfsDir := filepath.Join(configDir, "mindfs")
	if err := os.MkdirAll(mindfsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(mindfsDir, "cert.pem")
	keyPath := filepath.Join(mindfsDir, "key.pem")
	if err := os.WriteFile(certPath, []byte("cert"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, []byte("key"), 0o644); err != nil {
		t.Fatal(err)
	}

	gotCert, gotKey, err := EnsureCert("", "")
	if err != nil {
		t.Fatalf("EnsureCert returned error: %v", err)
	}
	if gotCert != certPath || gotKey != keyPath {
		t.Fatalf("EnsureCert paths = %q, %q; want %q, %q", gotCert, gotKey, certPath, keyPath)
	}
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("key mode = %o, want 0600", got)
	}
}
