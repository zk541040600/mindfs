package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mindfs/server/internal/e2ee"

	"github.com/gorilla/websocket"
)

func TestStreamHubSequencesEncryptedV2Frames(t *testing.T) {
	serverConn := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Upgrade(w, r, nil, 1024, 1024)
		if err != nil {
			return
		}
		serverConn <- conn
	}))
	defer server.Close()

	clientConn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer clientConn.Close()
	conn := <-serverConn
	defer conn.Close()

	manager := e2ee.NewManager(e2ee.Config{Enabled: true, NodeID: "node", PairingSecret: "secret"})
	sess, err := manager.OpenSessionForClient("client", e2ee.DerivedKey{
		Transport:       []byte("0123456789abcdef0123456789abcdef"),
		ProtocolVersion: e2ee.ProtocolVersionV2,
	})
	if err != nil {
		t.Fatalf("OpenSessionForClient: %v", err)
	}
	hub := NewStreamHub(manager)

	writeAndRead := func(response WSResponse, wantSequence uint64) WSResponse {
		t.Helper()
		if err := hub.WriteJSON("client", conn, response); err != nil {
			t.Fatalf("WriteJSON: %v", err)
		}
		_, raw, readErr := clientConn.ReadMessage()
		if readErr != nil {
			t.Fatalf("ReadMessage: %v", readErr)
		}
		var envelope e2ee.CipherEnvelope
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatalf("Unmarshal envelope: %v", err)
		}
		plaintext, err := e2ee.DecryptBytes(sess.Key, &envelope)
		if err != nil {
			t.Fatalf("DecryptBytes: %v", err)
		}
		var frame E2EEWSFrame
		if err := json.Unmarshal(plaintext, &frame); err != nil {
			t.Fatalf("Unmarshal frame: %v", err)
		}
		if frame.Sequence != wantSequence {
			t.Fatalf("frame sequence = %d, want %d", frame.Sequence, wantSequence)
		}
		var decoded WSResponse
		if err := json.Unmarshal(frame.Message, &decoded); err != nil {
			t.Fatalf("Unmarshal response: %v", err)
		}
		return decoded
	}

	if got := writeAndRead(WSResponse{Type: "pong"}, 1); got.Type != "pong" {
		t.Fatalf("first response = %#v", got)
	}
	if got := writeAndRead(WSResponse{Type: "e2ee.error", Payload: map[string]any{"code": "e2ee_frame_invalid"}}, 2); got.Type != "e2ee.error" {
		t.Fatalf("second response = %#v", got)
	}
}

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
