package pi

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	agenttypes "mindfs/server/internal/agent/types"
)

func TestMergeEnvUsesRuntimeWorkingDirectory(t *testing.T) {
	parentPWD := t.TempDir()
	runtimePWD := t.TempDir()
	t.Setenv("PWD", parentPWD)

	values := make(map[string]string)
	for _, entry := range mergeEnv(nil, runtimePWD) {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = value
		}
	}
	if got := values["PWD"]; got != runtimePWD {
		t.Fatalf("PWD = %q, want runtime root %q", got, runtimePWD)
	}
}

func TestSanitizeDiagnosticLineRedactsSecretsAndBoundsOutput(t *testing.T) {
	bearerSecret := strings.Repeat("a", 64)
	apiSecret := strings.Repeat("b", 40)
	raw := "Authorization: Bearer " + bearerSecret + " api_key=" + apiSecret + " " + strings.Repeat("x", 1000)

	got := sanitizeDiagnosticLine(raw)
	if strings.Contains(got, bearerSecret) || strings.Contains(got, apiSecret) {
		t.Fatalf("diagnostic line leaked a secret: %q", got)
	}
	if !strings.Contains(got, "[REDACTED:token]") || !strings.Contains(got, "[REDACTED:secret]") {
		t.Fatalf("diagnostic line missing redaction markers: %q", got)
	}
	if len(got) > 163 {
		t.Fatalf("diagnostic line length = %d, want at most 163 bytes", len(got))
	}
}

