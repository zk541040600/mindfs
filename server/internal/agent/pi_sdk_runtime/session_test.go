package pisdkruntime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	agenttypes "mindfs/server/internal/agent/types"
)

func TestPreviewTruncatesUTF8Safely(t *testing.T) {
	got := preview(strings.Repeat("界", 180))
	if !utf8.ValidString(got) {
		t.Fatalf("preview is not valid UTF-8: %q", got)
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("preview missing ellipsis: %q", got)
	}
}

func TestWithDefaultTimeoutAddsCancellableDeadline(t *testing.T) {
	ctx, cancel := withDefaultTimeout(context.Background())
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("withDefaultTimeout did not add a deadline")
	}
	if !deadline.After(time.Now()) || time.Until(deadline) > defaultCommandTimeout {
		t.Fatalf("deadline = %s, want within default timeout", deadline)
	}

	cancel()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("cancel did not close context Done channel")
	}
}

func TestWithDefaultTimeoutPreservesExistingDeadline(t *testing.T) {
	parent, parentCancel := context.WithTimeout(context.Background(), time.Minute)
	defer parentCancel()

	ctx, cancel := withDefaultTimeout(parent)
	cancel()

	select {
	case <-ctx.Done():
		t.Fatal("returned cancel should not cancel a context that already had a deadline")
	case <-time.After(20 * time.Millisecond):
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..", ".."))
}

func openTestSession(t *testing.T, scenario string) agenttypes.Session {
	t.Helper()
	return openTestSessionWithModelMode(t, scenario, "sdk-model", "sdk-mode")
}

func openTestSessionWithModelMode(t *testing.T, scenario, model, mode string) agenttypes.Session {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sess, err := NewRuntime().OpenSession(ctx, OpenOptions{
		AgentName:    "pi",
		SessionKey:   "sdk-test",
		RootPath:     repoRoot(t),
		Command:      "pi",
		Model:        model,
		Mode:         mode,
		TestScenario: scenario,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

func TestStartPayloadForOptionsUsesSDKRuntimeForProbe(t *testing.T) {
	payload := startPayloadForOptions(OpenOptions{
		Model: "provider/model",
		Mode:  "high",
		Probe: true,
	})
	if payload["type"] != "start_sdk_runtime" {
		t.Fatalf("payload type = %v, want start_sdk_runtime", payload["type"])
	}
	if payload["model"] != "provider/model" {
		t.Fatalf("payload model = %v, want provider/model", payload["model"])
	}
	if payload["mode"] != "high" {
		t.Fatalf("payload mode = %v, want high", payload["mode"])
	}
	if _, ok := payload["scenario"]; ok {
		t.Fatalf("probe payload unexpectedly selected test scenario: %+v", payload)
	}
}

func TestStartupTimeoutCoversPiSDKInitialization(t *testing.T) {
	if startupTimeout < 30*time.Second {
		t.Fatalf("startupTimeout = %s, want at least 30s for Pi SDK initialization", startupTimeout)
	}
}

func TestStartPayloadForOptionsPassesResumeSessionID(t *testing.T) {
	payload := startPayloadForOptions(OpenOptions{
		ResumeSessionID: " 019eb637-77d1-7567-ab40-4e22386a40c1 ",
	})
	if payload["sessionId"] != "019eb637-77d1-7567-ab40-4e22386a40c1" {
		t.Fatalf("payload sessionId = %v", payload["sessionId"])
	}
}

func TestStartPayloadForOptionsUsesExplicitTestScenario(t *testing.T) {
	payload := startPayloadForOptions(OpenOptions{
		Model:        "provider/model",
		Probe:        true,
		TestScenario: "prompt-stream",
	})
	if payload["type"] != "start_test_runtime" || payload["scenario"] != "prompt-stream" {
		t.Fatalf("payload = %+v, want explicit prompt-stream test runtime", payload)
	}
}

func TestApplyStartResponseUpdatesSessionState(t *testing.T) {
	data, err := json.Marshal(map[string]any{
		"sessionId":     "019eb637-77d1-7567-ab40-4e22386a40c1",
		"thinkingLevel": "high",
		"model": map[string]any{
			"provider": "fake",
			"id":       "model",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	sess := &session{sessionID: "synthetic-session", model: "old/model", mode: "off"}
	sess.applyStartResponse(bridgeResponse{Data: data})
	if got := sess.SessionID(); got != "019eb637-77d1-7567-ab40-4e22386a40c1" {
		t.Fatalf("SessionID = %q, want real SDK session id", got)
	}
	if got := sess.CurrentModel(); got != "fake/model" {
		t.Fatalf("CurrentModel = %q, want fake/model", got)
	}
	if got := sess.mode; got != "high" {
		t.Fatalf("mode = %q, want high", got)
	}
}

func TestApplyStartResponseAcceptsStringModel(t *testing.T) {
	data, err := json.Marshal(map[string]any{
		"sessionId":     "real-session",
		"thinkingLevel": "medium",
		"model":         "provider/string-model",
	})
	if err != nil {
		t.Fatal(err)
	}
	sess := &session{sessionID: "synthetic-session", model: "old/model", mode: "off"}
	sess.applyStartResponse(bridgeResponse{Data: data})
	if got := sess.SessionID(); got != "real-session" {
		t.Fatalf("SessionID = %q, want real-session", got)
	}
	if got := sess.CurrentModel(); got != "provider/string-model" {
		t.Fatalf("CurrentModel = %q, want provider/string-model", got)
	}
	if got := sess.mode; got != "medium" {
		t.Fatalf("mode = %q, want medium", got)
	}
}

func TestSessionOnUpdateReplaysBacklog(t *testing.T) {
	sess := &session{sessionID: "synthetic-session"}
	sess.emit(agenttypes.Event{Type: agenttypes.EventTypeRecovery, SessionID: sess.SessionID(), Data: agenttypes.RecoveryStatus{Message: "booting"}})
	sess.emit(agenttypes.Event{Type: agenttypes.EventTypeMessageChunk, SessionID: sess.SessionID(), Data: agenttypes.MessageChunk{Content: "boot"}})

	var got []agenttypes.Event
	sess.OnUpdate(func(ev agenttypes.Event) {
		got = append(got, ev)
	})
	if len(got) != 2 {
		t.Fatalf("replayed events = %#v, want 2", got)
	}
	if got[0].Type != agenttypes.EventTypeRecovery || got[1].Type != agenttypes.EventTypeMessageChunk {
		t.Fatalf("replayed event order = %#v", got)
	}
}

func TestSessionBacklogPreservesBlockingExtensionUI(t *testing.T) {
	sess := &session{sessionID: "synthetic-session"}
	sess.emit(agenttypes.Event{Type: agenttypes.EventTypeExtensionUI, SessionID: sess.SessionID(), Data: agenttypes.ExtensionUIRequest{ID: "ui-1", Method: "select"}})
	for i := 0; i < maxBufferedEventsBeforeSubscriber+10; i++ {
		sess.emit(agenttypes.Event{Type: agenttypes.EventTypeRecovery, SessionID: sess.SessionID(), Data: agenttypes.RecoveryStatus{Message: "status"}})
	}

	var got []agenttypes.Event
	sess.OnUpdate(func(ev agenttypes.Event) {
		got = append(got, ev)
	})
	if len(got) != maxBufferedEventsBeforeSubscriber {
		t.Fatalf("replayed events = %d, want capped %d", len(got), maxBufferedEventsBeforeSubscriber)
	}
	for _, ev := range got {
		if isBlockingExtensionUIEvent(ev) {
			return
		}
	}
	t.Fatalf("blocking extension UI was evicted from backlog: %#v", got[:3])
}

func collectSessionEvents(sess agenttypes.Session) (*[]agenttypes.Event, *sync.Mutex) {
	events := make([]agenttypes.Event, 0, 12)
	var mu sync.Mutex
	sess.OnUpdate(func(ev agenttypes.Event) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	})
	return &events, &mu
}

func snapshotEvents(events *[]agenttypes.Event, mu *sync.Mutex) []agenttypes.Event {
	mu.Lock()
	defer mu.Unlock()
	return append([]agenttypes.Event(nil), (*events)...)
}

func extensionMethods(events []agenttypes.Event) map[string]agenttypes.ExtensionUIRequest {
	methods := make(map[string]agenttypes.ExtensionUIRequest)
	for _, ev := range events {
		if ev.Type != agenttypes.EventTypeExtensionUI {
			continue
		}
		req, ok := ev.Data.(agenttypes.ExtensionUIRequest)
		if !ok {
			continue
		}
		methods[req.Method] = req
	}
	return methods
}

func joinedMessageChunks(events []agenttypes.Event) string {
	var b strings.Builder
	for _, ev := range events {
		if ev.Type != agenttypes.EventTypeMessageChunk {
			continue
		}
		if chunk, ok := ev.Data.(agenttypes.MessageChunk); ok {
			b.WriteString(chunk.Content)
		}
	}
	return b.String()
}

func hasEvent(events []agenttypes.Event, typ agenttypes.EventType) bool {
	for _, ev := range events {
		if ev.Type == typ {
			return true
		}
	}
	return false
}

func toolCalls(events []agenttypes.Event, typ agenttypes.EventType) []agenttypes.ToolCall {
	calls := make([]agenttypes.ToolCall, 0)
	for _, ev := range events {
		if ev.Type != typ {
			continue
		}
		call, ok := ev.Data.(agenttypes.ToolCall)
		if ok {
			calls = append(calls, call)
		}
	}
	return calls
}

func todoUpdates(events []agenttypes.Event) []agenttypes.TodoUpdate {
	updates := make([]agenttypes.TodoUpdate, 0)
	for _, ev := range events {
		if ev.Type != agenttypes.EventTypeTodoUpdate {
			continue
		}
		update, ok := ev.Data.(agenttypes.TodoUpdate)
		if ok {
			updates = append(updates, update)
		}
	}
	return updates
}

func waitForToolCallKind(t *testing.T, events *[]agenttypes.Event, mu *sync.Mutex, kind agenttypes.ToolKind) agenttypes.ToolCall {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		calls := toolCalls(snapshotEvents(events, mu), agenttypes.EventTypeToolCall)
		for _, call := range calls {
			if call.Kind == kind {
				return call
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("tool call kind %q not observed: %#v", kind, snapshotEvents(events, mu))
	return agenttypes.ToolCall{}
}

func waitForEvent(t *testing.T, events *[]agenttypes.Event, mu *sync.Mutex, typ agenttypes.EventType) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if hasEvent(snapshotEvents(events, mu), typ) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("event %q not observed: %#v", typ, snapshotEvents(events, mu))
}

func TestRuntimeUIDemoEmitsExtensionUIAndAcceptsResponses(t *testing.T) {
	sess := openTestSession(t, "extension-ui")
	events, mu := collectSessionEvents(sess)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := sess.SendMessage(ctx, "/ui-demo"); err != nil {
		t.Fatal(err)
	}

	methods := extensionMethods(snapshotEvents(events, mu))
	for _, method := range []string{"notify", "setStatus", "setWidget", "setTitle", "set_editor_text", "select", "confirm", "input", "editor"} {
		if _, ok := methods[method]; !ok {
			t.Fatalf("extension UI method %q missing from events: %#v", method, methods)
		}
	}
	selectReq := methods["select"]
	if selectReq.Payload["title"] != "Choose bridge route" {
		t.Fatalf("select payload = %#v", selectReq.Payload)
	}

	confirmed := true
	for _, response := range []agenttypes.ExtensionUIResponse{
		{RequestID: "select-1", Method: "select", Value: "sdk-bridge"},
		{RequestID: "confirm-1", Method: "confirm", Confirmed: &confirmed},
		{RequestID: "input-1", Method: "input", Value: "typed"},
		{RequestID: "editor-1", Method: "editor", Value: "edited"},
	} {
		if err := sess.AnswerExtensionUI(ctx, response); err != nil {
			t.Fatalf("AnswerExtensionUI(%s): %v", response.RequestID, err)
		}
	}
	if err := sess.AnswerExtensionUI(ctx, agenttypes.ExtensionUIResponse{RequestID: "confirm-1", Method: "confirm", Confirmed: &confirmed}); err == nil || !strings.Contains(err.Error(), "not pending") {
		t.Fatalf("expected duplicate response to fail as not pending, got %v", err)
	}
}

func TestRuntimeRealSDKExtensionUIRoundTripCompletesTurn(t *testing.T) {
	root := repoRoot(t)
	agentDir := filepath.Join(t.TempDir(), "agent")
	extensionDir := filepath.Join(agentDir, "extensions")
	if err := os.MkdirAll(extensionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extensionDir, "mindfs-ui-roundtrip.js"), []byte(`
export default function mindfsUIRoundTrip(pi) {
  pi.registerCommand("mindfs-ui-roundtrip", {
    description: "MindFS SDK UI roundtrip test command",
    handler: async (_args, ctx) => {
      const selected = await ctx.ui.select("Pick route", ["left", "right"]);
      ctx.ui.notify("selected=" + selected, "info");
    },
  });
}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sess, err := NewRuntime().OpenSession(ctx, OpenOptions{
		AgentName:  "pi",
		SessionKey: "sdk-real-ui-test",
		RootPath:   root,
		Command:    "pi",
		Probe:      true,
		AgentDir:   agentDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	events, mu := collectSessionEvents(sess)

	done := make(chan error, 1)
	go func() {
		done <- sess.SendMessage(ctx, "/mindfs-ui-roundtrip")
	}()
	waitForEvent(t, events, mu, agenttypes.EventTypeExtensionUI)

	var selectReq agenttypes.ExtensionUIRequest
	for _, ev := range snapshotEvents(events, mu) {
		if ev.Type != agenttypes.EventTypeExtensionUI {
			continue
		}
		req, ok := ev.Data.(agenttypes.ExtensionUIRequest)
		if ok && req.Method == "select" {
			selectReq = req
			break
		}
	}
	if selectReq.ID == "" {
		t.Fatalf("select extension UI request missing: %#v", snapshotEvents(events, mu))
	}
	if err := sess.AnswerExtensionUI(ctx, agenttypes.ExtensionUIResponse{
		RequestID: selectReq.ID,
		Method:    "select",
		Value:     "right",
	}); err != nil {
		t.Fatalf("AnswerExtensionUI: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("SendMessage: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("SendMessage did not complete after extension UI response; events=%#v", snapshotEvents(events, mu))
	}

	gotEvents := snapshotEvents(events, mu)
	if !hasEvent(gotEvents, agenttypes.EventTypeMessageDone) {
		t.Fatalf("message_done event missing after UI roundtrip: %#v", gotEvents)
	}
}

func TestRuntimeCloseCleansPendingRequests(t *testing.T) {
	sess := openTestSession(t, "extension-ui")
	events, mu := collectSessionEvents(sess)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := sess.SendMessage(ctx, "/ui-demo"); err != nil {
		t.Fatal(err)
	}
	if _, ok := extensionMethods(snapshotEvents(events, mu))["select"]; !ok {
		t.Fatalf("select request not emitted before close")
	}
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sess.AnswerExtensionUI(ctx, agenttypes.ExtensionUIResponse{RequestID: "select-1", Method: "select", Value: "sdk-bridge"}); err == nil || !strings.Contains(err.Error(), "not pending") {
		t.Fatalf("expected closed session to clear pending extension UI, got %v", err)
	}
}

func TestRuntimePromptStreamEmitsMessageDoneAndContextWindow(t *testing.T) {
	sess := openTestSession(t, "prompt-stream")
	events, mu := collectSessionEvents(sess)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := sess.SendMessage(ctx, "hello sdk"); err != nil {
		t.Fatal(err)
	}

	gotEvents := snapshotEvents(events, mu)
	if got := joinedMessageChunks(gotEvents); got != "sdk prompt: hello sdk" {
		t.Fatalf("message chunks = %q, want deterministic sdk prompt", got)
	}
	if !hasEvent(gotEvents, agenttypes.EventTypeMessageDone) {
		t.Fatalf("message_done event missing: %#v", gotEvents)
	}
	window, err := sess.ContextWindow(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if window.TotalTokens != 7 || window.ModelContextWindow != 100 {
		t.Fatalf("context window = %+v, want total=7 window=100", window)
	}
}

func TestHandleMessageUpdateFallsBackToAssistantContentSnapshots(t *testing.T) {
	sess := &session{sessionID: "synthetic-session"}
	events, mu := collectSessionEvents(sess)

	sess.handleMessageUpdate([]byte(`{"type":"message_update","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]}}`))
	sess.handleMessageUpdate([]byte(`{"type":"message_update","message":{"role":"assistant","content":[{"type":"text","text":"hello world"}]}}`))
	sess.handleMessageEnd([]byte(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"hello world"}]}}`))

	if got := joinedMessageChunks(snapshotEvents(events, mu)); got != "hello world" {
		t.Fatalf("message chunks = %q, want snapshot delta text without duplication", got)
	}
}

func TestHandleMessageEndCompletesMissingSnapshotTail(t *testing.T) {
	sess := &session{sessionID: "synthetic-session"}
	events, mu := collectSessionEvents(sess)

	sess.handleMessageUpdate([]byte(`{"type":"message_update","message":{"role":"assistant"},"assistantMessageEvent":{"type":"text_delta","delta":"hello"}}`))
	sess.handleMessageEnd([]byte(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"hello world"}]}}`))

	if got := joinedMessageChunks(snapshotEvents(events, mu)); got != "hello world" {
		t.Fatalf("message chunks = %q, want message_end snapshot tail", got)
	}
}

func TestRuntimeMessageEndOnlyDoesNotCompleteTurn(t *testing.T) {
	sess := openTestSession(t, "message-end-only")
	events, mu := collectSessionEvents(sess)

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	err := sess.SendMessage(ctx, "hello sdk")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("SendMessage error = %v, want context deadline because message_end is not terminal", err)
	}

	gotEvents := snapshotEvents(events, mu)
	if got := joinedMessageChunks(gotEvents); got != "sdk prompt: hello sdk" {
		t.Fatalf("message chunks = %q, want deterministic sdk prompt", got)
	}
	if hasEvent(gotEvents, agenttypes.EventTypeMessageDone) {
		t.Fatalf("message_done emitted for message_end-only runtime: %#v", gotEvents)
	}
}

func TestRuntimePromptFailureReturnsError(t *testing.T) {
	sess := openTestSession(t, "prompt-stream")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := sess.SendMessage(ctx, "please fail"); err == nil || !strings.Contains(err.Error(), "E_TEST_PROMPT") {
		t.Fatalf("expected deterministic prompt failure, got %v", err)
	}
}

func TestRuntimeToolEventsMapToMindFSToolCalls(t *testing.T) {
	sess := openTestSession(t, "tool-events")
	events, mu := collectSessionEvents(sess)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := sess.SendMessage(ctx, "list files"); err != nil {
		t.Fatal(err)
	}

	gotEvents := snapshotEvents(events, mu)
	starts := toolCalls(gotEvents, agenttypes.EventTypeToolCall)
	if len(starts) != 1 {
		t.Fatalf("tool_call events = %#v, want 1", starts)
	}
	if starts[0].CallID != "tool-1" || starts[0].RawType != "pi-sdk" || starts[0].Kind != agenttypes.ToolKindRead || starts[0].Status != "running" {
		t.Fatalf("tool start = %+v", starts[0])
	}
	updates := toolCalls(gotEvents, agenttypes.EventTypeToolUpdate)
	if len(updates) != 2 {
		t.Fatalf("tool_update events = %#v, want update and end", updates)
	}
	if updates[0].Status != "running" || len(updates[0].Content) != 1 || updates[0].Content[0].Text != "AGENTS.md" {
		t.Fatalf("tool partial update = %+v", updates[0])
	}
	if updates[1].Status != "complete" || len(updates[1].Content) != 1 || updates[1].Content[0].Text != "README.md" {
		t.Fatalf("tool final update = %+v", updates[1])
	}
}

func TestRuntimeAskUserQuestionAnswerRoundTripAndTodoUpdate(t *testing.T) {
	sess := openTestSession(t, "ask-user-todo")
	events, mu := collectSessionEvents(sess)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- sess.SendMessage(ctx, "ask user and update todos")
	}()

	askCall := waitForToolCallKind(t, events, mu, agenttypes.ToolKindAskUser)
	if askCall.CallID != "ask-1" || askCall.Status != "running" {
		t.Fatalf("ask user tool call = %+v", askCall)
	}
	if _, ok := askCall.Meta["questions"]; !ok {
		t.Fatalf("ask user tool call missing questions meta: %+v", askCall.Meta)
	}
	waitForEvent(t, events, mu, agenttypes.EventTypeTodoUpdate)

	if err := sess.AnswerQuestion(ctx, agenttypes.AskUserAnswer{
		ToolUseID: "ask-1",
		Answers:   map[string]string{"Which bridge route should MindFS use?": "SDK bridge"},
	}); err != nil {
		t.Fatalf("AnswerQuestion: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("SendMessage: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("SendMessage did not finish after AnswerQuestion: %v", ctx.Err())
	}

	gotEvents := snapshotEvents(events, mu)
	updates := todoUpdates(gotEvents)
	if len(updates) < 2 {
		t.Fatalf("todo updates = %#v, want start and completion updates", updates)
	}
	if len(updates[0].Items) != 2 || updates[0].Items[0].Status != "in_progress" {
		t.Fatalf("first todo update = %#v", updates[0])
	}
	askUpdates := toolCalls(gotEvents, agenttypes.EventTypeToolUpdate)
	foundAskComplete := false
	for _, update := range askUpdates {
		if update.CallID == "ask-1" && update.Status == "complete" {
			foundAskComplete = true
			break
		}
	}
	if !foundAskComplete {
		t.Fatalf("ask completion update missing: %#v", askUpdates)
	}
	if got := joinedMessageChunks(gotEvents); !strings.Contains(got, "SDK bridge") {
		t.Fatalf("message chunks = %q, want answered route", got)
	}
}

func TestPiSDKEditInputMapsToDiffContent(t *testing.T) {
	items := inputContentItems("edit", map[string]any{
		"path":  "server/file.go",
		"edits": []any{map[string]any{"oldText": "old", "newText": "new"}},
	})
	if len(items) != 1 {
		t.Fatalf("inputContentItems = %#v, want one diff", items)
	}
	if items[0].Type != "diff" || items[0].Path != "server/file.go" || items[0].OldText == nil || *items[0].OldText != "old" || items[0].NewText != "new" {
		t.Fatalf("edit diff item = %#v", items[0])
	}
}

func TestPiSDKWriteInputMapsToAddContent(t *testing.T) {
	items := inputContentItems("write", map[string]any{"path": "server/new.go", "content": "package main\n"})
	if len(items) != 1 {
		t.Fatalf("inputContentItems = %#v, want one add", items)
	}
	if items[0].Type != "text" || items[0].Path != "server/new.go" || items[0].ChangeKind != "add" || items[0].Text != "package main\n" {
		t.Fatalf("write add item = %#v", items[0])
	}
}

func TestPiSDKEditResultMapsPatchToDiffContent(t *testing.T) {
	patch := "--- server/file.go\n+++ server/file.go\n@@ -1 +1 @@\n-old\n+new\n"
	raw, err := json.Marshal(map[string]any{
		"content": []map[string]any{{"type": "text", "text": "Successfully replaced 1 block(s) in server/file.go."}},
		"details": map[string]any{"patch": patch, "diff": "-1 old\n+1 new", "firstChangedLine": 1},
	})
	if err != nil {
		t.Fatal(err)
	}

	items := resultContentItems(raw)
	if len(items) < 2 {
		t.Fatalf("resultContentItems = %#v, want patch plus text", items)
	}
	if items[0].Type != "text" || items[0].Text != patch || items[0].Path != "server/file.go" {
		t.Fatalf("patch content item = %#v", items[0])
	}
	locations := resultLocations(raw)
	if len(locations) != 1 || locations[0].Path != "server/file.go" {
		t.Fatalf("resultLocations = %#v, want server/file.go", locations)
	}
}

func TestPiSDKWriteFinalResultPreservesAddPreview(t *testing.T) {
	raw, err := json.Marshal(map[string]any{
		"content": []map[string]any{{"type": "text", "text": "Successfully wrote 13 bytes to server/new.go"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	items := toolResultContentItems("write", map[string]any{
		"path":    "server/new.go",
		"content": "package main\n",
	}, raw)
	if len(items) != 2 {
		t.Fatalf("toolResultContentItems = %#v, want add preview plus success text", items)
	}
	if items[0].Type != "text" || items[0].Path != "server/new.go" || items[0].ChangeKind != "add" || items[0].Text != "package main\n" {
		t.Fatalf("add preview item = %#v", items[0])
	}
	if items[1].Text != "Successfully wrote 13 bytes to server/new.go" {
		t.Fatalf("success item = %#v", items[1])
	}
}

func TestPiSDKEditResultMapsDiffDetailWhenPatchMissing(t *testing.T) {
	diff := "+1 new line\n-1 old line"
	raw, err := json.Marshal(map[string]any{
		"content": []map[string]any{{"type": "text", "text": "Successfully replaced 1 block(s) in server/file.go."}},
		"details": map[string]any{"diff": diff, "firstChangedLine": 1},
	})
	if err != nil {
		t.Fatal(err)
	}

	items := resultContentItems(raw)
	if len(items) < 2 {
		t.Fatalf("resultContentItems = %#v, want diff plus text", items)
	}
	if items[0].Type != "text" || items[0].Text != diff {
		t.Fatalf("diff detail item = %#v", items[0])
	}
}

func TestPiSDKThinkingLevelChangedUpdatesMode(t *testing.T) {
	sess := &session{mode: "off"}
	sess.handleThinkingLevelChanged([]byte(`{"type":"thinking_level_changed","level":"high"}`))
	if got := sess.mode; got != "high" {
		t.Fatalf("mode = %q, want high", got)
	}
}

func TestRuntimeWaitsAcrossToolTurnUntilAgentEnd(t *testing.T) {
	sess := openTestSession(t, "tool-multi-turn")
	events, mu := collectSessionEvents(sess)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	started := time.Now()
	if err := sess.SendMessage(ctx, "list files"); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed < messageEndFallbackDelay {
		t.Fatalf("SendMessage returned after %s; want it to wait past delayed next LLM turn until final agent_end", elapsed)
	}

	gotEvents := snapshotEvents(events, mu)
	if got := joinedMessageChunks(gotEvents); got != "final answer after tool: list files" {
		t.Fatalf("message chunks = %q, want final post-tool answer; events=%#v", got, gotEvents)
	}
	if !hasEvent(gotEvents, agenttypes.EventTypeMessageDone) {
		t.Fatalf("message_done event missing: %#v", gotEvents)
	}
}

func TestRuntimeWaitsAcrossTextThenDelayedTool(t *testing.T) {
	sess := openTestSession(t, "text-then-delayed-tool")
	events, mu := collectSessionEvents(sess)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	started := time.Now()
	if err := sess.SendMessage(ctx, "run bash"); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed < messageEndFallbackDelay+400*time.Millisecond {
		t.Fatalf("SendMessage returned after %s; want it to wait across delayed tool execution", elapsed)
	}

	gotEvents := snapshotEvents(events, mu)
	if got := joinedMessageChunks(gotEvents); got != "preparing tool: run bashdelayed tool complete: run bash" {
		t.Fatalf("message chunks = %q, want pre-tool and final text; events=%#v", got, gotEvents)
	}
	starts := toolCalls(gotEvents, agenttypes.EventTypeToolCall)
	if len(starts) != 1 || starts[0].Status != "running" {
		t.Fatalf("tool_call events = %#v, want one running delayed tool", starts)
	}
	updates := toolCalls(gotEvents, agenttypes.EventTypeToolUpdate)
	if len(updates) != 1 || updates[0].Status != "complete" {
		t.Fatalf("tool_update events = %#v, want one complete delayed tool", updates)
	}
	if !hasEvent(gotEvents, agenttypes.EventTypeMessageDone) {
		t.Fatalf("message_done event missing: %#v", gotEvents)
	}
}

func TestRuntimeAgentEndWillRetryDoesNotCompleteTurn(t *testing.T) {
	sess := openTestSession(t, "agent-end-retry")
	events, mu := collectSessionEvents(sess)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	started := time.Now()
	if err := sess.SendMessage(ctx, "after retry"); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed < 75*time.Millisecond {
		t.Fatalf("SendMessage returned after %s; want willRetry=true agent_end to be ignored", elapsed)
	}

	gotEvents := snapshotEvents(events, mu)
	if got := joinedMessageChunks(gotEvents); got != "retry finished: after retry" {
		t.Fatalf("message chunks = %q, want retry output; events=%#v", got, gotEvents)
	}
}

func TestRuntimeWaitsForPromptDoneAfterRawAgentEnd(t *testing.T) {
	sess := openTestSession(t, "prompt-done-after-agent-end")
	events, mu := collectSessionEvents(sess)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	started := time.Now()
	if err := sess.SendMessage(ctx, "compact first"); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed < messageEndFallbackDelay {
		t.Fatalf("SendMessage returned after %s; want raw agent_end promptDone=false to wait for SDK prompt resolution", elapsed)
	}

	gotEvents := snapshotEvents(events, mu)
	if got := joinedMessageChunks(gotEvents); got != "answer before compaction: compact first" {
		t.Fatalf("message chunks = %q, want answer before compaction; events=%#v", got, gotEvents)
	}
	if !hasEvent(gotEvents, agenttypes.EventTypeMessageDone) {
		t.Fatalf("message_done event missing: %#v", gotEvents)
	}
}

func TestRuntimeTurnEndOnlyDoesNotCompleteTurn(t *testing.T) {
	sess := openTestSession(t, "turn-end-only")
	events, mu := collectSessionEvents(sess)

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	err := sess.SendMessage(ctx, "empty answer")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("SendMessage error = %v, want context deadline because turn_end is not terminal", err)
	}
	gotEvents := snapshotEvents(events, mu)
	if hasEvent(gotEvents, agenttypes.EventTypeMessageDone) {
		t.Fatalf("message_done emitted for turn_end-only runtime: %#v", gotEvents)
	}
	if got := joinedMessageChunks(gotEvents); got != "" {
		t.Fatalf("message chunks = %q, want empty", got)
	}
}

func TestRuntimeSlashCommandsListAndExecute(t *testing.T) {
	sess := openTestSession(t, "slash-controls")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	commands, err := sess.ListCommands(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(commands.Commands) != 3 {
		t.Fatalf("commands = %+v, want 3", commands)
	}
	if commands.Commands[0].Name != "jira" || commands.Commands[0].Description != "extension: Jira issue lookup" {
		t.Fatalf("first command = %+v", commands.Commands[0])
	}

	events, mu := collectSessionEvents(sess)
	if err := sess.SendMessage(ctx, "/skill:jira GE-1"); err != nil {
		t.Fatal(err)
	}
	if got := joinedMessageChunks(snapshotEvents(events, mu)); got != "slash command executed: /skill:jira GE-1" {
		t.Fatalf("slash response = %q", got)
	}
}

func TestRuntimeModelModeControls(t *testing.T) {
	sess := openTestSessionWithModelMode(t, "runtime-controls", "", "")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	models, err := sess.ListModels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if models.CurrentModelID != "fake/model" || len(models.Models) != 2 || models.Models[0].ID != "fake/model" {
		t.Fatalf("models = %+v", models)
	}
	if err := sess.SetModel(ctx, "fake/plain"); err != nil {
		t.Fatal(err)
	}
	if got := sess.CurrentModel(); got != "fake/plain" {
		t.Fatalf("CurrentModel = %q, want fake/plain", got)
	}
	if err := sess.SetModel(ctx, "missing/model"); err == nil || !strings.Contains(err.Error(), "model not found") {
		t.Fatalf("expected missing model error, got %v", err)
	}

	modes, err := sess.ListModes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if modes.CurrentModeID != "off" || len(modes.Modes) < 7 {
		t.Fatalf("modes = %+v", modes)
	}
	if modes.Modes[len(modes.Modes)-1].ID != "max" {
		t.Fatalf("last mode = %+v, want max", modes.Modes[len(modes.Modes)-1])
	}
	if err := sess.SetMode(ctx, "high"); err != nil {
		t.Fatal(err)
	}
	modes, err = sess.ListModes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if modes.CurrentModeID != "high" {
		t.Fatalf("CurrentModeID = %q, want high", modes.CurrentModeID)
	}
}

func TestRuntimeCancelCurrentTurnDoesNotWaitForAbortResponse(t *testing.T) {
	sess := openTestSessionWithModelMode(t, "abort-hangs", "", "")
	events, mu := collectSessionEvents(sess)
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	go func() {
		done <- sess.SendMessage(ctx, "wait-for-abort")
	}()
	waitForEvent(t, events, mu, agenttypes.EventTypeMessageChunk)

	started := time.Now()
	if err := sess.CancelCurrentTurn(); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("CancelCurrentTurn took %s, want it not to wait for abort response", elapsed)
	}
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "aborted") {
			t.Fatalf("SendMessage after cancel = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("SendMessage did not unblock after CancelCurrentTurn")
	}
}

func TestRuntimeCancelCurrentTurnUnblocksPrompt(t *testing.T) {
	sess := openTestSessionWithModelMode(t, "runtime-controls", "", "")
	events, mu := collectSessionEvents(sess)
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	go func() {
		done <- sess.SendMessage(ctx, "wait-for-cancel")
	}()
	waitForEvent(t, events, mu, agenttypes.EventTypeMessageChunk)
	if err := sess.CancelCurrentTurn(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "aborted") {
			t.Fatalf("SendMessage after cancel = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("SendMessage did not unblock after CancelCurrentTurn")
	}
}
