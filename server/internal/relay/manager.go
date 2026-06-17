package relay

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const defaultRelayBaseURL = "http://localhost:7331"

const (
	relayHealthCheckInterval     = time.Minute
	relayHealthCheckTimeout      = 10 * time.Second
	relayHealthFailureThreshold  = 3
	relayHealthReconnectCooldown = 10 * time.Minute
)

type Status struct {
	Bound              bool   `json:"relay_bound"`
	NoRelayer          bool   `json:"no_relayer"`
	Connected          bool   `json:"relay_connected"`
	PendingCode        string `json:"pending_code"`
	NodeName           string `json:"node_name"`
	NodeID             string `json:"node_id"`
	E2EENodeID         string `json:"e2ee_node_id,omitempty"`
	RelayBaseURL       string `json:"relay_base_url"`
	NodeURL            string `json:"node_url"`
	LastError          string `json:"last_error,omitempty"`
	LastConnectedAt    string `json:"last_connected_at,omitempty"`
	LastDisconnectedAt string `json:"last_disconnected_at,omitempty"`
	LastReconnectAt    string `json:"last_reconnect_at,omitempty"`
	ReconnectCount     int64  `json:"reconnect_count"`
	HealthFailures     int    `json:"relay_health_failures,omitempty"`
	E2EERequired       bool   `json:"e2ee_required"`
}

type Manager struct {
	service   *Service
	noRelayer bool
	relayBase string

	mu           sync.Mutex
	ctx          context.Context
	cancel       context.CancelFunc
	started      bool
	polling      bool
	pendingCode  string
	pendingSince time.Time
	nodeName     string
	lastError    string

	relayGeneration      int64
	connected            bool
	lastConnectedAt      time.Time
	lastDisconnectedAt   time.Time
	lastReconnectAt      time.Time
	reconnectCount       int64
	healthFailures       int
	healthMonitorStarted bool
}

func NewManager(localAddr string, noRelayer bool, relayBaseURL string, useTLS bool) (*Manager, error) {
	service, err := NewService(localAddr, useTLS)
	if err != nil {
		return nil, err
	}
	resolvedRelayBase := strings.TrimSpace(os.Getenv("MINDFS_RELAY_BASE_URL"))
	if resolvedRelayBase == "" {
		resolvedRelayBase = strings.TrimSpace(relayBaseURL)
	}
	return &Manager{
		service:   service,
		noRelayer: noRelayer,
		relayBase: strings.TrimSuffix(defaultIfEmpty(resolvedRelayBase, defaultRelayBaseURL), "/"),
		nodeName:  defaultNodeName(),
	}, nil
}

func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.started {
		return nil
	}
	m.started = true
	m.ctx = ctx

	creds, err := m.service.store.Load()
	if err != nil {
		m.started = false
		m.ctx = nil
		return err
	}
	if relayBaseMismatch(m.relayBase, creds.Relay.Endpoint) {
		if clearErr := m.service.store.Clear(); clearErr != nil {
			m.started = false
			m.ctx = nil
			return clearErr
		}
		log.Printf("[relay] configured relay base changed, clearing stored credentials and requiring rebind")
		m.lastError = "relay base changed, rebinding required"
		creds = Credentials{}
	}
	if m.noRelayer {
		return nil
	}
	m.startHealthMonitorLocked(ctx)
	if creds.Relay.DeviceToken != "" && creds.Relay.Endpoint != "" {
		m.startLocked(ctx)
		return nil
	}
	return nil
}

func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.statusLocked()
}

func (m *Manager) NoRelayer() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.noRelayer
}

func (m *Manager) StartBinding() (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.noRelayer {
		return m.statusLocked(), nil
	}
	creds, err := m.service.store.Load()
	if err != nil {
		m.lastError = err.Error()
		return m.statusLocked(), err
	}
	if creds.Relay.DeviceToken != "" && creds.Relay.Endpoint != "" {
		return m.statusLocked(), nil
	}
	if m.ctx == nil {
		return m.statusLocked(), errors.New("relay manager not started")
	}
	m.ensurePendingLocked()
	m.startPollingLocked(m.ctx, m.pendingCode)
	return m.statusLocked(), nil
}

