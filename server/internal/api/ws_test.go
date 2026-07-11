package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mindfs/server/internal/agent"
	agenttypes "mindfs/server/internal/agent/types"
	"mindfs/server/internal/e2ee"
	rootfs "mindfs/server/internal/fs"
	"mindfs/server/internal/session"

	"github.com/gorilla/websocket"
)

func TestSessionDoneResponseIncludesRequestIDPayload(t *testing.T) {
	resp := buildSessionDoneResponse("root", "session", "msg-123", false)
	if resp.ID != "msg-123" {
		t.Fatalf("ID = %q, want request id", resp.ID)
	}
	if got := resp.Payload["request_id"]; got != "msg-123" {
		t.Fatalf("payload request_id = %#v, want msg-123", got)
	}
}

func TestSessionErrorResponseIncludesRequestIDAndDoesNotMasqueradeAsDone(t *testing.T) {
	resp := buildSessionErrorResponse("root", "session", "msg-123", "session.message_failed", "upstream unavailable", false)
	if resp.Type != "session.error" || resp.ID != "msg-123" {
		t.Fatalf("response = %#v, want request-scoped session.error", resp)
	}
	if got := resp.Payload["request_id"]; got != "msg-123" {
		t.Fatalf("payload request_id = %#v, want msg-123", got)
	}
	if resp.Error == nil || resp.Error.Message != "upstream unavailable" {
		t.Fatalf("response error = %#v", resp.Error)
	}
}

func TestParseClientContext(t *testing.T) {
	payload := map[string]any{
		"context": map[string]any{
			"current_root": "ignored-by-payload",
			"selection": map[string]any{
				"file_path":  "docs/readme.md",
				"start_line": 1,
				"end_line":   3,
				"text":       "abc",
			},
		},
	}

	got := parseClientContext(payload, "mindfs")
	if got.CurrentRoot != "ignored-by-payload" {
		t.Fatalf("unexpected current root: %q", got.CurrentRoot)
	}
	if got.Selection == nil || got.Selection.Text != "abc" {
		t.Fatalf("unexpected selection: %#v", got.Selection)
	}

	got = parseClientContext(map[string]any{}, "fallback-root")
	if got.CurrentRoot != "fallback-root" {
		t.Fatalf("expected fallback root, got %q", got.CurrentRoot)
	}
}

func TestAppendReplyEventPrefixesTruncatedSummary(t *testing.T) {
	hub := NewStreamHub(nil)

	hub.AppendReplyEvent("sess-1", StreamEvent{
		Type: "message_chunk",
		Data: agenttypes.MessageChunk{Content: strings.Repeat("前", 601) + "后"},
	})

	snapshot := hub.PendingSessionSnapshot("sess-1")
	if !strings.HasPrefix(snapshot.Summary, "...") {
		t.Fatalf("summary should start with ellipsis when truncated, got %q", snapshot.Summary)
	}
	if !strings.HasSuffix(snapshot.Summary, "后") {
		t.Fatalf("summary should keep the end of the content, got %q", snapshot.Summary)
	}
}

func TestAppendReplyEventResetsSummaryAfterAuxiliaryEvent(t *testing.T) {
	hub := NewStreamHub(nil)

	hub.AppendReplyEvent("sess-1", StreamEvent{
		Type: string(agenttypes.EventTypeMessageChunk),
		Data: agenttypes.MessageChunk{Content: "before aux"},
	})
	hub.AppendReplyEvent("sess-1", StreamEvent{
		Type: string(agenttypes.EventTypePlanUpdate),
		Data: agenttypes.PlanUpdate{Content: "- inspect"},
	})
	hub.AppendReplyEvent("sess-1", StreamEvent{
		Type: string(agenttypes.EventTypeMessageChunk),
		Data: agenttypes.MessageChunk{Content: "after aux"},
	})

	snapshot := hub.PendingSessionSnapshot("sess-1")
	if snapshot.Summary != "after aux" {
		t.Fatalf("summary = %q, want aux boundary to discard previous content", snapshot.Summary)
	}
}

func TestAppendReplyEventCoalescesGoalStateForReplay(t *testing.T) {
	hub := NewStreamHub(nil)
	hub.AppendReplyEvent("sess-1", StreamEvent{
		Type: string(agenttypes.EventTypeGoalState),
		Data: agenttypes.GoalState{Status: "active", Objective: "repair history"},
	})
	hub.AppendReplyEvent("sess-1", StreamEvent{
		Type: string(agenttypes.EventTypeGoalState),
		Data: agenttypes.GoalState{Status: "paused", Objective: "repair history", PauseReason: "approval required"},
	})

	hub.mu.RLock()
	state := hub.pendingSessions["sess-1"]
	events := append([]StreamEvent(nil), state.ReplyingList...)
	hub.mu.RUnlock()
	if len(events) != 1 {
		t.Fatalf("goal replay events = %#v, want latest event only", events)
	}
	goal, ok := events[0].Data.(agenttypes.GoalState)
	if !ok || goal.Status != "paused" {
		t.Fatalf("latest goal replay event = %#v", events[0])
	}
}

