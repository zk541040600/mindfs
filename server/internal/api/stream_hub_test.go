package api

import (
	"testing"
	"time"
)

func TestClearSessionPendingDoesNotWaitForeverForReplayClient(t *testing.T) {
	hub := NewStreamHub(nil)
	hub.SetPendingReply("root", "session-1", "replying")

	hub.mu.Lock()
	hub.replayStates[pendingClientKey("client-1", "session-1")] = &ClientReplayState{
		Status:      ClientStreamStatusReplay,
		ReplayIndex: 0,
	}
	hub.mu.Unlock()

	previousWait := clearSessionPendingReplayWait
	clearSessionPendingReplayWait = 20 * time.Millisecond
	t.Cleanup(func() {
		clearSessionPendingReplayWait = previousWait
	})

	started := time.Now()
	hub.ClearSessionPending("session-1")
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("ClearSessionPending took %s, want bounded replay wait", elapsed)
	}

	hub.mu.RLock()
	defer hub.mu.RUnlock()
	if _, ok := hub.pendingSessions["session-1"]; ok {
		t.Fatal("pending session was not cleared")
	}
	if _, ok := hub.replayStates[pendingClientKey("client-1", "session-1")]; ok {
		t.Fatal("replay state was not cleared after bounded wait")
	}
}
