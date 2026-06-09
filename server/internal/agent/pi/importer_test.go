package pi

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	pisdkbridge "mindfs/server/internal/agent/pi_sdk_bridge"
	agenttypes "mindfs/server/internal/agent/types"
)

type fakeBridgeClient struct {
	data pisdkbridge.ListSessionsData
	err  error
}

func (f fakeBridgeClient) ListSessions(context.Context, string, int) (pisdkbridge.ListSessionsData, error) {
	return f.data, f.err
}

type fakeRefreshBridge struct {
	listCalls    int
	refreshCalls int
	listData     pisdkbridge.ListSessionsData
	refreshData  pisdkbridge.ListSessionsData
}

type fakeImportBridge struct {
	fakeBridgeClient
	importData pisdkbridge.ImportSessionData
	importErr  error
}

func (f fakeImportBridge) ImportSession(context.Context, pisdkbridge.ImportSessionOptions) (pisdkbridge.ImportSessionData, error) {
	return f.importData, f.importErr
}

func (f *fakeRefreshBridge) ListSessions(context.Context, string, int) (pisdkbridge.ListSessionsData, error) {
	f.listCalls++
	return f.listData, nil
}

func (f *fakeRefreshBridge) RefreshSessions(context.Context, string, int) (pisdkbridge.ListSessionsData, error) {
	f.refreshCalls++
	return f.refreshData, nil
}