func TestPreviewTruncatesUTF8Safely(t *testing.T) {
	got := preview(strings.Repeat("界", 60))
	if !utf8.ValidString(got) {
		t.Fatalf("preview is not valid UTF-8: %q", got)
	}
	if !strings.HasSuffix(got, "…") {
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

func writeFakePiRPC(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-pi")
	script := `#!/usr/bin/env python3
import json
import os
import sys
import threading

fail_once_file = os.environ.get("FAKE_PI_FAIL_ONCE_FILE")
if fail_once_file and not os.path.exists(fail_once_file):
    open(fail_once_file, "w").close()
    sys.exit(141)
abort_hangs = os.environ.get("FAKE_PI_ABORT_HANG") == "1"

out_lock = threading.Lock()
pending_prompt_id = None
pending_ui_method = None

def send(obj):
    with out_lock:
        print(json.dumps(obj, ensure_ascii=False), flush=True)

def finish_dialog_prompt(req_id, message):
    send({"id": req_id, "type": "response", "command": "prompt", "success": True})
    send({"type": "agent_start"})
    send({"type": "message_start"})
    send({"type": "message_update", "assistantMessageEvent": {"type": "text_delta", "delta": message}})
    send({"type": "message_end", "message": {"role": "assistant", "content": [{"type": "text", "text": message}]}})
    send({"type": "agent_end", "willRetry": False})

def finish_prompt(req_id, message):
    send({"id": req_id, "type": "response", "command": "prompt", "success": True})
    send({"type": "agent_start"})
    send({"type": "message_start"})
    if "tool" in message:
        send({"type": "tool_execution_start", "toolCallId": "tool-1", "toolName": "ls", "args": {"path": "."}})
        send({"type": "tool_execution_end", "toolCallId": "tool-1", "toolName": "ls", "result": {"content": [{"type": "text", "text": "AGENTS.md"}]}, "isError": False})
    if "retry-error" in message:
        send({"type": "message_end", "message": {"role": "assistant", "errorMessage": "fake upstream error", "content": []}})
        send({"type": "agent_end", "willRetry": False})
        return
    if "slow" in message:
        return
    send({"type": "message_update", "assistantMessageEvent": {"type": "text_delta", "delta": "收到"}})
    send({"type": "message_end", "message": {"role": "assistant", "content": [{"type": "text", "text": "收到"}]}})
    send({"type": "agent_end", "willRetry": False})

for line in sys.stdin:
    try:
        req = json.loads(line)
    except Exception as exc:
        send({"type": "response", "success": False, "error": str(exc)})
        continue
    req_id = req.get("id")
    typ = req.get("type")
    if typ == "get_state":
        send({"id": req_id, "type": "response", "command": "get_state", "success": True, "data": {"sessionId": "fake-session", "thinkingLevel": "off", "model": {"provider": "fake", "id": "model"}}})
    elif typ == "get_available_models":
        send({"id": req_id, "type": "response", "command": "get_available_models", "success": True, "data": {"models": [{"id": "model", "name": "Fake Model", "provider": "fake", "reasoning": True, "thinkingLevelMap": {"off": "off", "high": "high"}}]}})
    elif typ == "get_commands":
        send({"id": req_id, "type": "response", "command": "get_commands", "success": True, "data": {"commands": [{"name": "jira", "description": "Jira", "source": "skill"}, {"name": "skill:jira", "description": "Jira skill", "source": "skill"}, {"name": "mcp", "description": "MCP", "source": "extension"}]}})
    elif typ == "set_model":
        if req.get("provider") == "bad-provider":
            send({"id": req_id, "type": "response", "command": "set_model", "success": False, "error": "unknown model"})
        else:
            send({"id": req_id, "type": "response", "command": "set_model", "success": True, "data": {"provider": req.get("provider"), "id": req.get("modelId")}})
    elif typ == "set_thinking_level":
        send({"id": req_id, "type": "response", "command": "set_thinking_level", "success": True, "data": {"level": req.get("level")}})
    elif typ == "prompt":
        message = req.get("message", "")
        if message.strip() == "/mcp":
            # Some Pi extension commands only emit UI notifications and do not
            # produce a prompt response or agent_end. MindFS must still surface
            # visible feedback and finish the turn.
            send({"type": "extension_ui_request", "id": "notify-1", "method": "notify", "message": "MCP: fake connected", "notifyType": "info"})
            continue
        if message.strip() == "/silent-extension":
            # Worst-case extension command: accepted by Pi's command layer, but
            # no prompt response, no notify and no agent turn ever reaches RPC.
            # MindFS must not hang forever.
            continue
        if "select-dialog" in message:
            pending_prompt_id = req_id
            pending_ui_method = "select"
            send({"type": "extension_ui_request", "id": "select-1", "method": "select", "title": "Pick one", "options": ["Allow", "Block"], "timeout": 10000})
            continue
        if "confirm-dialog" in message:
            pending_prompt_id = req_id
            pending_ui_method = "confirm"
            send({"type": "extension_ui_request", "id": "confirm-1", "method": "confirm", "title": "Confirm?", "message": "Proceed?"})
            continue
        if "fire-ui" in message:
            send({"type": "extension_ui_request", "id": "notify-2", "method": "notify", "message": "Fire info", "notifyType": "info"})
            send({"type": "extension_ui_request", "id": "status-1", "method": "setStatus", "statusKey": "fake", "statusText": "running"})
            send({"type": "extension_ui_request", "id": "widget-1", "method": "setWidget", "widgetKey": "fake", "widgetLines": ["one", "two"], "widgetPlacement": "aboveEditor"})
            send({"type": "extension_ui_request", "id": "title-1", "method": "setTitle", "title": "fake title"})
            send({"type": "extension_ui_request", "id": "editor-text-1", "method": "set_editor_text", "text": "prefill"})
            finish_dialog_prompt(req_id, "fire done")
            continue
        finish_prompt(req_id, message)
    elif typ == "extension_ui_response":
        if pending_prompt_id:
            if req.get("cancelled"):
                message = "cancelled: " + str(pending_ui_method)
            elif pending_ui_method == "confirm":
                message = "confirmed: " + str(req.get("confirmed"))
            else:
                message = "selected: " + str(req.get("value", ""))
            prompt_id = pending_prompt_id
            pending_prompt_id = None
            pending_ui_method = None
            finish_dialog_prompt(prompt_id, message)
        else:
            send({"type": "response", "success": False, "error": "no pending extension ui"})
    elif typ == "abort":
        if abort_hangs:
            continue
        send({"id": req_id, "type": "response", "command": "abort", "success": True})
        send({"type": "agent_end", "willRetry": False})
    elif typ == "get_session_stats":
        send({"id": req_id, "type": "response", "command": "get_session_stats", "success": True, "data": {"contextUsage": {"tokens": 7, "contextWindow": 100}}})
    else:
        send({"id": req_id, "type": "response", "command": typ, "success": False, "error": "unsupported " + str(typ)})
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func openFakeSession(t *testing.T, r *Runtime, command string, key string) *session {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sess, err := r.OpenSession(ctx, OpenOptions{
		AgentName:  "pi",
		SessionKey: key,
		Command:    command,
		Args:       []string{"--mode", "rpc", "--no-session"},
		RootPath:   t.TempDir(),
		Model:      "fake/model",
		Mode:       "off",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	piSess, ok := sess.(*session)
	if !ok {
		t.Fatalf("expected *session, got %T", sess)
	}
	return piSess
}

func collectEvents(s *session) (*[]agenttypes.Event, *sync.Mutex) {
	events := make([]agenttypes.Event, 0, 8)
	var mu sync.Mutex
	s.OnUpdate(func(ev agenttypes.Event) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	})
	return &events, &mu
}

func hasEvent(events []agenttypes.Event, typ agenttypes.EventType) bool {
	for _, ev := range events {
		if ev.Type == typ {
			return true
		}
	}
	return false
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

func waitForEvent(t *testing.T, events *[]agenttypes.Event, mu *sync.Mutex, typ agenttypes.EventType) agenttypes.Event {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		for _, ev := range *events {
			if ev.Type == typ {
				mu.Unlock()
				return ev
			}
		}
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	t.Fatalf("event %s not observed; got %+v", typ, *events)
	return agenttypes.Event{}
}

func copyEvents(events *[]agenttypes.Event, mu *sync.Mutex) []agenttypes.Event {
	mu.Lock()
	defer mu.Unlock()
	return append([]agenttypes.Event(nil), (*events)...)
}

func TestListCommandsModelsAndModes(t *testing.T) {
	cmd := writeFakePiRPC(t)
	r := NewRuntime()
	s := openFakeSession(t, r, cmd, "list")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	commands, err := s.ListCommands(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(commands.Commands) != 3 || commands.Commands[0].Name != "jira" || commands.Commands[1].Name != "skill:jira" {
		t.Fatalf("unexpected commands: %+v", commands.Commands)
	}
	models, err := s.ListModels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if models.CurrentModelID != "fake/model" || len(models.Models) != 1 || models.Models[0].ID != "fake/model" {
		t.Fatalf("unexpected models: %+v", models)
	}
	modes, err := s.ListModes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if modes.CurrentModeID != "off" || len(modes.Modes) < 7 {
		t.Fatalf("unexpected modes: %+v", modes)
	}
	if modes.Modes[len(modes.Modes)-1].ID != "max" {
		t.Fatalf("last mode = %+v, want max", modes.Modes[len(modes.Modes)-1])
	}
	if err := s.SetModel(ctx, "bad-provider/bad-model"); err == nil || !strings.Contains(err.Error(), "unknown model") {
		t.Fatalf("expected model error, got %v", err)
	}
}

func TestSendMessageStreamsToolsAndErrors(t *testing.T) {
	cmd := writeFakePiRPC(t)
	r := NewRuntime()
	s := openFakeSession(t, r, cmd, "message")
	events, mu := collectEvents(s)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := s.SendMessage(ctx, "please use tool"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	got := append([]agenttypes.Event(nil), (*events)...)
	mu.Unlock()
	if !strings.Contains(joinedMessageChunks(got), "收到") {
		t.Fatalf("missing streamed text: %+v", got)
	}
	if !hasEvent(got, agenttypes.EventTypeToolCall) || !hasEvent(got, agenttypes.EventTypeToolUpdate) || !hasEvent(got, agenttypes.EventTypeMessageDone) {
		t.Fatalf("missing tool/done events: %+v", got)
	}

	err := s.SendMessage(ctx, "retry-error")
	if err == nil || !strings.Contains(err.Error(), "fake upstream error") {
		t.Fatalf("expected upstream error, got %v", err)
	}
}

func TestExtensionOnlySlashCommandFinishes(t *testing.T) {
	cmd := writeFakePiRPC(t)
	r := NewRuntime()
	s := openFakeSession(t, r, cmd, "slash")
	events, mu := collectEvents(s)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	if err := s.SendMessage(ctx, "/mcp"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	got := append([]agenttypes.Event(nil), (*events)...)
	mu.Unlock()
	if !strings.Contains(joinedMessageChunks(got), "MCP: fake connected") {
		t.Fatalf("missing notify chunk: %+v", got)
	}
	if !hasEvent(got, agenttypes.EventTypeMessageDone) {
		t.Fatalf("missing done event: %+v", got)
	}
}

func TestExtensionUIDialogRoundTrip(t *testing.T) {
	cmd := writeFakePiRPC(t)
	r := NewRuntime()
	s := openFakeSession(t, r, cmd, "extension-ui-dialog")
	events, mu := collectEvents(s)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- s.SendMessage(ctx, "select-dialog") }()

	ev := waitForEvent(t, events, mu, agenttypes.EventTypeExtensionUI)
	request, ok := ev.Data.(agenttypes.ExtensionUIRequest)
	if !ok {
		t.Fatalf("unexpected extension UI payload: %#v", ev.Data)
	}
	if request.ID != "select-1" || request.Method != "select" {
		t.Fatalf("unexpected extension UI request: %+v", request)
	}
	options, ok := request.Payload["options"].([]any)
	if !ok || len(options) != 2 || options[0] != "Allow" {
		t.Fatalf("unexpected options: %#v", request.Payload["options"])
	}
	if err := s.AnswerExtensionUI(ctx, agenttypes.ExtensionUIResponse{RequestID: request.ID, Method: request.Method, Value: "Allow"}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("SendMessage did not finish after extension UI response")
	}
	got := copyEvents(events, mu)
	if !strings.Contains(joinedMessageChunks(got), "selected: Allow") {
		t.Fatalf("missing selected result: %+v", got)
	}
	if !hasEvent(got, agenttypes.EventTypeMessageDone) {
		t.Fatalf("missing done event: %+v", got)
	}
}

func TestExtensionUIConfirmRoundTrip(t *testing.T) {
	cmd := writeFakePiRPC(t)
	r := NewRuntime()
	s := openFakeSession(t, r, cmd, "extension-ui-confirm")
	events, mu := collectEvents(s)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- s.SendMessage(ctx, "confirm-dialog") }()
	ev := waitForEvent(t, events, mu, agenttypes.EventTypeExtensionUI)
	request, ok := ev.Data.(agenttypes.ExtensionUIRequest)
	if !ok || request.ID != "confirm-1" || request.Method != "confirm" {
		t.Fatalf("unexpected extension UI request: %#v", ev.Data)
	}
	confirmed := true
	if err := s.AnswerExtensionUI(ctx, agenttypes.ExtensionUIResponse{RequestID: request.ID, Method: request.Method, Confirmed: &confirmed}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("SendMessage did not finish after confirm response")
	}
	if got := joinedMessageChunks(copyEvents(events, mu)); !strings.Contains(got, "confirmed: True") {
		t.Fatalf("missing confirm result: %q", got)
	}
}

func TestExtensionUIFireAndForgetRequests(t *testing.T) {
	cmd := writeFakePiRPC(t)
	r := NewRuntime()
	s := openFakeSession(t, r, cmd, "extension-ui-fire")
	events, mu := collectEvents(s)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := s.SendMessage(ctx, "fire-ui"); err != nil {
		t.Fatal(err)
	}
	got := copyEvents(events, mu)
	methods := make(map[string]bool)
	for _, ev := range got {
		if ev.Type != agenttypes.EventTypeExtensionUI {
			continue
		}
		request, ok := ev.Data.(agenttypes.ExtensionUIRequest)
		if !ok {
			t.Fatalf("unexpected extension UI payload: %#v", ev.Data)
		}
		methods[request.Method] = true
	}
	for _, method := range []string{"notify", "setStatus", "setWidget", "setTitle", "set_editor_text"} {
		if !methods[method] {
			t.Fatalf("missing fire-and-forget method %s in %+v", method, got)
		}
	}
	if !strings.Contains(joinedMessageChunks(got), "fire done") {
		t.Fatalf("missing final text: %+v", got)
	}
}

func TestSilentExtensionOnlySlashCommandUsesFallbackAndFinishes(t *testing.T) {
	oldProbe, oldFallback := extensionOnlyProbeInterval, extensionOnlyFallbackPeriod
	extensionOnlyProbeInterval = 20 * time.Millisecond
	extensionOnlyFallbackPeriod = 80 * time.Millisecond
	defer func() {
		extensionOnlyProbeInterval = oldProbe
		extensionOnlyFallbackPeriod = oldFallback
	}()

	cmd := writeFakePiRPC(t)
	r := NewRuntime()
	s := openFakeSession(t, r, cmd, "silent-slash")
	events, mu := collectEvents(s)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := s.SendMessage(ctx, "/silent-extension"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	got := append([]agenttypes.Event(nil), (*events)...)
	mu.Unlock()
	if !strings.Contains(joinedMessageChunks(got), "Command handled: /silent-extension") {
		t.Fatalf("missing fallback chunk: %+v", got)
	}
	if !hasEvent(got, agenttypes.EventTypeMessageDone) {
		t.Fatalf("missing done event: %+v", got)
	}
}

func TestCancelCurrentTurnUnblocksSlowPrompt(t *testing.T) {
	cmd := writeFakePiRPC(t)
	r := NewRuntime()
	s := openFakeSession(t, r, cmd, "cancel")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- s.SendMessage(ctx, "slow") }()
	time.Sleep(250 * time.Millisecond)
	if err := s.CancelCurrentTurn(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, io.EOF) {
			t.Fatalf("unexpected cancel error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("SendMessage did not unblock after cancel")
	}
}

func TestCancelCurrentTurnDoesNotWaitForMissingAbortResponse(t *testing.T) {
	cmd := writeFakePiRPC(t)
	r := NewRuntime()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sess, err := r.OpenSession(ctx, OpenOptions{
		AgentName:  "pi",
		SessionKey: "cancel-abort-hang",
		Command:    cmd,
		Args:       []string{"--mode", "rpc", "--no-session"},
		RootPath:   t.TempDir(),
		Model:      "fake/model",
		Mode:       "off",
		Env:        map[string]string{"FAKE_PI_ABORT_HANG": "1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	s, ok := sess.(*session)
	if !ok {
		t.Fatalf("expected *session, got %T", sess)
	}

	done := make(chan error, 1)
	go func() { done <- s.SendMessage(ctx, "slow") }()
	time.Sleep(250 * time.Millisecond)
	started := time.Now()
	if err := s.CancelCurrentTurn(); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("CancelCurrentTurn took %s, want it not to wait for abort response", elapsed)
	}
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, io.EOF) {
			t.Fatalf("unexpected cancel error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SendMessage did not unblock after cancel")
	}
}

func TestOpenSessionRetriesClosedStartup(t *testing.T) {
	cmd := writeFakePiRPC(t)
	r := NewRuntime()
	marker := filepath.Join(t.TempDir(), "failed-once")
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	sess, err := r.OpenSession(ctx, OpenOptions{
		AgentName:  "pi",
		SessionKey: "retry-startup",
		Command:    cmd,
		Args:       []string{"--mode", "rpc", "--no-session"},
		RootPath:   t.TempDir(),
		Model:      "fake/model",
		Mode:       "off",
		Env:        map[string]string{"FAKE_PI_FAIL_ONCE_FILE": marker},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("fake startup failure did not run: %v", err)
	}
	if sess.SessionID() != "fake-session" {
		t.Fatalf("session did not recover after startup retry: %q", sess.SessionID())
	}
}

func TestRuntimeCloseAllTerminatesTrackedSessions(t *testing.T) {
	cmd := writeFakePiRPC(t)
	r := NewRuntime()
	s1 := openFakeSession(t, r, cmd, "close-1")
	s2 := openFakeSession(t, r, cmd, "close-2")
	if s1.cmd.Process == nil || s2.cmd.Process == nil {
		t.Fatal("expected child processes")
	}

	r.CloseAll()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		remaining := len(r.sessions)
		r.mu.Unlock()
		if remaining == 0 && s1.Closed() && s2.Closed() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	r.mu.Lock()
	remaining := len(r.sessions)
	r.mu.Unlock()
	t.Fatalf("runtime still tracks %d sessions; closed=(%v,%v)", remaining, s1.Closed(), s2.Closed())
}
