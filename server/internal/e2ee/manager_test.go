package e2ee

import (
	"bytes"
	"testing"
	"time"
)

func TestOpenSessionForClientZeroesReplacedSessionKey(t *testing.T) {
	manager := newTestManager()
	firstKey := []byte("0123456789abcdef0123456789abcdef")
	if _, err := manager.OpenSessionForClient("client", DerivedKey{Transport: firstKey}); err != nil {
		t.Fatalf("OpenSessionForClient first: %v", err)
	}
	manager.mu.RLock()
	firstSessionID := manager.clientIDs["client"]
	firstSession := manager.sessions[firstSessionID]
	firstStoredKey := firstSession.Key
	manager.mu.RUnlock()

	if _, err := manager.OpenSessionForClient("client", DerivedKey{Transport: []byte("abcdef0123456789abcdef0123456789")}); err != nil {
		t.Fatalf("OpenSessionForClient second: %v", err)
	}

	if !allZero(firstStoredKey) {
		t.Fatalf("replaced session key was not zeroed: %x", firstStoredKey)
	}
}

func TestSessionForClientZeroesExpiredSessionKey(t *testing.T) {
	manager := newTestManager()
	if _, err := manager.OpenSessionForClient("client", DerivedKey{Transport: []byte("0123456789abcdef0123456789abcdef")}); err != nil {
		t.Fatalf("OpenSessionForClient: %v", err)
	}
	manager.mu.Lock()
	sessionID := manager.clientIDs["client"]
	session := manager.sessions[sessionID]
	storedKey := session.Key
	session.LastSeenAt = time.Now().Add(-idleTTL - time.Minute)
	manager.mu.Unlock()

	if _, err := manager.SessionForClient("client"); err == nil {
		t.Fatal("expected expired session error")
	}
	if !allZero(storedKey) {
		t.Fatalf("expired session key was not zeroed: %x", storedKey)
	}
}

func TestCleanupExpiredZeroesExpiredSessionKey(t *testing.T) {
	manager := newTestManager()
	if _, err := manager.OpenSessionForClient("client", DerivedKey{Transport: []byte("0123456789abcdef0123456789abcdef")}); err != nil {
		t.Fatalf("OpenSessionForClient: %v", err)
	}
	manager.mu.Lock()
	sessionID := manager.clientIDs["client"]
	session := manager.sessions[sessionID]
	storedKey := session.Key
	session.LastSeenAt = time.Now().Add(-idleTTL - time.Minute)
	manager.mu.Unlock()

	manager.CleanupExpired()

	if !allZero(storedKey) {
		t.Fatalf("cleanup did not zero expired session key: %x", storedKey)
	}
}

func TestSessionForClientNoTouchDoesNotRefreshLastSeen(t *testing.T) {
	manager := newTestManager()
	if _, err := manager.OpenSessionForClient("client", DerivedKey{Transport: []byte("0123456789abcdef0123456789abcdef")}); err != nil {
		t.Fatalf("OpenSessionForClient: %v", err)
	}
	oldSeen := time.Now().Add(-time.Hour).UTC()
	manager.mu.Lock()
	sessionID := manager.clientIDs["client"]
	manager.sessions[sessionID].LastSeenAt = oldSeen
	manager.mu.Unlock()

	if _, err := manager.SessionForClientNoTouch("client"); err != nil {
		t.Fatalf("SessionForClientNoTouch: %v", err)
	}
	manager.mu.RLock()
	got := manager.sessions[sessionID].LastSeenAt
	manager.mu.RUnlock()
	if !got.Equal(oldSeen) {
		t.Fatalf("LastSeenAt changed to %s, want %s", got, oldSeen)
	}

	if err := manager.TouchSessionForClient("client"); err != nil {
		t.Fatalf("TouchSessionForClient: %v", err)
	}
	manager.mu.RLock()
	got = manager.sessions[sessionID].LastSeenAt
	manager.mu.RUnlock()
	if !got.After(oldSeen) {
		t.Fatalf("LastSeenAt = %s, want after %s", got, oldSeen)
	}
}

func newTestManager() *Manager {
	return NewManager(Config{
		Enabled:       true,
		NodeID:        "node",
		PairingSecret: "secret",
	})
}

func allZero(value []byte) bool {
	return bytes.Equal(value, make([]byte, len(value)))
}