func TestImporterListExternalSessionsUsesSafeSDKMetadata(t *testing.T) {
	bridge := fakeBridgeClient{data: pisdkbridge.ListSessionsData{Sessions: []pisdkbridge.SessionSummary{
		{ID: "sid-1", Cwd: "/root/mindfs", Name: "  Safe\nTitle  ", Modified: "2026-06-09T04:05:06Z", Created: "2026-06-09T01:02:03Z", HasFirstMessage: true, EntryCount: 4},
		{ID: "", Cwd: "/root/mindfs", Name: "ignored", Modified: "2026-06-09T04:05:07Z"},
	}}}
	importer := NewImporter(ImporterOptions{AgentName: "pi", Bridge: bridge})
	result, err := importer.ListExternalSessions(context.Background(), agenttypes.ListExternalSessionsInput{RootPath: "/root/mindfs", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("expected 1 item, got %+v", result.Items)
	}
	item := result.Items[0]
	if item.Agent != "pi" || item.AgentSessionID != "sid-1" || item.Cwd != "/root/mindfs" {
		t.Fatalf("unexpected item identity: %+v", item)
	}
	if item.FirstUserText != "" || item.Title != "Safe Title" {
		t.Fatalf("unsafe fields returned: %+v", item)
	}
	if item.UpdatedAt.Format(time.RFC3339) != "2026-06-09T04:05:06Z" {
		t.Fatalf("unexpected updated time: %s", item.UpdatedAt.Format(time.RFC3339))
	}
}

func TestImporterListExternalSessionsRefreshUsesBridgeRefresher(t *testing.T) {
	bridge := &fakeRefreshBridge{
		listData:    pisdkbridge.ListSessionsData{Sessions: []pisdkbridge.SessionSummary{{ID: "cached", Cwd: "/root/mindfs", Name: "cached", Modified: "2026-06-09T04:05:06Z"}}},
		refreshData: pisdkbridge.ListSessionsData{Sessions: []pisdkbridge.SessionSummary{{ID: "fresh", Cwd: "/root/mindfs", Name: "fresh", Modified: "2026-06-09T04:05:07Z"}}},
	}
	importer := NewImporter(ImporterOptions{AgentName: "pi", Bridge: bridge})
	result, err := importer.ListExternalSessions(context.Background(), agenttypes.ListExternalSessionsInput{RootPath: "/root/mindfs", Limit: 10, Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	if bridge.refreshCalls != 1 || bridge.listCalls != 0 {
		t.Fatalf("expected refresh call only, list=%d refresh=%d", bridge.listCalls, bridge.refreshCalls)
	}
	if len(result.Items) != 1 || result.Items[0].AgentSessionID != "fresh" {
		t.Fatalf("expected refreshed item, got %+v", result.Items)
	}
}

func TestImporterListExternalSessionsFailsClosedAndImportUnsupported(t *testing.T) {
	importer := NewImporter(ImporterOptions{AgentName: "pi", Bridge: fakeBridgeClient{err: errors.New("boom")}})
	result, err := importer.ListExternalSessions(context.Background(), agenttypes.ListExternalSessionsInput{RootPath: "/root/mindfs", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 0 {
		t.Fatalf("expected fail-closed empty list, got %+v", result.Items)
	}

	_, err = importer.ImportExternalSession(context.Background(), agenttypes.ImportExternalSessionInput{RootPath: "/root/mindfs", Agent: "pi", AgentSessionID: "sid-1"})
	if err == nil || !strings.Contains(err.Error(), "mode=safe_transcript") {
		t.Fatalf("expected explicit mode error, got %v", err)
	}
	imported, err := importer.ImportExternalSession(context.Background(), agenttypes.ImportExternalSessionInput{RootPath: "/root/mindfs", Agent: "pi", AgentSessionID: "sid-1", AfterTimestamp: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if imported.Agent != "pi" || imported.AgentSessionID != "sid-1" || len(imported.Exchanges) != 0 {
		t.Fatalf("unexpected delta import: %+v", imported)
	}
}

func TestImporterSafeTranscriptImport(t *testing.T) {
	bridge := fakeImportBridge{importData: pisdkbridge.ImportSessionData{Exchanges: []pisdkbridge.ImportExchange{
		{Role: "user", Content: "hello", Timestamp: "2026-06-09T04:05:06Z"},
		{Role: "agent", Content: "world", Timestamp: "2026-06-09T04:05:07Z"},
		{Role: "tool", Content: "ignored"},
	}}}
	importer := NewImporter(ImporterOptions{AgentName: "pi", Bridge: bridge})
	imported, err := importer.ImportExternalSession(context.Background(), agenttypes.ImportExternalSessionInput{RootPath: "/root/mindfs", Agent: "pi", AgentSessionID: "sid-1", Mode: safeTranscriptMode})
	if err != nil {
		t.Fatal(err)
	}
	if imported.Agent != "pi" || imported.AgentSessionID != "sid-1" || len(imported.Exchanges) != 2 {
		t.Fatalf("unexpected import result: %+v", imported)
	}
	if imported.Exchanges[0].Role != "user" || imported.Exchanges[0].Content != "hello" || imported.Exchanges[0].Timestamp.Format(time.RFC3339) != "2026-06-09T04:05:06Z" {
		t.Fatalf("unexpected first exchange: %+v", imported.Exchanges[0])
	}
}

func TestImporterSafeTranscriptImportRejectsNoSafeContent(t *testing.T) {
	bridge := fakeImportBridge{importData: pisdkbridge.ImportSessionData{Exchanges: []pisdkbridge.ImportExchange{{Role: "tool", Content: "ignored"}}}}
	importer := NewImporter(ImporterOptions{AgentName: "pi", Bridge: bridge})
	_, err := importer.ImportExternalSession(context.Background(), agenttypes.ImportExternalSessionInput{RootPath: "/root/mindfs", Agent: "pi", AgentSessionID: "sid-1", Mode: safeTranscriptMode})
	if err == nil || !strings.Contains(err.Error(), "no safe transcript") {
		t.Fatalf("expected no safe transcript error, got %v", err)
	}
}

func TestImporterBridgeStatusWithCacher(t *testing.T) {
	importer := NewImporter(ImporterOptions{AgentName: "pi", Bridge: fakeBridgeClient{}})
	// fakeBridgeClient doesn't implement BridgeCacher
	_, ok := importer.BridgeStatus()
	if ok {
		t.Fatal("fakeBridgeClient should not implement BridgeCacher")
	}
}

func TestImporterBridgeStatusWithCachedClient(t *testing.T) {
	dir := t.TempDir()
	probe := dir + "/probe.mjs"
	node := dir + "/fake-node"
	os.WriteFile(probe, []byte("// fake\n"), 0o644)
	os.WriteFile(node, []byte(`#!/bin/sh
cat <<'JSON'
{"type":"response","command":"list-sessions","success":true,"data":{"count":0,"returned":0,"sessions":[]}}
JSON
`), 0o755)

	client := pisdkbridge.NewClient(pisdkbridge.ClientOptions{NodeCommand: node, ProbePath: probe, Timeout: time.Second})
	cached := pisdkbridge.NewCachedClient(client, 30*time.Second)
	importer := NewImporter(ImporterOptions{AgentName: "pi", Bridge: cached})

	status, ok := importer.BridgeStatus()
	if !ok {
		t.Fatal("CachedClient should implement BridgeCacher")
	}
	if status.TTL != 30*time.Second {
		t.Fatalf("unexpected TTL: %v", status.TTL)
	}
}

func TestNewImporterDefaultUsesCachedClient(t *testing.T) {
	importer := NewImporter(ImporterOptions{AgentName: "pi"})
	// The default bridge should be a CachedClient (implements BridgeCacher)
	_, ok := importer.BridgeStatus()
	if !ok {
		t.Fatal("default importer bridge should implement BridgeCacher (CachedClient)")
	}
}
