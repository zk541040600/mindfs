package pi

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	pisdkbridge "mindfs/server/internal/agent/pi_sdk_bridge"
	agenttypes "mindfs/server/internal/agent/types"
)

type ImporterOptions struct {
	AgentName string
	Bridge    BridgeClient
}

type BridgeClient interface {
	ListSessions(ctx context.Context, cwd string, limit int) (pisdkbridge.ListSessionsData, error)
}

type BridgeRefresher interface {
	RefreshSessions(ctx context.Context, cwd string, limit int) (pisdkbridge.ListSessionsData, error)
}

type BridgeImporter interface {
	ImportSession(ctx context.Context, opts pisdkbridge.ImportSessionOptions) (pisdkbridge.ImportSessionData, error)
}

const safeTranscriptMode = "safe_transcript"

// BridgeCacher is an optional interface that BridgeClient implementations may
// satisfy to expose SDK bridge cache/status metadata.
type BridgeCacher interface {
	BridgeStatus() pisdkbridge.BridgeStatus
}

type Importer struct {
	agentName string
	bridge    BridgeClient
}

func NewImporter(opts ImporterOptions) *Importer {
	bridge := opts.Bridge
	if bridge == nil {
		bridge = pisdkbridge.NewCachedClient(
			pisdkbridge.NewClient(pisdkbridge.ClientOptions{}),
			0, // uses default 60s TTL
		)
	}
	return &Importer{agentName: strings.TrimSpace(opts.AgentName), bridge: bridge}
}

// BridgeStatus returns the current SDK bridge cache status if the underlying
// bridge client supports it. Returns false otherwise.
func (i *Importer) BridgeStatus() (pisdkbridge.BridgeStatus, bool) {
	if cacher, ok := i.bridge.(BridgeCacher); ok {
		return cacher.BridgeStatus(), true
	}
	return pisdkbridge.BridgeStatus{}, false
}

func (i *Importer) AgentName() string {
	if strings.TrimSpace(i.agentName) != "" {
		return strings.TrimSpace(i.agentName)
	}
	return "pi"
}

func (i *Importer) ListExternalSessions(ctx context.Context, in agenttypes.ListExternalSessionsInput) (agenttypes.ListExternalSessionsResult, error) {
	limit := in.Limit
	if limit <= 0 {
		limit = 20
	}
	if strings.TrimSpace(in.RootPath) == "" || i.bridge == nil {
		return agenttypes.ListExternalSessionsResult{}, nil
	}
	var data pisdkbridge.ListSessionsData
	var err error
	if in.Refresh {
		if refresher, ok := i.bridge.(BridgeRefresher); ok {
			data, err = refresher.RefreshSessions(ctx, in.RootPath, limit)
		} else {
			data, err = i.bridge.ListSessions(ctx, in.RootPath, limit)
		}
	} else {
		data, err = i.bridge.ListSessions(ctx, in.RootPath, limit)
	}
	if err != nil {
		// External Pi SDK metadata is auxiliary. Discovery must fail closed and
		// never make the interactive runtime or explicit pi-rpc rollback unavailable.
		log.Printf("[agent/pi/importer] sdk bridge list-sessions failed: %v", err)
		return agenttypes.ListExternalSessionsResult{}, nil
	}
	items := make([]agenttypes.ExternalSessionSummary, 0, len(data.Sessions))
	for _, session := range data.Sessions {
		id := strings.TrimSpace(session.ID)
		if id == "" {
			continue
		}
		cwd := strings.TrimSpace(session.Cwd)
		if cwd == "" {
			cwd = strings.TrimSpace(in.RootPath)
		}
		updatedAt := parseSDKBridgeTime(session.Modified)
		if updatedAt.IsZero() {
			updatedAt = parseSDKBridgeTime(session.Created)
		}
		if !in.BeforeTime.IsZero() && !updatedAt.Before(in.BeforeTime) {
			continue
		}
		if !in.AfterTime.IsZero() && !updatedAt.After(in.AfterTime) {
			continue
		}
		items = append(items, agenttypes.ExternalSessionSummary{
			Agent:          i.AgentName(),
			AgentSessionID: id,
			Cwd:            cwd,
			Title:          safeSessionTitle(session.Name),
			UpdatedAt:      updatedAt,
		})
		if len(items) >= limit {
			break
		}
	}
	return agenttypes.ListExternalSessionsResult{Items: items}, nil
}

