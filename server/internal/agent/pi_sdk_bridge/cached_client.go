package pisdkbridge

import (
	"context"
	"time"
)

// CachedClient wraps a Client with a session listing cache.
// It implements the same interface used by the Pi importer, so it can be
// substituted transparently. Cache hits return fresh data without invoking
// the Node bridge; cache misses or stale entries trigger a real bridge call.
type CachedClient struct {
	client *Client
	cache  *SessionCache
}

// NewCachedClient wraps client with a cache using the given TTL.
// If ttl <= 0, defaultCacheTTL (60s) is used.
func NewCachedClient(client *Client, ttl time.Duration) *CachedClient {
	return &CachedClient{
		client: client,
		cache:  NewSessionCache(ttl),
	}
}

// ListSessions returns cached session data when fresh, or calls the bridge
// and updates the cache. Fail-closed: if the bridge fails and no cache
// exists, an empty result is returned. If the bridge fails and stale cache
// data exists, the stale data is returned.
func (cc *CachedClient) ListSessions(ctx context.Context, cwd string, limit int) (ListSessionsData, error) {
	if entry := cc.cache.Get(cwd, limit); entry != nil && entry.IsFresh(cc.cache.ttl) {
		return entry.Data, nil
	}
	return cc.RefreshSessions(ctx, cwd, limit)
}

// RefreshSessions bypasses fresh cache and invokes the bridge. On failure it
// still returns stale safe data when available, and otherwise fails closed with
// an empty result.
func (cc *CachedClient) RefreshSessions(ctx context.Context, cwd string, limit int) (ListSessionsData, error) {
	start := time.Now()
	data, err := cc.client.ListSessions(ctx, cwd, limit)
	latency := time.Since(start)

	if err == nil {
		cc.cache.Set(cwd, limit, data, latency)
		return data, nil
	}

	// Bridge failed. Try to return stale data if available.
	if cc.cache.SetStale(cwd, limit, err.Error(), latency) {
		if entry := cc.cache.Get(cwd, limit); entry != nil && !entry.CachedAt.IsZero() {
			// Return stale safe data; the error is recorded in status.
			return entry.Data, nil
		}
	}

	cc.cache.SetFailed(cwd, limit, err.Error(), latency)
	// Fail closed: return empty result, not the error.
	return ListSessionsData{}, nil
}

// ImportSession delegates explicit transcript import to the underlying client.
// Import content is never cached.
func (cc *CachedClient) ImportSession(ctx context.Context, opts ImportSessionOptions) (ImportSessionData, error) {
	return cc.client.ImportSession(ctx, opts)
}

// Cache returns the underlying SessionCache for status inspection.
func (cc *CachedClient) Cache() *SessionCache {
	return cc.cache
}

// Client returns the underlying bridge Client.
func (cc *CachedClient) Client() *Client {
	return cc.client
}

// BridgeStatus returns a snapshot of the SDK bridge cache health. Satisfies
// the pi.BridgeCacher interface.
func (cc *CachedClient) BridgeStatus() BridgeStatus {
	return cc.cache.Status()
}
