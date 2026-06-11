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

func openTestSession(t *testing.T) agenttypes.Session {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sess, err := NewRuntime().OpenSession(ctx, OpenOptions{
		AgentName:  "pi",
		SessionKey: "sdk-test",
		RootPath:   repoRoot(t),
		Command:    "pi",
		Model:      "sdk-model",
		Mode:       "sdk-mode",
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

func TestRuntimeUIDemoEmitsExtensionUIAndAcceptsResponses(t *testing.T) {
	sess := openTestSession(t)
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
	sess := openTestSession(t)
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

func TestRuntimeUnsupportedMethodsReturnErrors(t *testing.T) {
	sess := openTestSession(t)
	ctx := context.Background()
	if err := sess.SendMessage(ctx, "hello"); err == nil || !strings.Contains(err.Error(), "only /ui-demo") {
		t.Fatalf("expected unsupported prompt error, got %v", err)
	}
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
