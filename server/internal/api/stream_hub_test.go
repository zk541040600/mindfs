package api

import (
	"testing"
	"time"
)

func TestReplayGenerationIsInvalidatedByNewTurn(t *testing.T) {
	hub := NewStreamHub(nil)
	hub.setPendingUserForRequest("root", "session-1", "request-A", "replying", "pi", "model", "", "", "", false, "turn A")
	if !hub.appendReplyEventForRequest("session-1", "request-A", StreamEvent{Type: "message_chunk"}) {
		t.Fatal("turn A event was not accepted")
	}
	hub.EnqueueSessionMessage("root", "session-1", "replying", QueuedUserMessage{
		ID:                 "queued-C",
		PendingUserMessage: PendingUserMessage{Content: "turn C"},
	})
	hub.mu.Lock()
	hub.replayStates[pendingClientKey("client-1", "session-1")] = &ClientReplayState{
		Status:      ClientStreamStatusReplay,
		ReplayIndex: 0,
		RequestID:   "request-A",
	}
	hub.mu.Unlock()

	step := hub.collectReplayStep("client-1", "session-1", "request-A")
	if !step.valid || len(step.events) != 1 {
		t.Fatalf("replay step = %#v, want copied turn A event", step)
	}
	hub.setPendingUserForRequest("root", "session-1", "request-B", "replying", "pi", "model", "", "", "", false, "turn B")

	if hub.replayRequestCurrent("client-1", "session-1", "request-A") {
		t.Fatal("copied turn A replay remained current after turn B started")
	}
	if next := hub.collectReplayStep("client-1", "session-1", "request-A"); next.valid {
		t.Fatalf("stale replay step = %#v, want invalidated replay", next)
	}
	if hub.replayQueueToClient("root", "client-1", "session-1", "request-A") {
		t.Fatal("stale turn A queue replay remained sendable after turn B started")
	}
}

func TestClearSessionPendingForRequestRejectsNewGeneration(t *testing.T) {
	hub := NewStreamHub(nil)
	hub.setPendingUserForRequest("root", "session-1", "request-A", "replying", "pi", "model", "", "", "", false, "turn A")
	if !hub.recordSessionTerminal("session-1", CompletedSessionState{RequestID: "request-A"}) {
		t.Fatal("turn A terminal was not recorded")
	}
	hub.setPendingUserForRequest("root", "session-1", "request-B", "replying", "pi", "model", "", "", "", false, "turn B")

	if hub.clearSessionPendingForRequest("session-1", "request-A") {
		t.Fatal("turn A cleared pending state after turn B started")
	}
	hub.mu.RLock()
	state := hub.pendingSessions["session-1"]
	current := hub.activeRequests["session-1"]
	hub.mu.RUnlock()
	if current != "request-B" || state == nil || state.User == nil || state.User.Content != "turn B" {
		t.Fatalf("current request = %q, state = %#v, want active turn B", current, state)
	}
}

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