func (m *Manager) statusLocked() Status {
	status := Status{
		NoRelayer:      m.noRelayer,
		PendingCode:    m.pendingCode,
		NodeName:       m.nodeName,
		RelayBaseURL:   m.resolveRelayBaseLocked(),
		LastError:      m.lastError,
		ReconnectCount: m.reconnectCount,
		HealthFailures: m.healthFailures,
	}
	if !m.lastConnectedAt.IsZero() {
		status.LastConnectedAt = m.lastConnectedAt.Format(time.RFC3339Nano)
	}
	if !m.lastDisconnectedAt.IsZero() {
		status.LastDisconnectedAt = m.lastDisconnectedAt.Format(time.RFC3339Nano)
	}
	if !m.lastReconnectAt.IsZero() {
		status.LastReconnectAt = m.lastReconnectAt.Format(time.RFC3339Nano)
	}
	if m.noRelayer {
		status.PendingCode = ""
		return status
	}
	creds, err := m.service.store.Load()
	if err == nil && creds.Relay.DeviceToken != "" && creds.Relay.Endpoint != "" {
		status.Bound = true
		status.Connected = m.connected
		status.NodeID = creds.Relay.NodeID
		if status.RelayBaseURL == "" {
			status.RelayBaseURL = endpointBaseURL(creds.Relay.Endpoint)
		}
		if status.RelayBaseURL != "" && status.NodeID != "" {
			status.NodeURL = strings.TrimSuffix(status.RelayBaseURL, "/") + "/n/" + status.NodeID + "/"
		}
		status.PendingCode = ""
	}
	return status
}

func (m *Manager) startLocked(parent context.Context) {
	m.relayGeneration++
	generation := m.relayGeneration
	runCtx, cancel := context.WithCancel(parent)
	m.ctx = parent
	m.cancel = cancel
	hooks := SessionHooks{
		OnConnected: func() {
			m.markRelayConnected(generation)
		},
		OnDisconnected: func(err error) {
			m.markRelayDisconnected(generation, err)
		},
	}
	go func() {
		if err := m.service.Run(runCtx, hooks); err != nil && runCtx.Err() == nil {
			if isPermanentRelayError(err) {
				m.handlePermanentRelayError(err)
				return
			}
			m.markRelayDisconnected(generation, err)
			log.Printf("[relay] stopped: %v", err)
		}
	}()
}

func (m *Manager) startPollingLocked(parent context.Context, pendingCode string) {
	if strings.TrimSpace(pendingCode) == "" || m.polling {
		return
	}
	m.polling = true
	go m.pollLoop(parent, pendingCode)
}

func (m *Manager) pollLoop(parent context.Context, pendingCode string) {
	defer m.finishPolling(pendingCode)

	delay := time.Duration(0)
	for {
		if delay > 0 {
			select {
			case <-parent.Done():
				return
			case <-time.After(delay):
			}
		} else if parent.Err() != nil {
			return
		}

		result, err := m.service.PollBind(parent, m.resolveRelayBase(), pendingCode)
		if err != nil {
			delay = nextDelay(delay)
			m.mu.Lock()
			m.lastError = err.Error()
			m.mu.Unlock()
			continue
		}

		switch result.Status {
		case "pending":
			delay = result.NextPollAfter
			if delay <= 0 {
				delay = 3 * time.Second
			}
		case "confirmed":
			if err := m.service.store.Save(Credentials{Relay: result.Credentials}); err != nil {
				m.mu.Lock()
				m.lastError = err.Error()
				m.mu.Unlock()
				delay = nextDelay(delay)
				continue
			}
			m.mu.Lock()
			m.pendingCode = ""
			m.lastError = ""
			alreadyStarted := m.cancel != nil
			m.mu.Unlock()
			if alreadyStarted {
				m.restart()
			} else {
				m.mu.Lock()
				if m.ctx != nil {
					m.startLocked(m.ctx)
				}
				m.mu.Unlock()
			}
			return
		case "claimed", "expired", "revoked":
			m.mu.Lock()
			m.lastError = result.Status
			m.pendingCode = ""
			m.mu.Unlock()
			return
		default:
			delay = nextDelay(delay)
		}
	}
}

func (m *Manager) finishPolling(pendingCode string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.polling = false
}

func (m *Manager) restart() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reconnectLocked("bind_confirmed")
}

func (m *Manager) Reconnect() (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.noRelayer {
		return m.statusLocked(), errors.New("relay integration is disabled")
	}
	if m.ctx == nil {
		return m.statusLocked(), errors.New("relay manager not started")
	}
	creds, err := m.service.store.Load()
	if err != nil {
		m.lastError = err.Error()
		return m.statusLocked(), err
	}
	if creds.Relay.DeviceToken == "" || creds.Relay.Endpoint == "" {
		return m.statusLocked(), errors.New("relay is not bound")
	}
	m.reconnectLocked("manual")
	return m.statusLocked(), nil
}

func (m *Manager) reconnectLocked(reason string) {
	now := time.Now().UTC()
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	m.connected = false
	m.lastDisconnectedAt = now
	m.lastReconnectAt = now
	m.reconnectCount++
	m.lastError = ""
	log.Printf("[relay] reconnect requested reason=%s count=%d", reason, m.reconnectCount)
	if m.ctx != nil {
		m.startLocked(m.ctx)
	}
}

