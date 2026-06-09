package pi

import (
	"context"
	"errors"
	"strings"

	agenttypes "mindfs/server/internal/agent/types"
)

type ImporterOptions struct {
	AgentName string
}

type Importer struct {
	agentName string
}

func NewImporter(opts ImporterOptions) *Importer {
	return &Importer{agentName: strings.TrimSpace(opts.AgentName)}
}

func (i *Importer) AgentName() string {
	if strings.TrimSpace(i.agentName) != "" {
		return strings.TrimSpace(i.agentName)
	}
	return "pi"
}

func (i *Importer) ListExternalSessions(context.Context, agenttypes.ListExternalSessionsInput) (agenttypes.ListExternalSessionsResult, error) {
	// MindFS runs pi-rpc with --no-session, so there are no durable Pi sessions
	// to import. Returning an empty list keeps external-session discovery and
	// best-effort sync quiet while the live MindFS session remains authoritative.
	return agenttypes.ListExternalSessionsResult{}, nil
}

func (i *Importer) ImportExternalSession(_ context.Context, in agenttypes.ImportExternalSessionInput) (agenttypes.ImportedExternalSession, error) {
	agentName := strings.TrimSpace(in.Agent)
	if agentName == "" {
		agentName = i.AgentName()
	}
	if !in.AfterTimestamp.IsZero() {
		// Delta sync asks for records after a timestamp. pi-rpc sessions are
		// already recorded through MindFS streaming, and --no-session leaves no
		// external transcript to tail, so there is simply nothing extra to import.
		return agenttypes.ImportedExternalSession{Agent: agentName, AgentSessionID: strings.TrimSpace(in.AgentSessionID)}, nil
	}
	return agenttypes.ImportedExternalSession{}, errors.New("pi-rpc external session import is not supported")
}
