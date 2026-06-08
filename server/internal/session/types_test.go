package session

import (
	"testing"

	agenttypes "mindfs/server/internal/agent/types"
)

func TestCompactExchangeAuxPreservesThoughtOnlyEntry(t *testing.T) {
	aux := ExchangeAux{Seq: 2, Line: 3, Thought: "checking stream events"}

	compacted, ok := CompactExchangeAux(aux)
	if !ok {
		t.Fatalf("thought-only aux was dropped")
	}
	if compacted.Thought != aux.Thought {
		t.Fatalf("Thought = %q, want %q", compacted.Thought, aux.Thought)
	}
	if compacted.ToolCall != nil {
		t.Fatalf("ToolCall = %#v, want nil", compacted.ToolCall)
	}
}

func TestCompactToolCallPreservesACPContent(t *testing.T) {
	toolCall := agenttypes.ToolCall{
		CallID:  "call-1",
		Status:  "complete",
		Kind:    agenttypes.ToolKindOther,
		RawType: "acp",
		Content: []agenttypes.ToolCallContentItem{{Type: "text", Text: "tool result"}},
	}

	compacted := CompactToolCall(toolCall)
	if len(compacted.Content) != 1 || compacted.Content[0].Text != "tool result" {
		t.Fatalf("Content = %#v, want retained ACP content", compacted.Content)
	}
}
