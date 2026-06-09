package pisdkbridge

import (
	"strings"
	"sync"
	"time"
)

const defaultCacheTTL = 60 * time.Second

// CacheEntry holds cached list-sessions data and metadata about the bridge call.
type CacheEntry struct {
	Data          ListSessionsData
	CachedAt      time.Time
	Stale         bool // true when this entry was returned after a bridge failure
	LastLatency   time.Duration
	LastError     string
	LastCheckedAt time.Time
}

// Age returns how long since the data was cached.
func (e *CacheEntry) Age() time.Duration {
	if e.CachedAt.IsZero() {
		return 0
	}
	return time.Since(e.CachedAt)
}

// IsFresh returns true if the cache entry is within the given TTL.
// If ttl <= 0, defaultCacheTTL is used as a fallback.
func (e *CacheEntry) IsFresh(ttl time.Duration) bool {
	if ttl <= 0 {
		ttl = defaultCacheTTL
	}
	return !e.CachedAt.IsZero() && time.Since(e.CachedAt) < ttl
}

type cacheKey struct {
	cwd   string
	limit int
}

func makeCacheKey(cwd string, limit int) cacheKey {
	return cacheKey{cwd: strings.TrimSpace(cwd), limit: limit}
}

// SessionCache caches list-sessions results per cwd+limit with a TTL.
type SessionCache struct {
	mu      sync.RWMutex
	entries map[cacheKey]*CacheEntry
	ttl     time.Duration

	// Global status across all keys
	lastBridgeLatency time.Duration
	lastBridgeError   string
	lastBridgeCheckAt time.Time
	bridgeAvailable   bool
}

// NewSessionCache creates a session cache with the given TTL.
// If ttl <= 0, defaultCacheTTL (60s) is used.
func NewSessionCache(ttl time.Duration) *SessionCache {
	if ttl <= 0 {
		ttl = defaultCacheTTL
	}
	return &SessionCache{
		entries: make(map[cacheKey]*CacheEntry),
		ttl:     ttl,
	}
}

// Get returns the cache entry for the given key, or nil if not found.
func (c *SessionCache) Get(cwd string, limit int) *CacheEntry {
	key := makeCacheKey(cwd, limit)
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry := c.entries[key]
	if entry == nil {
		return nil
	}
	// Return a copy to avoid races
	cp := *entry
	return &cp
}

// Set stores a successful bridge result.
func (c *SessionCache) Set(cwd string, limit int, data ListSessionsData, latency time.Duration) {
	key := makeCacheKey(cwd, limit)
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = &CacheEntry{
		Data:          data,
		CachedAt:      now,
		Stale:         false,
		LastLatency:   latency,
		LastCheckedAt: now,
	}
	c.lastBridgeLatency = latency
	c.lastBridgeError = ""
	c.lastBridgeCheckAt = now
	c.bridgeAvailable = true
}

// SetStale marks the existing entry as stale after a bridge failure, so callers
// know the data is from a previous successful call. Returns true if an entry
// was promoted to stale, false if no prior entry exists.
func (c *SessionCache) SetStale(cwd string, limit int, bridgeErr string, latency time.Duration) bool {
	key := makeCacheKey(cwd, limit)
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastBridgeError = truncate(truncate(bridgeErr, 500), maxCapturedStderr)
	c.lastBridgeCheckAt = now
	c.lastBridgeLatency = latency
	c.bridgeAvailable = false

	entry, ok := c.entries[key]
	if !ok {
		return false
	}
	entry.Stale = true
	entry.LastError = truncate(truncate(bridgeErr, 500), maxCapturedStderr)
	entry.LastCheckedAt = now
	entry.LastLatency = latency
	return true
}

// SetFailed records a bridge failure when no prior cache entry exists.
func (c *SessionCache) SetFailed(cwd string, limit int, bridgeErr string, latency time.Duration) {
	key := makeCacheKey(cwd, limit)
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastBridgeError = truncate(truncate(bridgeErr, 500), maxCapturedStderr)
	c.lastBridgeCheckAt = now
	c.lastBridgeLatency = latency
	c.bridgeAvailable = false

	// If no prior entry, record a minimal entry with just error info
	if _, ok := c.entries[key]; !ok {
		c.entries[key] = &CacheEntry{
			CachedAt:      time.Time{}, // zero = never had real data
			Stale:         true,
			LastError:     truncate(truncate(bridgeErr, 500), maxCapturedStderr),
			LastCheckedAt: now,
			LastLatency:   latency,
		}
	}
}

// EntryCount returns the number of cache entries.
func (c *SessionCache) EntryCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}

// Status returns a snapshot of the global bridge status.
func (c *SessionCache) Status() BridgeStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return BridgeStatus{
		Available:     c.bridgeAvailable,
		LastLatency:   c.lastBridgeLatency,
		LastError:     c.lastBridgeError,
		LastCheckedAt: c.lastBridgeCheckAt,
		CacheEntries:  len(c.entries),
		TTL:           c.ttl,
	}
}

// BridgeStatus is a read-only snapshot of SDK bridge health.
type BridgeStatus struct {
	Available     bool          `json:"available"`
	LastLatency   time.Duration `json:"last_latency_ms"`
	LastError     string        `json:"last_error,omitempty"`
	LastCheckedAt time.Time     `json:"last_checked_at"`
	CacheEntries  int           `json:"cache_entries"`
	TTL           time.Duration `json:"ttl_ms"`
}
