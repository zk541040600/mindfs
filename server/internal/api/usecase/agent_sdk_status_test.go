package usecase

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"mindfs/server/internal/agent"
	pisdkbridge "mindfs/server/internal/agent/pi_sdk_bridge"
	agenttypes "mindfs/server/internal/agent/types"
	rootfs "mindfs/server/internal/fs"
	"mindfs/server/internal/preferences"
	"mindfs/server/internal/session"
)

type mockSDKStatusImporter struct {
	agentName string
	status    pisdkbridge.BridgeStatus
}

func (m *mockSDKStatusImporter) AgentName() string { return m.agentName }
func (m *mockSDKStatusImporter) ListExternalSessions(_ context.Context, _ agenttypes.ListExternalSessionsInput) (agenttypes.ListExternalSessionsResult, error) {
	return agenttypes.ListExternalSessionsResult{}, nil
}
func (m *mockSDKStatusImporter) ImportExternalSession(_ context.Context, _ agenttypes.ImportExternalSessionInput) (agenttypes.ImportedExternalSession, error) {
	return agenttypes.ImportedExternalSession{}, errors.New("not supported")
}
func (m *mockSDKStatusImporter) BridgeStatus() pisdkbridge.BridgeStatus {
	return m.status
}

type sdkStatusTestRegistry struct {
	importer agenttypes.ExternalSessionImporter
}

func (r *sdkStatusTestRegistry) GetRoot(string) (rootfs.RootInfo, error) {
	return rootfs.RootInfo{}, nil
}
func (r *sdkStatusTestRegistry) GetSessionManager(string) (*session.Manager, error) {
	return nil, nil
}
func (r *sdkStatusTestRegistry) UpsertRoot(string) (rootfs.RootInfo, error) {
	return rootfs.RootInfo{}, nil
}
func (r *sdkStatusTestRegistry) RemoveRoot(string) (rootfs.RootInfo, error) {
	return rootfs.RootInfo{}, nil
}
func (r *sdkStatusTestRegistry) RenameRoot(string, string, string) (rootfs.RootInfo, error) {
	return rootfs.RootInfo{}, nil
}
func (r *sdkStatusTestRegistry) ListRoots() []rootfs.RootInfo { return nil }
func (r *sdkStatusTestRegistry) GetAgentPool() *agent.Pool    { return nil }
func (r *sdkStatusTestRegistry) GetPreferences() *preferences.Store {
	return nil
}
func (r *sdkStatusTestRegistry) GetExternalSessionImporter(agentName string) (agenttypes.ExternalSessionImporter, error) {
	if r.importer != nil && r.importer.AgentName() == agentName {
		return r.importer, nil
	}
	return nil, errors.New("agent not configured: " + agentName)
}
func (r *sdkStatusTestRegistry) GetProber() *agent.Prober { return nil }
func (r *sdkStatusTestRegistry) GetCandidateRegistry() *CandidateRegistry {
	return nil
}
func (r *sdkStatusTestRegistry) GetFileWatcher(string, *session.Manager) (*rootfs.SharedFileWatcher, error) {
	return nil, nil
}
func (r *sdkStatusTestRegistry) ReleaseFileWatcher(string, string) {}

func TestAgentSDKStatusWithCacher(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	importer := &mockSDKStatusImporter{
		agentName: "pi",
		status: pisdkbridge.BridgeStatus{
			Available:     true,
			LastLatency:   150 * time.Millisecond,
			LastError:     "",
			LastCheckedAt: now,
			CacheEntries:  2,
			TTL:           60 * time.Second,
		},
	}
	registry := &sdkStatusTestRegistry{importer: importer}
	service := Service{Registry: registry}

	out, err := service.AgentSDKStatus(AgentSDKStatusInput{AgentName: "pi"})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Enabled {
		t.Fatal("expected enabled=true")
	}
	if out.Agent != "pi" {
		t.Fatalf("expected agent=pi, got %s", out.Agent)
	}
	if !out.Available {
		t.Fatal("expected available=true")
	}
	if out.LastLatencyMs != 150 {
		t.Fatalf("expected last_latency_ms=150, got %d", out.LastLatencyMs)
	}
	if out.CacheEntries != 2 {
		t.Fatalf("expected cache_entries=2, got %d", out.CacheEntries)
	}
	if out.TTLMs != 60000 {
		t.Fatalf("expected ttl_ms=60000, got %d", out.TTLMs)
	}
	if !out.LastCheckedAt.Equal(now) {
		t.Fatalf("expected last_checked_at=%v, got %v", now, out.LastCheckedAt)
	}
	if len(out.Capabilities) != 1 || out.Capabilities[0] != "list-sessions" {
		t.Fatalf("expected capabilities=[list-sessions], got %v", out.Capabilities)
	}
}

func TestAgentSDKStatusNonCacherImporter(t *testing.T) {
	// An importer that doesn't implement BridgeCacher should report enabled=false
	registry := &sdkStatusTestRegistry{importer: nil}
	service := Service{Registry: registry}

	out, err := service.AgentSDKStatus(AgentSDKStatusInput{AgentName: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Enabled {
		t.Fatal("expected enabled=false for agent without cacher")
	}
	if out.Agent != "claude" {
		t.Fatalf("expected agent=claude, got %s", out.Agent)
	}
}

func TestAgentSDKStatusMissingAgent(t *testing.T) {
	registry := &sdkStatusTestRegistry{importer: nil}
	service := Service{Registry: registry}

	out, err := service.AgentSDKStatus(AgentSDKStatusInput{AgentName: ""})
	if err == nil {
		t.Fatal("expected error for empty agent")
	}
	if out.Enabled {
		t.Fatal("expected enabled=false for empty agent")
	}
}

func TestAgentSDKStatusBridgeUnavailable(t *testing.T) {
	importer := &mockSDKStatusImporter{
		agentName: "pi",
		status: pisdkbridge.BridgeStatus{
			Available:    false,
			LastLatency:  5 * time.Second,
			LastError:    "E_FAIL: bridge timed out",
			CacheEntries: 1,
			TTL:          60 * time.Second,
		},
	}
	registry := &sdkStatusTestRegistry{importer: importer}
	service := Service{Registry: registry}

	out, err := service.AgentSDKStatus(AgentSDKStatusInput{AgentName: "pi"})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Enabled {
		t.Fatal("should still be enabled even when bridge unavailable")
	}
	if out.Available {
		t.Fatal("expected available=false")
	}
	if !strings.Contains(out.LastError, "E_FAIL") {
		t.Fatalf("expected error to contain E_FAIL, got %q", out.LastError)
	}
}