func TestStreamHubStoresErrorAsTerminalInsteadOfCompletion(t *testing.T) {
	hub := NewStreamHub(nil)
	hub.BroadcastSessionError("root", "session", "msg-123", "session.message_failed", "upstream unavailable")

	hub.mu.RLock()
	terminal := hub.completed["session"]
	hub.mu.RUnlock()
	if terminal == nil || terminal.RequestID != "msg-123" || terminal.ErrorMessage != "upstream unavailable" {
		t.Fatalf("terminal state = %#v", terminal)
	}
}

func TestRunSessionMessageFailureBroadcastsErrorWithoutDone(t *testing.T) {
	rootDir := t.TempDir()
	registry := rootfs.NewRegistry(filepath.Join(t.TempDir(), "registry.json"))
	root, err := registry.Upsert(rootDir)
	if err != nil {
		t.Fatalf("registry.Upsert: %v", err)
	}
	pool := agent.NewPool(agent.Config{Agents: []agent.Definition{{
		Name:     "pi",
		Command:  writeFailingPiSDKForWSTest(t),
		Protocol: agent.ProtocolPiSDK,
	}}})
	defer pool.CloseAll()
	appContext := &AppContext{Dirs: registry, Agents: pool}
	manager, err := appContext.GetSessionManager(root.ID)
	if err != nil {
		t.Fatalf("GetSessionManager: %v", err)
	}
	created, err := manager.Create(context.Background(), session.CreateInput{Type: session.TypeChat, Agent: "pi", Name: "failure test"})
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}

	handler := &WSHandler{AppContext: appContext}
	handler.runSessionMessage(sessionMessageJob{
		RootID:      root.ID,
		Key:         created.Key,
		RequestID:   "request-failed",
		SessionType: session.TypeChat,
		SessionName: created.Name,
		User: PendingUserMessage{
			Agent:   "pi",
			Content: "fail this turn",
		},
	})

	hub := appContext.GetSessionStreamHub()
	hub.mu.RLock()
	terminal := hub.completed[created.Key]
	hub.mu.RUnlock()
	if terminal == nil || terminal.RequestID != "request-failed" || terminal.ErrorMessage == "" {
		t.Fatalf("terminal state = %#v, want request-scoped error", terminal)
	}
	if hub.IsSessionReplying(created.Key) {
		t.Fatal("failed session remained pending")
	}
	loaded, err := manager.Get(context.Background(), created.Key, 0)
	if err != nil {
		t.Fatalf("Get session: %v", err)
	}
	if len(loaded.Exchanges) != 1 || loaded.Exchanges[0].Role != "user" {
		t.Fatalf("exchanges = %#v, want user-only failure persistence", loaded.Exchanges)
	}
}

