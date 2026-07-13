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

func TestReplayValuesAreConsumedOnce(t *testing.T) {
	manager := newTestManager()
	expiresAt := time.Now().UTC().Add(time.Minute)

	if !manager.ConsumeRequestProof("client", "proof", expiresAt) {
		t.Fatal("first request proof was not consumed")
	}
	if manager.ConsumeRequestProof("client", "proof", expiresAt) {
		t.Fatal("replayed request proof was accepted")
	}
	if manager.ConsumeRequestProof("client", "expired", time.Now().UTC().Add(-time.Minute)) {
		t.Fatal("expired request proof was consumed")
	}
	if !manager.ConsumeOpenNonce("client", "nonce") {
		t.Fatal("first open nonce was not consumed")
	}
	if manager.ConsumeOpenNonce("client", "nonce") {
		t.Fatal("replayed open nonce was accepted")
	}
}

func TestTouchSessionExtendsOpenNonceReplayProtection(t *testing.T) {
	manager := newTestManager()
	if !manager.ConsumeOpenNonce("client", "nonce") {
		t.Fatal("open nonce was not consumed")
	}
	if _, err := manager.OpenSessionForClient("client", DerivedKey{Transport: []byte("0123456789abcdef0123456789abcdef")}); err != nil {
		t.Fatalf("OpenSessionForClient: %v", err)
	}
	key := "client\x00nonce"
	manager.mu.RLock()
	before := manager.usedOpenNonces[key]
	manager.mu.RUnlock()

	if err := manager.TouchSessionForClient("client"); err != nil {
		t.Fatalf("TouchSessionForClient: %v", err)
	}
	manager.mu.RLock()
	after := manager.usedOpenNonces[key]
	manager.mu.RUnlock()
	if !after.After(before) {
		t.Fatalf("open nonce expiry = %s, want after %s", after, before)
	}
}

func TestWSSequencesRejectReplayAndAdvanceServerFrames(t *testing.T) {
	manager := newTestManager()
	sess, err := manager.OpenSessionForClient("client", DerivedKey{
		Transport:       []byte("0123456789abcdef0123456789abcdef"),
		ProtocolVersion: ProtocolVersionV2,
	})
	if err != nil {
		t.Fatalf("OpenSessionForClient: %v", err)
	}

	if err := manager.ConsumeClientWSSequence("client", sess.ID, 1); err != nil {
		t.Fatalf("ConsumeClientWSSequence first: %v", err)
	}
	afterFirst, err := manager.SessionForClientNoTouch("client")
	if err != nil {
		t.Fatalf("SessionForClientNoTouch after first frame: %v", err)
	}
	if err := manager.ConsumeClientWSSequence("client", sess.ID, 1); err == nil || err.Error() != "e2ee_frame_replayed" {
		t.Fatalf("replayed frame error = %v, want e2ee_frame_replayed", err)
	}
	afterReplay, err := manager.SessionForClientNoTouch("client")
	if err != nil {
		t.Fatalf("SessionForClientNoTouch after replay: %v", err)
	}
	if !afterReplay.LastSeenAt.Equal(afterFirst.LastSeenAt) {
		t.Fatalf("LastSeenAt changed after replay: first=%s replay=%s", afterFirst.LastSeenAt, afterReplay.LastSeenAt)
	}

	firstServerSequence, err := manager.NextServerWSSequence("client", sess.ID)
	if err != nil {
		t.Fatalf("NextServerWSSequence first: %v", err)
	}
	secondServerSequence, err := manager.NextServerWSSequence("client", sess.ID)
	if err != nil {
		t.Fatalf("NextServerWSSequence second: %v", err)
	}
	if firstServerSequence != 1 || secondServerSequence != 2 {
		t.Fatalf("server sequences = %d, %d; want 1, 2", firstServerSequence, secondServerSequence)
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
