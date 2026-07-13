package e2ee

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"time"
)

const idleTTL = 24 * time.Hour

type Session struct {
	ID              string
	NodeID          string
	Key             []byte
	ProtocolVersion int
	CreatedAt       time.Time
	LastSeenAt      time.Time
	lastClientWSSeq uint64
	nextServerWSSeq uint64
}

type Manager struct {
	mu                sync.RWMutex
	cfg               Config
	sessions          map[string]*Session
	clientIDs         map[string]string
	usedRequestProofs map[string]time.Time
	usedOpenNonces    map[string]time.Time
}

func NewManager(cfg Config) *Manager {
	return &Manager{
		cfg:               cfg,
		sessions:          map[string]*Session{},
		clientIDs:         map[string]string{},
		usedRequestProofs: map[string]time.Time{},
		usedOpenNonces:    map[string]time.Time{},
	}
}

func (m *Manager) Enabled() bool {
	return m != nil && m.cfg.Enabled && m.cfg.PairingSecret != ""
}

func (m *Manager) NodeID() string {
	if m == nil {
		return ""
	}
	return m.cfg.NodeID
}

func (m *Manager) PairingSecret() string {
	if m == nil {
		return ""
	}
	return m.cfg.PairingSecret
}

func (m *Manager) OpenSessionForClient(clientID string, key DerivedKey) (*Session, error) {
	if !m.Enabled() {
		return nil, errors.New("e2ee disabled")
	}
	if clientID == "" {
		return nil, errors.New("client_id required")
	}
	if key.ProtocolVersion == 0 {
		key.ProtocolVersion = ProtocolVersionV1
	}
	if !IsSupportedProtocolVersion(key.ProtocolVersion) {
		return nil, errors.New("unsupported e2ee protocol version")
	}
	sessionID, err := randomSessionID()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	sess := &Session{
		ID:              sessionID,
		NodeID:          m.cfg.NodeID,
		Key:             append([]byte(nil), key.Transport...),
		ProtocolVersion: key.ProtocolVersion,
		CreatedAt:       now,
		LastSeenAt:      now,
	}
	m.mu.Lock()
	if previousID := m.clientIDs[clientID]; previousID != "" {
		m.removeSessionLocked(previousID)
	}
	m.sessions[sess.ID] = sess
	m.clientIDs[clientID] = sess.ID
	m.mu.Unlock()
	return cloneSession(sess), nil
}

func (m *Manager) SessionForClient(clientID string) (*Session, error) {
	return m.sessionForClient(clientID, true)
}

func (m *Manager) SessionForClientNoTouch(clientID string) (*Session, error) {
	return m.sessionForClient(clientID, false)
}

func (m *Manager) TouchSessionForClient(clientID string) error {
	_, err := m.sessionForClient(clientID, true)
	return err
}

// ConsumeRequestProof records a successfully verified request proof until its validity window closes.
func (m *Manager) ConsumeRequestProof(clientID, proof string, expiresAt time.Time) bool {
	return m.consumeReplayValue(&m.usedRequestProofs, clientID, proof, expiresAt)
}

// ConsumeOpenNonce records a successfully verified handshake nonce for the lifetime of an active session window.
func (m *Manager) ConsumeOpenNonce(clientID, nonce string) bool {
	return m.consumeReplayValue(&m.usedOpenNonces, clientID, nonce, time.Now().UTC().Add(idleTTL))
}

