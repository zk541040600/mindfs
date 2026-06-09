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

	agenttypes "mindfs/server/internal/agent/types"
)

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

out_lock = threading.Lock()

def send(obj):
    with out_lock:
        print(json.dumps(obj, ensure_ascii=False), flush=True)

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
        finish_prompt(req_id, message)
    elif typ == "abort":
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
	if modes.CurrentModeID != "off" || len(modes.Modes) < 6 {
		t.Fatalf("unexpected modes: %+v", modes)
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
		if remaining == 0 && s1.isClosed() && s2.isClosed() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	r.mu.Lock()
	remaining := len(r.sessions)
	r.mu.Unlock()
	t.Fatalf("runtime still tracks %d sessions; closed=(%v,%v)", remaining, s1.isClosed(), s2.isClosed())
}
