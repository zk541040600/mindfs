package usecase

import (
	"errors"
	"strings"
	"time"

	pisdkbridge "mindfs/server/internal/agent/pi_sdk_bridge"
)

// AgentSDKStatusInput is the input for querying an agent's SDK bridge status.
type AgentSDKStatusInput struct {
	AgentName string
}

// AgentSDKStatusOutput is the read-only SDK bridge status for an agent.
type AgentSDKStatusOutput struct {
	Enabled       bool               `json:"enabled"`
	Agent         string             `json:"agent"`
	Available     bool               `json:"available"`
	LastLatencyMs int64              `json:"last_latency_ms"`
	LastError     string             `json:"last_error,omitempty"`
	LastCheckedAt time.Time          `json:"last_checked_at"`
	CacheEntries  int                `json:"cache_entries"`
	TTLMs         int64              `json:"ttl_ms"`
	Capabilities  []string           `json:"capabilities,omitempty"`
	Cache         []CacheEntryStatus `json:"cache,omitempty"`
}

// CacheEntryStatus is a safe summary of one cache entry.
type CacheEntryStatus struct {
	Key          string    `json:"key"`
	Stale        bool      `json:"stale"`
	SessionCount int       `json:"session_count"`
	CachedAt     time.Time `json:"cached_at"`
	AgeMs        int64     `json:"age_ms"`
}

// AgentSDKStatus returns read-only SDK bridge status for the given agent.
// It is safe to call and does not execute extension commands or trigger bridge
// invocations; it only reads cached state.
func (s *Service) AgentSDKStatus(in AgentSDKStatusInput) (AgentSDKStatusOutput, error) {
	if err := s.ensureRegistry(); err != nil {
		return AgentSDKStatusOutput{}, err
	}
	agentName := strings.TrimSpace(in.AgentName)
	if agentName == "" {
		return AgentSDKStatusOutput{}, errors.New("agent is required")
	}

	importer, err := s.Registry.GetExternalSessionImporter(agentName)
	if err != nil {
		// Agent not configured or no importer — not an error, just disabled.
		return AgentSDKStatusOutput{Enabled: false, Agent: agentName}, nil
	}

	type bridgeStatusProvider interface {
		BridgeStatus() (pisdkbridge.BridgeStatus, bool)
	}
	type legacyBridgeStatusProvider interface {
		BridgeStatus() pisdkbridge.BridgeStatus
	}

	var status pisdkbridge.BridgeStatus
	var ok bool
	if provider, hasStatus := importer.(bridgeStatusProvider); hasStatus {
		status, ok = provider.BridgeStatus()
	} else if provider, hasStatus := importer.(legacyBridgeStatusProvider); hasStatus {
		status = provider.BridgeStatus()
		ok = true
	}
	if !ok {
		return AgentSDKStatusOutput{Enabled: false, Agent: agentName}, nil
	}
	return AgentSDKStatusOutput{
		Enabled:       true,
		Agent:         agentName,
		Available:     status.Available,
		LastLatencyMs: status.LastLatency.Milliseconds(),
		LastError:     status.LastError,
		LastCheckedAt: status.LastCheckedAt,
		CacheEntries:  status.CacheEntries,
		TTLMs:         status.TTL.Milliseconds(),
		Capabilities:  []string{"list-sessions"},
	}, nil
}
