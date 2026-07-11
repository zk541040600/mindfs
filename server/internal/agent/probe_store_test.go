package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestProbeSessionStoreSaveLockedRemovesTempFileOnRenameFailure(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "probe-sessions.json")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatalf("Mkdir target returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(target, "keep"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile nested returned error: %v", err)
	}

	store := &probeSessionStore{
		path: target,
		data: map[string]ProbeSessionBinding{
			"pi": {AgentSessionID: "session", UpdatedAt: time.Now().UTC()},
		},
	}
	if err := store.saveLocked(); err == nil {
		t.Fatal("saveLocked() error = nil, want rename failure")
	}
	if matches, err := filepath.Glob(filepath.Join(dir, "probe-sessions.json.tmp-*")); err != nil {
		t.Fatal(err)
	} else if len(matches) != 0 {
		t.Fatalf("temp files still exist after failed save: %#v", matches)
	}
	if _, err := os.Stat(filepath.Join(target, "keep")); err != nil {
		t.Fatalf("existing target content was not preserved: %v", err)
	}
}

func TestProbeSessionStoreSaveLockedDoesNotDeleteExistingNeighborTempFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "probe-sessions.json")
	neighborTmp := target + ".tmp"
	if err := os.WriteFile(neighborTmp, []byte("user temp"), 0o600); err != nil {
		t.Fatalf("WriteFile neighbor temp returned error: %v", err)
	}

	store := &probeSessionStore{
		path: target,
		data: map[string]ProbeSessionBinding{
			"pi": {AgentSessionID: "session", UpdatedAt: time.Now().UTC()},
		},
	}
	if err := store.saveLocked(); err != nil {
		t.Fatalf("saveLocked() returned error: %v", err)
	}
	payload, err := os.ReadFile(neighborTmp)
	if err != nil {
		t.Fatalf("neighbor temp file was removed: %v", err)
	}
	if string(payload) != "user temp" {
		t.Fatalf("neighbor temp content = %q, want user temp", string(payload))
	}
}
