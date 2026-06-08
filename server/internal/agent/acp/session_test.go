package acp

import (
	"testing"

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