func TestStartNextQueuedSessionMessagePreservesRequestID(t *testing.T) {
	rootDir := t.TempDir()
	registry := rootfs.NewRegistry(filepath.Join(t.TempDir(), "registry.json"))
	root, err := registry.Upsert(rootDir)
	if err != nil {
		t.Fatalf("registry.Upsert: %v", err)
	}
	pool := agent.NewPool(agent.Config{Agents: []agent.Definition{{
		Name:     "pi",
		Command:  writeSuccessfulPiSDKForWSTest(t),
		Protocol: agent.ProtocolPiSDK,
	}}})
	defer pool.CloseAll()
	appContext := &AppContext{Dirs: registry, Agents: pool}
	manager, err := appContext.GetSessionManager(root.ID)
	if err != nil {
		t.Fatalf("GetSessionManager: %v", err)
	}
	created, err := manager.Create(context.Background(), session.CreateInput{Type: session.TypeChat, Agent: "pi", Name: "queue request test"})
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}

	hub := appContext.GetSessionStreamHub()
	hub.EnqueueSessionMessage(root.ID, created.Key, created.Name, QueuedUserMessage{
		ID: "queued-request",
		PendingUserMessage: PendingUserMessage{
			Agent:     "pi",
			Content:   "run queued turn",
			Timestamp: time.Now().UTC(),
		},
	})
	(&WSHandler{AppContext: appContext}).startNextQueuedSessionMessage(root.ID, created.Key)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		hub.mu.RLock()
		terminal := hub.completed[created.Key]
		hub.mu.RUnlock()
		if terminal != nil {
			if terminal.RequestID != "queued-request" || terminal.ErrorMessage != "" {
				t.Fatalf("terminal state = %#v, want queued request completion", terminal)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("queued session did not complete")
}

func TestSessionMessageContextHasNoDeadlineWithoutAppContext(t *testing.T) {
	handler := &WSHandler{}

	ctx, cancel := handler.sessionMessageContext()
	defer cancel()

	if _, ok := ctx.Deadline(); ok {
		t.Fatal("session message context unexpectedly has a deadline")
	}
}

func TestUpdateToEventMapsExtensionUI(t *testing.T) {
	event := updateToEvent(agenttypes.Event{Type: agenttypes.EventTypeExtensionUI, Data: agenttypes.ExtensionUIRequest{
		ID:     "ui-1",
		Method: "select",
		Payload: map[string]any{
			"title":   "Pick",
			"options": []string{"Allow", "Block"},
		},
	}})
	if event == nil || event.Type != "extension_ui" {
		t.Fatalf("unexpected event: %#v", event)
	}
	request, ok := event.Data.(agenttypes.ExtensionUIRequest)
	if !ok || request.ID != "ui-1" || request.Method != "select" {
		t.Fatalf("unexpected payload: %#v", event.Data)
	}
}

func TestUpdateToEventMapsGoalState(t *testing.T) {
	event := updateToEvent(agenttypes.Event{Type: agenttypes.EventTypeGoalState, Data: agenttypes.GoalState{
		Status:    "paused",
		Objective: "repair history",
	}})
	if event == nil || event.Type != "goal_state" {
		t.Fatalf("unexpected event: %#v", event)
	}
	goal, ok := event.Data.(agenttypes.GoalState)
	if !ok || goal.Status != "paused" {
		t.Fatalf("unexpected payload: %#v", event.Data)
	}
}

func TestSessionMessageContextUsesAgentPoolLifecycle(t *testing.T) {
	pool := agent.NewPool(agent.Config{})
	defer pool.CloseAll()

	handler := &WSHandler{AppContext: &AppContext{Agents: pool}}
	ctx, cancel := handler.sessionMessageContext()
	defer cancel()

	if _, ok := ctx.Deadline(); ok {
		t.Fatal("session message context unexpectedly has a deadline")
	}
	pool.CloseAll()
	select {
	case <-ctx.Done():
	default:
		t.Fatal("expected session message context to be canceled when agent pool closes")
	}
}

func TestTurnUpdateTrackerWaitIdleWaitsForSettleWindow(t *testing.T) {
	tracker := newTurnUpdateTracker()
	tracker.Begin()
	done := make(chan bool, 1)
	go func() {
		done <- tracker.WaitIdle(context.Background(), 30*time.Millisecond, 500*time.Millisecond)
	}()

	select {
	case <-done:
		t.Fatal("WaitIdle returned while update was in-flight")
	case <-time.After(20 * time.Millisecond):
	}

	tracker.End()
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("WaitIdle returned false after update finished")
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("WaitIdle did not return after settle window")
	}
}

func TestTurnUpdateTrackerWaitIdleTimesOutWhenUpdateNeverEnds(t *testing.T) {
	tracker := newTurnUpdateTracker()
	tracker.Begin()

	if tracker.WaitIdle(context.Background(), 10*time.Millisecond, 30*time.Millisecond) {
		t.Fatal("expected WaitIdle to time out while update remains in-flight")
	}
}

