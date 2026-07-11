package codex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agenttypes "mindfs/server/internal/agent/types"
)

func TestImportExternalSessionFallbackRejectsDifferentCwd(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	rootDir := filepath.Join(t.TempDir(), "root")
	otherDir := filepath.Join(t.TempDir(), "other")
	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		t.Fatalf("MkdirAll root returned error: %v", err)
	}
	if err := os.MkdirAll(otherDir, 0o755); err != nil {
		t.Fatalf("MkdirAll other returned error: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(codexHome, "sessions"), 0o755); err != nil {
		t.Fatalf("MkdirAll sessions returned error: %v", err)
	}
	sessionPath := filepath.Join(codexHome, "sessions", "session.jsonl")
	body := strings.Join([]string{
		`{"type":"session_meta","payload":{"id":"target-session"},"timestamp":"2026-01-01T00:00:00Z"}`,
		`{"type":"turn_context","payload":{"cwd":` + quoteJSON(otherDir) + `},"timestamp":"2026-01-01T00:00:01Z"}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]},"timestamp":"2026-01-01T00:00:02Z"}`,
		`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]},"timestamp":"2026-01-01T00:00:03Z"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(sessionPath, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile session returned error: %v", err)
	}

	importer := NewImporter(ImporterOptions{AgentName: "codex"})
	_, err := importer.ImportExternalSession(context.Background(), ImportInput(rootDir, "target-session"))
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("ImportExternalSession error = %v, want not found for different cwd", err)
	}
}

func ImportInput(rootPath, sessionID string) agenttypes.ImportExternalSessionInput {
	return agenttypes.ImportExternalSessionInput{RootPath: rootPath, Agent: "codex", AgentSessionID: sessionID}
}

func quoteJSON(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func TestReadCodexImportedExchangeLocatorsAppliesRollbackBeforeTimestampFilter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rollout.jsonl")
	content := `{"timestamp":"2026-06-22T01:00:00Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"u1"}]}}
{"timestamp":"2026-06-22T01:00:01Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"a1"}]}}
{"timestamp":"2026-06-22T01:00:02Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"u2"}]}}
{"timestamp":"2026-06-22T01:00:03Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"a2"}]}}
{"timestamp":"2026-06-22T01:00:04Z","type":"event_msg","payload":{"type":"thread_rolled_back","num_turns":1}}
{"timestamp":"2026-06-22T01:00:05Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"u3"}]}}
{"timestamp":"2026-06-22T01:00:06Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"a3"}]}}
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	all, err := readCodexImportedExchangeLocators(path, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 4 {
		t.Fatalf("len(all) = %d, want 4", len(all))
	}
	if all[0].Content != "u1" || all[1].Content != "a1" || all[2].Content != "u3" || all[3].Content != "a3" {
		t.Fatalf("effective exchanges = %#v", all)
	}
	if all[3].CodexUserCountAfter != 2 {
		t.Fatalf("a3 user count = %d, want 2", all[3].CodexUserCountAfter)
	}

	after, err := time.Parse(time.RFC3339, "2026-06-22T01:00:01Z")
	if err != nil {
		t.Fatal(err)
	}
	delta, err := readCodexImportedExchangeLocators(path, after)
	if err != nil {
		t.Fatal(err)
	}
	if len(delta) != 2 {
		t.Fatalf("len(delta) = %d, want 2", len(delta))
	}
	if delta[0].Content != "u3" || delta[1].Content != "a3" {
		t.Fatalf("delta exchanges = %#v", delta)
	}
}
