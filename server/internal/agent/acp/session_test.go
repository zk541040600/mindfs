package acp

import (
	"context"
	"testing"
	"time"

	types "mindfs/server/internal/agent/types"

	acpsdk "github.com/coder/acp-go-sdk"
)

func TestConvertEventKeepsRawToolOutputVisible(t *testing.T) {
	title := "Read README"
	kind := acpsdk.ToolKindRead
	status := acpsdk.ToolCallStatusCompleted

	event := convertEvent(SessionUpdate{
		SessionID: "session-1",
		Type:      UpdateTypeToolUpdate,
		Raw: acpsdk.SessionUpdate{
			ToolCallUpdate: &acpsdk.SessionToolCallUpdate{
				ToolCallId: acpsdk.ToolCallId("call-1"),
				Title:      &title,
				Kind:       &kind,
				Status:     &status,
				RawInput:   map[string]any{"path": "README.md"},
				RawOutput:  map[string]any{"content": "hello from raw output"},
			},
		},
	})

	toolCall, ok := event.Data.(types.ToolCall)
	if !ok {
		t.Fatalf("Data = %T, want ToolCall", event.Data)
	}
	if toolCall.RawType != "acp" {
		t.Fatalf("RawType = %q, want acp", toolCall.RawType)
	}
	if len(toolCall.Content) != 1 || toolCall.Content[0].Text != "hello from raw output" {
		t.Fatalf("Content = %#v, want raw output text", toolCall.Content)
	}
	if toolCall.Meta == nil || toolCall.Meta["output"] == "" || toolCall.Meta["input"] == "" {
		t.Fatalf("Meta = %#v, want input/output", toolCall.Meta)
	}
}

func TestRequestPermissionWaitsForFrontendSelection(t *testing.T) {
	updates := make(chan SessionUpdate, 4)
	proc := newPermissionTestProcess(func(update SessionUpdate) { updates <- update })
	client := &mindfsClient{proc: proc}
	title := "Write config"
	kind := acpsdk.ToolKindEdit

	respCh := make(chan acpsdk.RequestPermissionResponse, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := client.RequestPermission(context.Background(), acpsdk.RequestPermissionRequest{
			SessionId: acpsdk.SessionId("acp-session"),
			ToolCall: acpsdk.ToolCallUpdate{
				ToolCallId: acpsdk.ToolCallId("perm-1"),
				Title:      &title,
				Kind:       &kind,
				Locations:  []acpsdk.ToolCallLocation{{Path: "config.json"}},
			},
			Options: []acpsdk.PermissionOption{
				{OptionId: "allow", Name: "Allow once", Kind: acpsdk.PermissionOptionKindAllowOnce},
				{OptionId: "reject", Name: "Reject", Kind: acpsdk.PermissionOptionKindRejectOnce},
			},
		})
		if err != nil {
			errCh <- err
			return
		}
		respCh <- resp
	}()

	extensionUI := waitForExtensionUIUpdate(t, updates)
	if extensionUI.ID != "perm-1" || extensionUI.Method != "select" {
		t.Fatalf("extension UI = %#v, want select perm-1", extensionUI)
	}
	options, ok := extensionUI.Payload["options"].([]map[string]any)
	if !ok || len(options) != 2 || options[1]["value"] != "reject" {
		t.Fatalf("options payload = %#v, want reject option value", extensionUI.Payload["options"])
	}
	select {
	case resp := <-respCh:
		t.Fatalf("permission response arrived before frontend selection: %#v", resp)
	case err := <-errCh:
		t.Fatalf("permission request failed before frontend selection: %v", err)
	case <-time.After(30 * time.Millisecond):
	}

	sess := &session{proc: proc, sessionKey: "mindfs-session"}
	if err := sess.AnswerExtensionUI(context.Background(), types.ExtensionUIResponse{RequestID: "perm-1", Method: "select", Value: "reject"}); err != nil {
		t.Fatalf("AnswerExtensionUI: %v", err)
	}
	resp := waitPermissionResponse(t, respCh, errCh)
	if resp.Outcome.Selected == nil || resp.Outcome.Selected.OptionId != "reject" {
		t.Fatalf("Outcome = %#v, want selected reject", resp.Outcome)
	}
}

func TestRequestPermissionCancelsWhenTurnIsCancelled(t *testing.T) {
	updates := make(chan SessionUpdate, 4)
	proc := newPermissionTestProcess(func(update SessionUpdate) { updates <- update })
	client := &mindfsClient{proc: proc}

	respCh := make(chan acpsdk.RequestPermissionResponse, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := client.RequestPermission(context.Background(), acpsdk.RequestPermissionRequest{
			SessionId: acpsdk.SessionId("acp-session"),
			ToolCall:  acpsdk.ToolCallUpdate{ToolCallId: acpsdk.ToolCallId("perm-cancel")},
			Options: []acpsdk.PermissionOption{
				{OptionId: "allow", Name: "Allow once", Kind: acpsdk.PermissionOptionKindAllowOnce},
			},
		})
		if err != nil {
			errCh <- err
			return
		}
		respCh <- resp
	}()

	_ = waitForExtensionUIUpdate(t, updates)
	if err := proc.CancelCurrentTurn("mindfs-session"); err != nil {
		t.Fatalf("CancelCurrentTurn: %v", err)
	}
	resp := waitPermissionResponse(t, respCh, errCh)
	if resp.Outcome.Cancelled == nil {
		t.Fatalf("Outcome = %#v, want cancelled", resp.Outcome)
	}
}

func newPermissionTestProcess(handler func(SessionUpdate)) *Process {
	state := &sessionState{ID: acpsdk.SessionId("acp-session")}
	state.setOnUpdate(handler)
	return &Process{
		agentName:    "acp-test",
		sessions:     map[string]*sessionState{"mindfs-session": state},
		sessionsByID: map[string]*sessionState{"acp-session": state},
	}
}

func waitForExtensionUIUpdate(t *testing.T, updates <-chan SessionUpdate) types.ExtensionUIRequest {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case update := <-updates:
			if update.Type != UpdateTypeExtensionUI {
				continue
			}
			event := convertEvent(update)
			request, ok := event.Data.(types.ExtensionUIRequest)
			if !ok {
				t.Fatalf("extension UI event data = %T, want ExtensionUIRequest", event.Data)
			}
			return request
		case <-deadline:
			t.Fatal("timed out waiting for extension UI update")
		}
	}
}

func waitPermissionResponse(t *testing.T, respCh <-chan acpsdk.RequestPermissionResponse, errCh <-chan error) acpsdk.RequestPermissionResponse {
	t.Helper()
	select {
	case resp := <-respCh:
		return resp
	case err := <-errCh:
		t.Fatalf("RequestPermission failed: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for permission response")
	}
	return acpsdk.RequestPermissionResponse{}
}
