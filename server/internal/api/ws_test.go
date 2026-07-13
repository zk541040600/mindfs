package api

import (
	"context"
	"encoding/json"
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

func TestSessionStreamResponseIncludesRequestIDPayload(t *testing.T) {
	resp := buildSessionStreamResponse("root", "session", "msg-123", &StreamEvent{Type: "message_chunk"})
	if got := resp.Payload["request_id"]; got != "msg-123" {
		t.Fatalf("payload request_id = %#v, want msg-123", got)
	}
}

func TestReplayQueueResponseIncludesRequestIDPayload(t *testing.T) {
	resp := buildSessionQueueUpdatedResponse("root", "session", "msg-123", nil, false)
	if got := resp.Payload["request_id"]; got != "msg-123" {
		t.Fatalf("payload request_id = %#v, want msg-123", got)
	}
}

func TestSessionDoneResponseIncludesRequestIDPayload(t *testing.T) {
	resp := buildSessionDoneResponse("root", "session", "msg-123", false)
	if resp.ID != "msg-123" {
		t.Fatalf("ID = %q, want request id", resp.ID)
	}
	if got := resp.Payload["request_id"]; got != "msg-123" {
		t.Fatalf("payload request_id = %#v, want msg-123", got)
	}
}

func TestSessionCancelledResponseIncludesRequestIDAndReplay(t *testing.T) {
	resp := buildSessionCancelledResponse("cancel-123", "root", "session", "msg-123", true)
	if resp.Type != "session.cancelled" || resp.ID != "cancel-123" {
		t.Fatalf("response = %#v, want request-scoped session.cancelled", resp)
	}
	if got := resp.Payload["request_id"]; got != "msg-123" {
		t.Fatalf("payload request_id = %#v, want msg-123", got)
	}
	if got := resp.Payload["replay"]; got != true {
		t.Fatalf("payload replay = %#v, want true", got)
	}
}

func TestStaleSessionCancelledResponseIsNonTerminalHint(t *testing.T) {
	resp := buildStaleSessionCancelledResponse("cancel-123", "root", "session", "old-request")
	if got := resp.Payload["stale"]; got != true {
		t.Fatalf("payload stale = %#v, want true", got)
	}
	if _, replay := resp.Payload["replay"]; replay {
		t.Fatalf("stale response unexpectedly marked for replay: %#v", resp.Payload)
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

func TestStreamHubCancellationDropsLateStreamAndCannotBecomeDone(t *testing.T) {
	hub := NewStreamHub(nil)
	hub.SetPendingUser("root", "session", "title", "pi", "model", "", "", "", false, "cancel me")
	hub.BroadcastSessionCancelled("root", "session", "cancel-123", "msg-123")

	if accepted := hub.AppendReplyEvent("session", StreamEvent{
		Type: "message_chunk",
		Data: agenttypes.MessageChunk{Content: "late after cancel"},
	}); accepted {
		t.Fatal("late stream event was accepted after cancellation")
	}
	hub.BroadcastSessionDone("root", "session", "msg-123")
	hub.BroadcastSessionError("root", "session", "msg-123", "session.message_failed", "late failure")

	hub.mu.RLock()
	terminal := hub.completed["session"]
	_, pending := hub.pendingSessions["session"]
	hub.mu.RUnlock()
	if terminal == nil || !terminal.Cancelled || terminal.RequestID != "msg-123" || terminal.ErrorMessage != "" {
		t.Fatalf("terminal state = %#v, want request-scoped cancellation", terminal)
	}
	if pending {
		t.Fatal("cancelled session was recreated as pending by a late event")
	}
}

func TestStreamHubRejectsStaleCallbacksAfterNewTurnStarts(t *testing.T) {
	hub := NewStreamHub(nil)
	hub.setPendingUserForRequest("root", "session", "request-A", "title", "pi", "model", "", "", "", false, "turn A")
	hub.BroadcastSessionCancelled("root", "session", "cancel-A", "request-A")
	hub.setPendingUserForRequest("root", "session", "request-B", "title", "pi", "model", "", "", "", false, "turn B")

	if accepted := hub.appendReplyEventForRequest("session", "request-A", StreamEvent{
		Type: "message_chunk",
		Data: agenttypes.MessageChunk{Content: "late A"},
	}); accepted {
		t.Fatal("late turn A stream was accepted after turn B started")
	}
	hub.BroadcastSessionDone("root", "session", "request-A")
	hub.BroadcastSessionError("root", "session", "request-A", "session.message_failed", "late A failure")

	hub.mu.RLock()
	terminal := hub.completed["session"]
	hub.mu.RUnlock()
	if terminal != nil {
		t.Fatalf("stale terminal = %#v, want turn B to remain active", terminal)
	}
	if accepted := hub.appendReplyEventForRequest("session", "request-B", StreamEvent{
		Type: "message_chunk",
		Data: agenttypes.MessageChunk{Content: "turn B"},
	}); !accepted {
		t.Fatal("current turn B stream was rejected")
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

func TestRunSessionMessageCancellationBroadcastsTerminal(t *testing.T) {
	rootDir := t.TempDir()
	registry := rootfs.NewRegistry(filepath.Join(t.TempDir(), "registry.json"))
	root, err := registry.Upsert(rootDir)
	if err != nil {
		t.Fatalf("registry.Upsert: %v", err)
	}
	pool := agent.NewPool(agent.Config{Agents: []agent.Definition{{
		Name:     "pi",
		Command:  writeCancelledPiSDKForWSTest(t),
		Protocol: agent.ProtocolPiSDK,
	}}})
	defer pool.CloseAll()
	appContext := &AppContext{Dirs: registry, Agents: pool}
	manager, err := appContext.GetSessionManager(root.ID)
	if err != nil {
		t.Fatalf("GetSessionManager: %v", err)
	}
	created, err := manager.Create(context.Background(), session.CreateInput{Type: session.TypeChat, Agent: "pi", Name: "cancellation test"})
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}

	handler := &WSHandler{AppContext: appContext}
	handler.runSessionMessage(sessionMessageJob{
		RootID:      root.ID,
		Key:         created.Key,
		RequestID:   "request-cancelled",
		SessionType: session.TypeChat,
		SessionName: created.Name,
		User: PendingUserMessage{
			Agent:   "pi",
			Content: "cancel this turn",
		},
	})

	hub := appContext.GetSessionStreamHub()
	hub.mu.RLock()
	terminal := hub.completed[created.Key]
	hub.mu.RUnlock()
	if terminal == nil || terminal.RequestID != "request-cancelled" || !terminal.Cancelled {
		t.Fatalf("terminal state = %#v, want request-scoped cancellation", terminal)
	}
	if hub.IsSessionReplying(created.Key) {
		t.Fatal("cancelled session remained pending")
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

func TestSessionJobShutdownWaitsForAcceptedQueueDrain(t *testing.T) {
	handler := &WSHandler{}
	parentStarted := make(chan struct{})
	releaseParent := make(chan struct{})
	childStarted := make(chan struct{})
	releaseChild := make(chan struct{})
	childAdmission := make(chan bool, 1)

	if !handler.startSessionJob(false, func() {
		close(parentStarted)
		<-releaseParent
		childAdmission <- handler.startSessionJob(true, func() {
			close(childStarted)
			<-releaseChild
		})
	}) {
		t.Fatal("initial browser message job was rejected")
	}
	<-parentStarted

	handler.BeginSessionShutdown()
	if handler.startSessionJob(false, func() {}) {
		t.Fatal("new browser message job was accepted after shutdown began")
	}

	waitCtx, cancelWait := context.WithTimeout(context.Background(), time.Second)
	defer cancelWait()
	waitDone := make(chan error, 1)
	go func() {
		waitDone <- handler.WaitSessionJobs(waitCtx)
	}()

	close(releaseParent)
	if admitted := <-childAdmission; !admitted {
		t.Fatal("accepted queued continuation was rejected during shutdown drain")
	}
	<-childStarted
	select {
	case err := <-waitDone:
		t.Fatalf("WaitSessionJobs returned before queued continuation settled: %v", err)
	case <-time.After(30 * time.Millisecond):
	}

	close(releaseChild)
	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatalf("WaitSessionJobs returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("WaitSessionJobs did not finish after accepted chain drained")
	}
}

func TestSessionShutdownPersistsAcceptedQueuedMessages(t *testing.T) {
	ctx := context.Background()
	rootDir := t.TempDir()
	registry := rootfs.NewRegistry(filepath.Join(t.TempDir(), "registry.json"))
	root, err := registry.Upsert(rootDir)
	if err != nil {
		t.Fatalf("registry.Upsert: %v", err)
	}
	manager := session.NewManager(root)
	created, err := manager.Create(ctx, session.CreateInput{Type: session.TypeChat, Agent: "pi", Name: "shutdown drain"})
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}

	promptMarker := filepath.Join(t.TempDir(), "prompt-started")
	pool := agent.NewPool(agent.Config{Agents: []agent.Definition{{
		Name:     "pi",
		Command:  writeShutdownBlockingPiSDKForWSTest(t),
		Protocol: agent.ProtocolPiSDK,
		Env:      map[string]string{"MINDFS_TEST_PROMPT_MARKER": promptMarker},
	}}})
	defer pool.CloseAll()
	appContext := &AppContext{Dirs: registry, Agents: pool}
	appContext.mu.Lock()
	appContext.roots = map[string]*RootContext{
		root.ID: {Session: manager},
	}
	appContext.mu.Unlock()
	handler := &WSHandler{AppContext: appContext}

	if !handler.startSessionJob(false, func() {
		handler.runSessionMessage(sessionMessageJob{
			RootID:      root.ID,
			Key:         created.Key,
			RequestID:   "request-active",
			SessionType: session.TypeChat,
			SessionName: created.Name,
			User: PendingUserMessage{
				Agent:   "pi",
				Content: "persist active input",
			},
		})
	}) {
		t.Fatal("active message job was rejected")
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, statErr := os.Stat(promptMarker); statErr == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("active Pi prompt did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}

	hub := appContext.GetSessionStreamHub()
	hub.EnqueueSessionMessage(root.ID, created.Key, created.Name, QueuedUserMessage{
		ID: "request-queued",
		PendingUserMessage: PendingUserMessage{
			Agent:     "pi",
			Content:   "persist queued input",
			Timestamp: time.Now().UTC(),
		},
	})

	handler.BeginSessionShutdown()
	pool.CloseAll()
	waitCtx, cancelWait := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelWait()
	if err := handler.WaitSessionJobs(waitCtx); err != nil {
		t.Fatalf("WaitSessionJobs: %v", err)
	}

	loaded, err := manager.Get(ctx, created.Key, 0)
	if err != nil {
		t.Fatalf("Get drained session: %v", err)
	}
	if len(loaded.Exchanges) != 2 {
		t.Fatalf("drained exchanges = %#v, want two persisted user inputs", loaded.Exchanges)
	}
	if loaded.Exchanges[0].Role != "user" || loaded.Exchanges[0].Content != "persist active input" {
		t.Fatalf("first drained exchange = %#v", loaded.Exchanges[0])
	}
	if loaded.Exchanges[1].Role != "user" || loaded.Exchanges[1].Content != "persist queued input" {
		t.Fatalf("second drained exchange = %#v", loaded.Exchanges[1])
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

func TestRequireWSProofRejectsReplay(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	clientID := "web-test"
	manager := e2ee.NewManager(e2ee.Config{Enabled: true, NodeID: "node", PairingSecret: "secret"})
	if _, err := manager.OpenSessionForClient(clientID, e2ee.DerivedKey{Transport: key}); err != nil {
		t.Fatalf("OpenSessionForClient: %v", err)
	}
	handler := &WSHandler{AppContext: &AppContext{E2EE: manager}}
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	proofPath := "/ws?client_id=" + url.QueryEscape(clientID)
	proof := e2ee.BuildRequestProof(key, http.MethodGet, proofPath, ts, clientID)
	req := httptest.NewRequest(http.MethodGet, proofPath+"&"+wsTSQuery+"="+url.QueryEscape(ts)+"&"+wsProofQuery+"="+url.QueryEscape(proof), nil)
	if err := handler.requireWSProof(req, clientID); err != nil {
		t.Fatalf("first requireWSProof returned error: %v", err)
	}
	if err := handler.requireWSProof(req, clientID); err == nil || !strings.Contains(err.Error(), "e2ee_proof_replayed") {
		t.Fatalf("replayed requireWSProof error = %v, want e2ee_proof_replayed", err)
	}
}

func TestWSRejectsReplayedV2Frame(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	clientID := "web-v2"
	manager := e2ee.NewManager(e2ee.Config{Enabled: true, NodeID: "node", PairingSecret: "secret"})
	if _, err := manager.OpenSessionForClient(clientID, e2ee.DerivedKey{Transport: key, ProtocolVersion: e2ee.ProtocolVersionV2}); err != nil {
		t.Fatalf("OpenSessionForClient: %v", err)
	}
	server := httptest.NewServer(&WSHandler{AppContext: &AppContext{E2EE: manager}})
	defer server.Close()

	ts := time.Now().UTC().Format(time.RFC3339Nano)
	proofPath := "/ws?client_id=" + url.QueryEscape(clientID)
	proof := e2ee.BuildRequestProof(key, http.MethodGet, proofPath, ts, clientID)
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + proofPath + "&" + wsTSQuery + "=" + url.QueryEscape(ts) + "&" + wsProofQuery + "=" + url.QueryEscape(proof)
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	request, err := json.Marshal(WSRequest{ID: "ping-1", Type: "ping", Payload: map[string]any{}})
	if err != nil {
		t.Fatalf("Marshal request: %v", err)
	}
	frame, err := json.Marshal(E2EEWSFrame{Sequence: 1, Message: request})
	if err != nil {
		t.Fatalf("Marshal frame: %v", err)
	}
	envelope, err := e2ee.EncryptBytes(key, frame)
	if err != nil {
		t.Fatalf("EncryptBytes: %v", err)
	}
	wire, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("Marshal envelope: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, wire); err != nil {
		t.Fatalf("WriteMessage first frame: %v", err)
	}

	readResponse := func(wantSequence uint64) WSResponse {
		t.Helper()
		if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
			t.Fatalf("SetReadDeadline: %v", err)
		}
		_, raw, readErr := conn.ReadMessage()
		if readErr != nil {
			t.Fatalf("ReadMessage: %v", readErr)
		}
		var responseEnvelope e2ee.CipherEnvelope
		if err := json.Unmarshal(raw, &responseEnvelope); err != nil {
			t.Fatalf("Unmarshal response envelope: %v", err)
		}
		plaintext, err := e2ee.DecryptBytes(key, &responseEnvelope)
		if err != nil {
			t.Fatalf("DecryptBytes response: %v", err)
		}
		var responseFrame E2EEWSFrame
		if err := json.Unmarshal(plaintext, &responseFrame); err != nil {
			t.Fatalf("Unmarshal response frame: %v", err)
		}
		if responseFrame.Sequence != wantSequence {
			t.Fatalf("response sequence = %d, want %d", responseFrame.Sequence, wantSequence)
		}
		var response WSResponse
		if err := json.Unmarshal(responseFrame.Message, &response); err != nil {
			t.Fatalf("Unmarshal response: %v", err)
		}
		return response
	}

	if response := readResponse(1); response.Type != "pong" {
		t.Fatalf("first response = %#v, want pong", response)
	}
	if err := conn.WriteMessage(websocket.TextMessage, wire); err != nil {
		t.Fatalf("WriteMessage replay: %v", err)
	}
	response := readResponse(2)
	if response.Type != "e2ee.error" || response.Payload["code"] != "e2ee_frame_replayed" {
		t.Fatalf("replay response = %#v, want encrypted e2ee_frame_replayed", response)
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

func writeCancelledPiSDKForWSTest(t *testing.T) string {
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
        send({"id": req_id, "type": "response", "command": typ, "success": True, "data": {"sessionId": "ws-cancelled-pi"}})
    elif typ == "get_state":
        send({"id": req_id, "type": "response", "command": typ, "success": True, "data": {"sessionId": "ws-cancelled-pi", "isStreaming": False, "pendingMessageCount": 0}})
    elif typ == "prompt":
        send({"id": req_id, "type": "response", "command": typ, "success": True, "data": {"runtime": "sdk"}})
        send({"type": "agent_start"})
        send({"type": "runtime_settled", "reason": "upstream_cancelled", "errorMessage": "operation cancelled"})
    else:
        send({"id": req_id, "type": "response", "command": typ, "success": True, "data": {}})
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake pi sdk: %v", err)
	}
	return path
}

func writeShutdownBlockingPiSDKForWSTest(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-node")
	script := `#!/usr/bin/env python3
import json
import os
import sys

marker = os.environ.get("MINDFS_TEST_PROMPT_MARKER", "")

def send(obj):
    print(json.dumps(obj), flush=True)

for line in sys.stdin:
    req = json.loads(line)
    req_id = req.get("id")
    typ = req.get("type")
    if typ == "start_sdk_runtime":
        send({"id": req_id, "type": "response", "command": typ, "success": True, "data": {"sessionId": "ws-shutdown-pi"}})
    elif typ == "get_state":
        send({"id": req_id, "type": "response", "command": typ, "success": True, "data": {"sessionId": "ws-shutdown-pi", "isStreaming": False, "pendingMessageCount": 0}})
    elif typ == "prompt":
        if marker:
            open(marker, "w", encoding="utf-8").close()
        send({"id": req_id, "type": "response", "command": typ, "success": True, "data": {"runtime": "sdk"}})
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