// ConsumeClientWSSequence accepts a new protocol-v2 client frame and renews only the current session.
func (m *Manager) ConsumeClientWSSequence(clientID, sessionID string, sequence uint64) error {
	if m == nil || !m.Enabled() {
		return errors.New("e2ee_required")
	}
	if sequence == 0 {
		return errors.New("e2ee_frame_invalid")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	sess, err := m.sessionForClientLocked(clientID, sessionID)
	if err != nil {
		return err
	}
	if sequence <= sess.lastClientWSSeq {
		return errors.New("e2ee_frame_replayed")
	}
	sess.lastClientWSSeq = sequence
	m.touchSessionLocked(clientID, sess, time.Now().UTC())
	return nil
}

// NextServerWSSequence reserves the next protocol-v2 server frame sequence for the current session.
func (m *Manager) NextServerWSSequence(clientID, sessionID string) (uint64, error) {
	if m == nil || !m.Enabled() {
		return 0, errors.New("e2ee_required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	sess, err := m.sessionForClientLocked(clientID, sessionID)
	if err != nil {
		return 0, err
	}
	if sess.nextServerWSSeq == ^uint64(0) {
		return 0, errors.New("e2ee_frame_exhausted")
	}
	sess.nextServerWSSeq++
	m.touchSessionLocked(clientID, sess, time.Now().UTC())
	return sess.nextServerWSSeq, nil
}

func (m *Manager) sessionForClient(clientID string, touch bool) (*Session, error) {
	if !m.Enabled() {
		return nil, errors.New("e2ee_required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	sess, err := m.sessionForClientLocked(clientID, "")
	if err != nil {
		return nil, err
	}
	if touch {
		m.touchSessionLocked(clientID, sess, time.Now().UTC())
	}
	return cloneSession(sess), nil
}

func (m *Manager) sessionForClientLocked(clientID, expectedSessionID string) (*Session, error) {
	sessionID := m.clientIDs[clientID]
	if sessionID == "" || (expectedSessionID != "" && sessionID != expectedSessionID) {
		return nil, errors.New("e2ee_session_missing")
	}
	sess := m.sessions[sessionID]
	if sess == nil {
		delete(m.clientIDs, clientID)
		return nil, errors.New("e2ee_session_missing")
	}
	if expiredLocked(sess) {
		m.removeSessionLocked(sessionID)
		delete(m.clientIDs, clientID)
		return nil, errors.New("e2ee_session_expired")
	}
	return sess, nil
}

func (m *Manager) touchSessionLocked(clientID string, sess *Session, now time.Time) {
	m.pruneReplayValuesLocked(now)
	sess.LastSeenAt = now
	m.extendOpenNonceExpiryLocked(clientID, now.Add(idleTTL))
}

func (m *Manager) CleanupExpired() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, sess := range m.sessions {
		if expiredLocked(sess) {
			m.removeSessionLocked(id)
			m.clearClientBindingLocked(id)
		}
	}
	m.pruneReplayValuesLocked(time.Now().UTC())
}

func (m *Manager) StartCleanup(stop <-chan struct{}) {
	if m == nil {
		return
	}
	ticker := time.NewTicker(time.Hour)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				m.CleanupExpired()
			case <-stop:
				return
			}
		}
	}()
}

func expiredLocked(sess *Session) bool {
	if sess == nil {
		return true
	}
	return time.Since(sess.LastSeenAt) > idleTTL
}

func (m *Manager) clearClientBindingLocked(sessionID string) {
	for clientID, current := range m.clientIDs {
		if current == sessionID {
			delete(m.clientIDs, clientID)
		}
	}
}

func (m *Manager) removeSessionLocked(sessionID string) {
	sess := m.sessions[sessionID]
	if sess != nil {
		zeroBytes(sess.Key)
		sess.Key = nil
	}
	delete(m.sessions, sessionID)
}

func (m *Manager) consumeReplayValue(entries *map[string]time.Time, clientID, value string, expiresAt time.Time) bool {
	if m == nil || clientID == "" || value == "" {
		return false
	}
	now := time.Now().UTC()
	if !expiresAt.After(now) {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneReplayValuesLocked(now)
	if *entries == nil {
		*entries = map[string]time.Time{}
	}
	key := clientID + "\x00" + value
	if expiry, exists := (*entries)[key]; exists && expiry.After(now) {
		return false
	}
	(*entries)[key] = expiresAt.UTC()
	return true
}

func (m *Manager) pruneReplayValuesLocked(now time.Time) {
	for _, entries := range []map[string]time.Time{m.usedRequestProofs, m.usedOpenNonces} {
		for key, expiresAt := range entries {
			if !expiresAt.After(now) {
				delete(entries, key)
			}
		}
	}
}

func (m *Manager) extendOpenNonceExpiryLocked(clientID string, expiresAt time.Time) {
	prefix := clientID + "\x00"
	for key := range m.usedOpenNonces {
		if strings.HasPrefix(key, prefix) {
			m.usedOpenNonces[key] = expiresAt
		}
	}
}

func zeroBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}

func cloneSession(sess *Session) *Session {
	if sess == nil {
		return nil
	}
	return &Session{
		ID:              sess.ID,
		NodeID:          sess.NodeID,
		Key:             append([]byte(nil), sess.Key...),
		ProtocolVersion: sess.ProtocolVersion,
		CreatedAt:       sess.CreatedAt,
		LastSeenAt:      sess.LastSeenAt,
	}
}

func randomSessionID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
