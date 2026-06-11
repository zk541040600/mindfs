package pisdkruntime

import (
	"context"
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sess, err := NewRuntime().OpenSession(ctx, OpenOptions{
		AgentName:    "pi",
		SessionKey:   "sdk-test",
		RootPath:     repoRoot(t),
		Command:      "pi",
		Model:        "sdk-model",
		Mode:         "sdk-mode",
		TestScenario: scenario,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess
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

func TestRuntimePromptFailureReturnsError(t *testing.T) {
	sess := openTestSession(t, "prompt-stream")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := sess.SendMessage(ctx, "please fail"); err == nil || !strings.Contains(err.Error(), "E_TEST_PROMPT") {
		t.Fatalf("expected deterministic prompt failure, got %v", err)
	}
}

func TestRuntimeUnsupportedMethodsReturnErrors(t *testing.T) {
	sess := openTestSession(t, "extension-ui")
	ctx := context.Background()
	if err := sess.SetModel(ctx, "provider/model"); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("expected SetModel unsupported error, got %v", err)
	}
	if _, err := sess.ListCommands(ctx); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("expected ListCommands unsupported error, got %v", err)
	}
	if err := sess.CancelCurrentTurn(); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("expected CancelCurrentTurn unsupported error, got %v", err)
	}
}
