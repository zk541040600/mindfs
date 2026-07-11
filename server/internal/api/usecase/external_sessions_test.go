package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	"mindfs/server/internal/agent"
	agenttypes "mindfs/server/internal/agent/types"
	"mindfs/server/internal/fs"
	"mindfs/server/internal/preferences"
	"mindfs/server/internal/session"
)

func TestExternalSessionDeltaAfterCtxSeqSkipsCopiedPrefix(t *testing.T) {
	exchanges := []agenttypes.ImportedExchange{
		{Role: "user", Content: "u1"},
		{Role: "agent", Content: "a1"},
		{Role: "user", Content: "u2"},
		{Role: "agent", Content: "a2"},
	}
	delta := externalSessionDeltaAfterCtxSeq(exchanges, 2)
	if len(delta) != 2 {
		t.Fatalf("len(delta) = %d, want 2", len(delta))
	}
	if delta[0].Content != "u2" || delta[1].Content != "a2" {
		t.Fatalf("delta = %#v", delta)
	}
}

func TestExternalSessionDeltaAfterCtxSeqReturnsEmptyWhenFullySynced(t *testing.T) {
	exchanges := []agenttypes.ImportedExchange{
		{Role: "user", Content: "u1"},
		{Role: "agent", Content: "a1"},
	}
	if delta := externalSessionDeltaAfterCtxSeq(exchanges, 2); len(delta) != 0 {
		t.Fatalf("delta = %#v, want empty", delta)
	}
}

func TestSyncExternalSessionDeltaFastDoesNotApplyCtxSeqToFilteredImport(t *testing.T) {
	root := fs.NewRootInfo("root", "Root", t.TempDir())
	manager := session.NewManager(root)
	created, err := manager.Create(context.Background(), session.CreateInput{
		Type:  session.TypeChat,
		Agent: "codex",
		Name:  "Imported",
	})
	if err != nil {
		t.Fatal(err)
	}
	lastTimestamp := time.Date(2026, 6, 27, 10, 0, 0, 0, time.UTC)
	if err := manager.AddExchangeForAgentAt(context.Background(), created, "user", "old", "codex", "", "", "", lastTimestamp); err != nil {
		t.Fatal(err)
	}
	current, err := manager.Get(context.Background(), created.Key, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.UpdateAgentState(context.Background(), current, "codex", 10, "external-1"); err != nil {
		t.Fatal(err)
	}
	importer := &syncDeltaTestImporter{
		exchanges: []agenttypes.ImportedExchange{
			{Role: "user", Content: "new user", Timestamp: lastTimestamp.Add(time.Minute)},
			{Role: "agent", Content: "new agent", Timestamp: lastTimestamp.Add(2 * time.Minute)},
		},
	}
	svc := &Service{Registry: &syncDeltaTestRegistry{
		root:     root,
		manager:  manager,
		importer: importer,
	}}
	out, err := svc.SyncExternalSessionDelta(context.Background(), SyncExternalSessionDeltaInput{
		RootID: root.ID,
		Key:    created.Key,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.ImportedCount != 2 {
		t.Fatalf("ImportedCount = %d, want 2", out.ImportedCount)
	}
	if importer.input.AfterTimestamp.IsZero() {
		t.Fatal("AfterTimestamp is zero, want fast sync to pass last timestamp")
	}
	latest, err := manager.Get(context.Background(), created.Key, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(latest.Exchanges) != 3 {
		t.Fatalf("len(exchanges) = %d, want 3", len(latest.Exchanges))
	}
	if got := latest.Exchanges[1].Content; got != "new user" {
		t.Fatalf("exchange[1] = %q, want new user", got)
	}
	if got := latest.Exchanges[2].Content; got != "new agent" {
		t.Fatalf("exchange[2] = %q, want new agent", got)
	}
}

type syncDeltaTestImporter struct {
	input     agenttypes.ImportExternalSessionInput
	exchanges []agenttypes.ImportedExchange
}

func (i *syncDeltaTestImporter) AgentName() string { return "codex" }

func (i *syncDeltaTestImporter) ListExternalSessions(context.Context, agenttypes.ListExternalSessionsInput) (agenttypes.ListExternalSessionsResult, error) {
	return agenttypes.ListExternalSessionsResult{}, nil
}

func (i *syncDeltaTestImporter) ImportExternalSession(_ context.Context, in agenttypes.ImportExternalSessionInput) (agenttypes.ImportedExternalSession, error) {
	i.input = in
	return agenttypes.ImportedExternalSession{
		Agent:          i.AgentName(),
		AgentSessionID: in.AgentSessionID,
		Cwd:            in.RootPath,
		Exchanges:      i.exchanges,
	}, nil
}

type syncDeltaTestRegistry struct {
	root     fs.RootInfo
	manager  *session.Manager
	importer agenttypes.ExternalSessionImporter
}

func (r *syncDeltaTestRegistry) GetRoot(rootID string) (fs.RootInfo, error) {
	if rootID != r.root.ID {
		return fs.RootInfo{}, errors.New("root not found")
	}
	return r.root, nil
}

func (r *syncDeltaTestRegistry) GetSessionManager(string) (*session.Manager, error) {
	return r.manager, nil
}

func (r *syncDeltaTestRegistry) UpsertRoot(string) (fs.RootInfo, error) {
	return fs.RootInfo{}, errors.New("not implemented")
}

func (r *syncDeltaTestRegistry) RemoveRoot(string) (fs.RootInfo, error) {
	return fs.RootInfo{}, errors.New("not implemented")
}

func (r *syncDeltaTestRegistry) RenameRoot(string, string, string) (fs.RootInfo, error) {
	return fs.RootInfo{}, errors.New("not implemented")
}

func (r *syncDeltaTestRegistry) ListRoots() []fs.RootInfo { return nil }

func (r *syncDeltaTestRegistry) GetAgentPool() *agent.Pool { return nil }

func (r *syncDeltaTestRegistry) GetPreferences() *preferences.Store { return nil }

func (r *syncDeltaTestRegistry) GetExternalSessionImporter(string) (agenttypes.ExternalSessionImporter, error) {
	return r.importer, nil
}

func (r *syncDeltaTestRegistry) GetProber() *agent.Prober { return nil }

func (r *syncDeltaTestRegistry) GetCandidateRegistry() *CandidateRegistry { return nil }

func (r *syncDeltaTestRegistry) GetFileWatcher(string, *session.Manager) (*fs.SharedFileWatcher, error) {
	return nil, nil
}

func (r *syncDeltaTestRegistry) ReleaseFileWatcher(string, string) {}