func (i *Importer) ScanExternalSessions(ctx context.Context, in agenttypes.ListExternalSessionsInput, visit agenttypes.ExternalSessionVisitFunc) error {
	result, err := i.ListExternalSessions(ctx, in)
	if err != nil {
		return err
	}
	for _, item := range result.Items {
		shouldContinue, err := visit(item)
		if err != nil {
			return err
		}
		if !shouldContinue {
			return nil
		}
	}
	return nil
}

func (i *Importer) ImportExternalSession(ctx context.Context, in agenttypes.ImportExternalSessionInput) (agenttypes.ImportedExternalSession, error) {
	agentName := strings.TrimSpace(in.Agent)
	if agentName == "" {
		agentName = i.AgentName()
	}
	sessionID := strings.TrimSpace(in.AgentSessionID)
	if !in.AfterTimestamp.IsZero() {
		// Delta sync remains non-fatal. Full transcript import is only available
		// through explicit safe_transcript mode, not background sync.
		return agenttypes.ImportedExternalSession{Agent: agentName, AgentSessionID: sessionID}, nil
	}
	if strings.TrimSpace(in.Mode) != safeTranscriptMode {
		return agenttypes.ImportedExternalSession{}, errors.New("pi external session transcript import requires mode=safe_transcript")
	}
	if strings.TrimSpace(in.RootPath) == "" || sessionID == "" {
		return agenttypes.ImportedExternalSession{}, errors.New("root path and session id are required")
	}
	importer, ok := i.bridge.(BridgeImporter)
	if !ok {
		return agenttypes.ImportedExternalSession{}, errors.New("pi sdk bridge transcript import is not available")
	}
	data, err := importer.ImportSession(ctx, pisdkbridge.ImportSessionOptions{
		Cwd:         in.RootPath,
		SessionID:   sessionID,
		MaxMessages: 200,
		MaxBytes:    256 * 1024,
	})
	if err != nil {
		return agenttypes.ImportedExternalSession{}, err
	}
	exchanges := make([]agenttypes.ImportedExchange, 0, len(data.Exchanges))
	for _, exchange := range data.Exchanges {
		role := strings.TrimSpace(exchange.Role)
		if role != "user" && role != "agent" {
			continue
		}
		content := strings.TrimSpace(exchange.Content)
		if content == "" {
			continue
		}
		exchanges = append(exchanges, agenttypes.ImportedExchange{
			Role:      role,
			Content:   content,
			Timestamp: parseSDKBridgeTime(exchange.Timestamp),
		})
	}
	if len(exchanges) == 0 {
		return agenttypes.ImportedExternalSession{}, errors.New("pi sdk bridge returned no safe transcript content")
	}
	return agenttypes.ImportedExternalSession{Agent: agentName, AgentSessionID: sessionID, Exchanges: exchanges}, nil
}

func parseSDKBridgeTime(value string) time.Time {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, trimmed); err == nil {
			return t
		}
	}
	return time.Time{}
}

func safeSessionTitle(name string) string {
	trimmed := strings.Join(strings.Fields(strings.TrimSpace(name)), " ")
	if trimmed == "" {
		return ""
	}
	return truncateUTF8ByBytes(trimmed, 120)
}

func truncateUTF8ByBytes(value string, maxBytes int) string {
	if maxBytes <= 0 || value == "" {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	end := 0
	for index := range value {
		if index > maxBytes {
			break
		}
		end = index
	}
	return strings.TrimSpace(value[:end])
}
