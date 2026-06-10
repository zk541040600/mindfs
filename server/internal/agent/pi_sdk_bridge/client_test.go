package pisdkbridge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, path string, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func TestClientListSessionsDecodesEnvelope(t *testing.T) {
	dir := t.TempDir()
	probe := filepath.Join(dir, "probe.mjs")
	writeFile(t, probe, "// fake probe\n", 0o644)
	node := filepath.Join(dir, "fake-node")
	writeFile(t, node, `#!/bin/sh
cat <<'JSON'
{"type":"response","command":"list-sessions","success":true,"data":{"count":1,"returned":1,"sessionDir":"/tmp/pi-sessions","sessions":[{"path":"/tmp/s.jsonl","id":"sid-1","cwd":"/root/mindfs","name":"safe title","created":"2026-06-09T01:02:03Z","modified":"2026-06-09T04:05:06Z","messageCount":2,"hasFirstMessage":true,"entryCount":3}]}}
JSON
`, 0o755)

	client := NewClient(ClientOptions{NodeCommand: node, ProbePath: probe, Timeout: time.Second})
	got, err := client.ListSessions(context.Background(), "/root/mindfs", 5)
	if err != nil {
		t.Fatal(err)
	}
	if got.Count != 1 || len(got.Sessions) != 1 || got.Sessions[0].ID != "sid-1" || got.Sessions[0].Name != "safe title" {
		t.Fatalf("unexpected sessions: %+v", got)
	}
}

func TestClientImportSessionDecodesSafeTranscript(t *testing.T) {
	dir := t.TempDir()
	probe := filepath.Join(dir, "probe.mjs")
	writeFile(t, probe, "// fake probe\n", 0o644)
	node := filepath.Join(dir, "fake-node")
	writeFile(t, node, `#!/bin/sh
cat <<'JSON'
{"type":"response","command":"import-session","success":true,"data":{"sessionId":"sid-1","title":"safe title","messageCount":2,"importedCount":2,"skippedCount":1,"redactedCount":1,"truncated":false,"totalBytes":11,"exchanges":[{"role":"user","content":"hello","timestamp":"2026-06-09T01:02:03Z"},{"role":"agent","content":"world","timestamp":"2026-06-09T01:02:04Z"}],"warnings":["tool_result_skipped"]}}
JSON
`, 0o755)

	client := NewClient(ClientOptions{NodeCommand: node, ProbePath: probe, Timeout: time.Second})
	got, err := client.ImportSession(context.Background(), ImportSessionOptions{Cwd: "/root/mindfs", SessionID: "sid-1", MaxMessages: 10, MaxBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionID != "sid-1" || got.ImportedCount != 2 || got.RedactedCount != 1 || len(got.Exchanges) != 2 || got.Exchanges[0].Content != "hello" {
		t.Fatalf("unexpected import data: %+v", got)
	}
}

func TestProbePathCandidatesIncludeInstalledShareLayout(t *testing.T) {
	prefix := t.TempDir()
	exe := filepath.Join(prefix, "bin", "mindfs")
	got := probePathCandidates("", exe)
	want := filepath.Join(prefix, "share", "mindfs", "server", "internal", "agent", "pi_sdk_bridge", "probe.mjs")
	for _, candidate := range got {
		if candidate == want {
			return
		}
	}
	t.Fatalf("installed share probe path %q missing from candidates: %#v", want, got)
}

func TestClientFailsClosedOnBridgeErrorInvalidJSONAndTimeout(t *testing.T) {
	dir := t.TempDir()
	probe := filepath.Join(dir, "probe.mjs")
	writeFile(t, probe, "// fake probe\n", 0o644)

	bridgeErr := filepath.Join(dir, "bridge-error")
	writeFile(t, bridgeErr, `#!/bin/sh
echo '{"type":"response","command":"list-sessions","success":false,"error":{"code":"E_PARAM","message":"bad flag"}}'
exit 1
`, 0o755)
	client := NewClient(ClientOptions{NodeCommand: bridgeErr, ProbePath: probe, Timeout: time.Second})
	_, err := client.ListSessions(context.Background(), "/root/mindfs", 5)
	if err == nil || !strings.Contains(err.Error(), "E_PARAM") {
		t.Fatalf("expected structured bridge error, got %v", err)
	}

	invalid := filepath.Join(dir, "invalid-json")
	writeFile(t, invalid, `#!/bin/sh
echo 'not json'
echo 'secret stderr that should be truncated' >&2
`, 0o755)
	client = NewClient(ClientOptions{NodeCommand: invalid, ProbePath: probe, Timeout: time.Second})
	_, err = client.ListSessions(context.Background(), "/root/mindfs", 5)
	if err == nil || !strings.Contains(err.Error(), "invalid JSON") {
		t.Fatalf("expected invalid JSON error, got %v", err)
	}

	slow := filepath.Join(dir, "slow")
	writeFile(t, slow, "#!/bin/sh\nsleep 2\n", 0o755)
	client = NewClient(ClientOptions{NodeCommand: slow, ProbePath: probe, Timeout: 50 * time.Millisecond})
	_, err = client.ListSessions(context.Background(), "/root/mindfs", 5)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout error, got %v", err)
	}
}