func TestStreamHubFrozenQueueBlocksAutomaticPopUntilUnfrozen(t *testing.T) {
	hub := NewStreamHub(nil)
	rootID := "root"
	sessionKey := "session"

	hub.EnqueueSessionMessage(rootID, sessionKey, "Session", QueuedUserMessage{
		ID: "first",
		PendingUserMessage: PendingUserMessage{
			Content:   "first message",
			Timestamp: time.Now().UTC(),
		},
	})
	hub.EnqueueSessionMessage(rootID, sessionKey, "Session", QueuedUserMessage{
		ID: "second",
		PendingUserMessage: PendingUserMessage{
			Content:   "second message",
			Timestamp: time.Now().UTC(),
		},
	})

	frozenQueue, frozen := hub.FreezeQueuedSessionMessages(sessionKey)
	if !frozen {
		t.Fatal("expected queue freeze to succeed")
	}
	if len(frozenQueue) != 2 {
		t.Fatalf("expected frozen queue snapshot to contain 2 items, got %d", len(frozenQueue))
	}
	if _, queue, ok := hub.PopQueuedSessionMessage(sessionKey, ""); ok {
		t.Fatal("expected frozen queue to block automatic pop")
	} else if len(queue) != 2 {
		t.Fatalf("expected frozen queue to remain intact, got %d items", len(queue))
	}

	queue, ok := hub.PromoteQueuedSessionMessage(sessionKey, "second")
	if !ok {
		t.Fatal("expected promote to succeed")
	}
	if len(queue) != 2 || queue[0].ID != "second" {
		t.Fatalf("expected promoted item at queue head, got %#v", queue)
	}

	item, queue, ok := hub.PopQueuedSessionMessage(sessionKey, "")
	if !ok {
		t.Fatal("expected promoted queue to be unfrozen")
	}
	if item.ID != "second" {
		t.Fatalf("expected promoted item to pop first, got %q", item.ID)
	}
	if len(queue) != 1 || queue[0].ID != "first" {
		t.Fatalf("expected remaining queue to contain first item, got %#v", queue)
	}
}

func TestStreamHubUnfreezeQueueAllowsAutomaticPop(t *testing.T) {
	hub := NewStreamHub(nil)
	sessionKey := "session"
	hub.EnqueueSessionMessage("root", sessionKey, "Session", QueuedUserMessage{
		ID: "first",
		PendingUserMessage: PendingUserMessage{
			Content:   "first message",
			Timestamp: time.Now().UTC(),
		},
	})
	_, frozen := hub.FreezeQueuedSessionMessages(sessionKey)
	if !frozen {
		t.Fatal("expected queue freeze to succeed")
	}

	unfrozenQueue, changed := hub.UnfreezeQueuedSessionMessages(sessionKey)
	if !changed {
		t.Fatal("expected queue unfreeze to report changed")
	}
	if len(unfrozenQueue) != 1 {
		t.Fatalf("expected unfreeze queue snapshot to contain 1 item, got %d", len(unfrozenQueue))
	}
	item, queue, ok := hub.PopQueuedSessionMessage(sessionKey, "")
	if !ok {
		t.Fatal("expected automatic pop after unfreeze")
	}
	if item.ID != "first" {
		t.Fatalf("expected first item, got %q", item.ID)
	}
	if len(queue) != 0 {
		t.Fatalf("expected empty queue, got %#v", queue)
	}
}

func TestAcknowledgeStaleSessionCancelKeepsActiveTurnAndUnfreezesQueue(t *testing.T) {
	hub := NewStreamHub(nil)
	rootID := "root"
	sessionKey := "session"
	hub.SetPendingUser(rootID, sessionKey, "Session", "pi", "", "", "", "", false, "new turn")
	hub.EnqueueSessionMessage(rootID, sessionKey, "Session", QueuedUserMessage{
		ID: "queued",
		PendingUserMessage: PendingUserMessage{
			Content:   "queued message",
			Timestamp: time.Now().UTC(),
		},
	})
	if _, frozen := hub.FreezeQueuedSessionMessages(sessionKey); !frozen {
		t.Fatal("expected queue freeze to succeed")
	}

	handler := &WSHandler{}
	handler.acknowledgeStaleSessionCancel(nil, "client", "cancel-ws-id", rootID, sessionKey, "old-request", hub)

	if !hub.IsSessionReplying(sessionKey) {
		t.Fatal("stale cancel acknowledgement cleared the newer active turn")
	}
	item, _, ok := hub.PopQueuedSessionMessage(sessionKey, "")
	if !ok {
		t.Fatal("stale cancel acknowledgement left the queue frozen")
	}
	if item.ID != "queued" {
		t.Fatalf("popped item = %q, want queued", item.ID)
	}
	hub.mu.RLock()
	completed := hub.completed[sessionKey]
	hub.mu.RUnlock()
	if completed != nil {
		t.Fatalf("stale cancel stored replay completion: %#v", completed)
	}
}