func (m *Manager) markRelayConnected(generation int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if generation != m.relayGeneration {
		return
	}
	m.connected = true
	m.healthFailures = 0
	m.lastConnectedAt = time.Now().UTC()
	m.lastError = ""
}

func (m *Manager) markRelayDisconnected(generation int64, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if generation != m.relayGeneration {
		return
	}
	m.connected = false
	m.lastDisconnectedAt = time.Now().UTC()
	if err != nil {
		m.lastError = err.Error()
	}
}

func (m *Manager) handlePermanentRelayError(err error) {
	m.mu.Lock()
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	m.connected = false
	m.lastDisconnectedAt = time.Now().UTC()
	if clearErr := m.service.store.Clear(); clearErr != nil {
		log.Printf("[relay] clear credentials failed after permanent error: %v", clearErr)
	}
	m.lastError = err.Error()
	m.mu.Unlock()

	log.Printf("[relay] credentials invalidated, rebinding required: %v", err)
}

func (m *Manager) startHealthMonitorLocked(parent context.Context) {
	if m.healthMonitorStarted || m.noRelayer {
		return
	}
	m.healthMonitorStarted = true
	go m.healthMonitorLoop(parent)
}

func (m *Manager) healthMonitorLoop(parent context.Context) {
	ticker := time.NewTicker(relayHealthCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-parent.Done():
			return
		case <-ticker.C:
			m.checkRelayPublicHealth(parent)
		}
	}
}

func (m *Manager) checkRelayPublicHealth(parent context.Context) {
	status := m.Status()
	if !status.Bound || strings.TrimSpace(status.NodeURL) == "" {
		m.resetHealthFailures()
		return
	}
	ctx, cancel := context.WithTimeout(parent, relayHealthCheckTimeout)
	err := m.service.CheckPublicHealth(ctx, status.NodeURL)
	cancel()
	if err == nil {
		m.resetHealthFailures()
		return
	}

	m.mu.Lock()
	m.healthFailures++
	failures := m.healthFailures
	cooldownReady := m.lastReconnectAt.IsZero() || time.Since(m.lastReconnectAt) >= relayHealthReconnectCooldown
	m.lastError = err.Error()
	if failures >= relayHealthFailureThreshold && cooldownReady && m.ctx != nil {
		log.Printf("[relay] public_health.failed count=%d threshold=%d cooldown_ready=%t node=%s url=%s err=%v", failures, relayHealthFailureThreshold, cooldownReady, status.NodeID, status.NodeURL, err)
		m.reconnectLocked("public_health_failed")
		m.healthFailures = 0
	} else {
		log.Printf("[relay] public_health.failed count=%d threshold=%d cooldown_ready=%t node=%s url=%s err=%v", failures, relayHealthFailureThreshold, cooldownReady, status.NodeID, status.NodeURL, err)
	}
	m.mu.Unlock()
}

func (m *Manager) resetHealthFailures() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.healthFailures = 0
}

func (m *Manager) ensurePendingLocked() {
	if strings.TrimSpace(m.pendingCode) != "" {
		return
	}
	m.pendingCode = generatePendingCode()
	m.pendingSince = time.Now().UTC()
}

func (m *Manager) resolveRelayBase() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.resolveRelayBaseLocked()
}

func (m *Manager) resolveRelayBaseLocked() string {
	if strings.TrimSpace(m.relayBase) != "" {
		return strings.TrimSuffix(m.relayBase, "/")
	}
	creds, err := m.service.store.Load()
	if err != nil {
		return ""
	}
	return endpointBaseURL(creds.Relay.Endpoint)
}

func relayBaseMismatch(configuredBase, endpoint string) bool {
	configuredBase = strings.TrimSuffix(strings.TrimSpace(configuredBase), "/")
	endpointBase := strings.TrimSuffix(strings.TrimSpace(endpointBaseURL(endpoint)), "/")
	if configuredBase == "" || endpointBase == "" {
		return false
	}
	return configuredBase != endpointBase
}

func defaultIfEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func nextDelay(current time.Duration) time.Duration {
	if current <= 0 {
		return 2 * time.Second
	}
	if current < 10*time.Second {
		current *= 2
	}
	if current > 10*time.Second {
		current = 10 * time.Second
	}
	return current
}

func generatePendingCode() string {
	buf := make([]byte, 18)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return "pc_" + base64.RawURLEncoding.EncodeToString(buf)
}

func defaultNodeName() string {
	name, err := os.Hostname()
	if err != nil {
		return "localhost"
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "localhost"
	}
	return name
}

func endpointBaseURL(endpoint string) string {
	u, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return ""
	}
	switch u.Scheme {
	case "ws":
		u.Scheme = "http"
	case "wss":
		u.Scheme = "https"
	default:
		return ""
	}
	u.Path = ""
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimSuffix(u.String(), "/")
}
