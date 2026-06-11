package pisdkruntime

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	agenttypes "mindfs/server/internal/agent/types"
)

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
		Probe: true,
	})
	if payload["type"] != "start_sdk_runtime" {
		t.Fatalf("payload type = %v, want start_sdk_runtime", payload["type"])
	}
	if payload["model"] != "provider/model" {
		t.Fatalf("payload model = %v, want provider/model", payload["model"])
	}
	if _, ok := payload["scenario"]; ok {
		t.Fatalf("probe payload unexpectedly selected test scenario: %+v", payload)
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

func TestRuntimeMessageEndFallbackCompletesTurn(t *testing.T) {
	sess := openTestSession(t, "message-end-only")
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
	window, err := sess.ContextWindow(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if window.TotalTokens != 7 || window.ModelContextWindow != 100 {
		t.Fatalf("context window = %+v, want total=7 window=100", window)
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
	if modes.CurrentModeID != "off" || len(modes.Modes) == 0 {
		t.Fatalf("modes = %+v", modes)
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
