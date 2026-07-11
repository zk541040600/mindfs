package codex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