func TestRequireWSProofAcceptsValidProof(t *testing.T) {
	clientID := "web-test"
	key := []byte("0123456789abcdef0123456789abcdef")
	manager := e2ee.NewManager(e2ee.Config{
		Enabled:       true,
		NodeID:        "node",
		PairingSecret: "secret",
	})
	if _, err := manager.OpenSessionForClient(clientID, e2ee.DerivedKey{Transport: key}); err != nil {
		t.Fatalf("OpenSessionForClient: %v", err)
	}
	handler := &WSHandler{AppContext: &AppContext{E2EE: manager}}
	ts := time.Now().UTC().Format(time.RFC3339)
	proofPath := "/ws?client_id=" + url.QueryEscape(clientID)
	proof := e2ee.BuildRequestProof(key, http.MethodGet, proofPath, ts, clientID)
	req := httptest.NewRequest(http.MethodGet, proofPath+"&"+wsTSQuery+"="+url.QueryEscape(ts)+"&"+wsProofQuery+"="+url.QueryEscape(proof), nil)

	if err := handler.requireWSProof(req, clientID); err != nil {
		t.Fatalf("requireWSProof() error = %v", err)
	}
}

func TestRequireWSProofRejectsMissingProofWhenE2EEEnabled(t *testing.T) {
	clientID := "web-test"
	manager := e2ee.NewManager(e2ee.Config{
		Enabled:       true,
		NodeID:        "node",
		PairingSecret: "secret",
	})
	handler := &WSHandler{AppContext: &AppContext{E2EE: manager}}
	req := httptest.NewRequest(http.MethodGet, "/ws?client_id="+url.QueryEscape(clientID), nil)

	if err := handler.requireWSProof(req, clientID); err == nil {
		t.Fatal("expected missing proof to be rejected")
	}
}

func TestWSProofPathExcludesProofQueryParams(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/ws?client_id=web-test&e2ee_ts=now&e2ee_proof=proof", nil)

	if got, want := wsProofPath(req), "/ws?client_id=web-test"; got != want {
		t.Fatalf("wsProofPath() = %q, want %q", got, want)
	}
}

func TestWSRejectsMessagesOverReadLimit(t *testing.T) {
	oldLimit := wsReadLimitBytes
	wsReadLimitBytes = 32
	t.Cleanup(func() { wsReadLimitBytes = oldLimit })

	server := httptest.NewServer(&WSHandler{})
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws?client_id=limit-test"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	payload := strings.Repeat("x", int(wsReadLimitBytes)+1)
	if err := conn.WriteMessage(websocket.TextMessage, []byte(payload)); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("expected websocket read to fail after oversized message")
	}
}

func writeFailingPiSDKForWSTest(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-node")
	script := `#!/usr/bin/env python3
import json
import sys

def send(obj):
    print(json.dumps(obj), flush=True)

for line in sys.stdin:
    req = json.loads(line)
    req_id = req.get("id")
    typ = req.get("type")
    if typ == "start_sdk_runtime":
        send({"id": req_id, "type": "response", "command": typ, "success": True, "data": {"sessionId": "ws-failing-pi"}})
    elif typ == "get_state":
        send({"id": req_id, "type": "response", "command": typ, "success": True, "data": {"sessionId": "ws-failing-pi", "isStreaming": False, "pendingMessageCount": 0}})
    elif typ == "prompt":
        send({"id": req_id, "type": "response", "command": typ, "success": False, "error": {"code": "E_UPSTREAM", "message": "upstream unavailable"}})
    else:
        send({"id": req_id, "type": "response", "command": typ, "success": True, "data": {}})
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake pi sdk: %v", err)
	}
	return path
}

func writeSuccessfulPiSDKForWSTest(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-node")
	script := `#!/usr/bin/env python3
import json
import sys

def send(obj):
    print(json.dumps(obj), flush=True)

for line in sys.stdin:
    req = json.loads(line)
    req_id = req.get("id")
    typ = req.get("type")
    if typ == "start_sdk_runtime":
        send({"id": req_id, "type": "response", "command": typ, "success": True, "data": {"sessionId": "ws-successful-pi"}})
    elif typ == "get_state":
        send({"id": req_id, "type": "response", "command": typ, "success": True, "data": {"sessionId": "ws-successful-pi", "isStreaming": False, "pendingMessageCount": 0}})
    elif typ == "prompt":
        send({"id": req_id, "type": "response", "command": typ, "success": True, "data": {"runtime": "sdk"}})
        send({"type": "message_start", "message": {"role": "assistant", "content": []}})
        send({"type": "message_update", "message": {"role": "assistant", "content": [{"type": "text", "text": "queued response"}]}, "assistantMessageEvent": {"type": "text_delta", "delta": "queued response"}})
        send({"type": "message_end", "message": {"role": "assistant", "stopReason": "end_turn", "content": [{"type": "text", "text": "queued response"}]}})
        send({"type": "runtime_settled", "reason": "queued_request_test"})
    else:
        send({"id": req_id, "type": "response", "command": typ, "success": True, "data": {}})
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake pi sdk: %v", err)
	}
	return path
}
