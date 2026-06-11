package pisdkbridge

import (
	"testing"
	"time"
)

func TestSessionCacheSetAndGet(t *testing.T) {
	cache := NewSessionCache(60 * time.Second)
	data := ListSessionsData{
		Count:    2,
		Returned: 2,
		Sessions: []SessionSummary{
			{ID: "s1", Cwd: "/root/mindfs", Name: "session one"},
			{ID: "s2", Cwd: "/root/mindfs", Name: "session two"},
		},
	}
	cache.Set("/root/mindfs", 10, data, 100*time.Millisecond)

	entry := cache.Get("/root/mindfs", 10)
	if entry == nil {
		t.Fatal("expected cache entry")
	}
	if entry.Data.Count != 2 || len(entry.Data.Sessions) != 2 {
		t.Fatalf("unexpected data: %+v", entry.Data)
	}
	if entry.Stale {
		t.Fatal("entry should not be stale")
	}
	if entry.LastLatency != 100*time.Millisecond {
		t.Fatalf("unexpected latency: %v", entry.LastLatency)
	}
}

func TestSessionCacheMiss(t *testing.T) {
	cache := NewSessionCache(60 * time.Second)
	entry := cache.Get("/nonexistent", 5)
	if entry != nil {
		t.Fatal("expected nil for cache miss")
	}
}

func TestSessionCacheTTLExpiry(t *testing.T) {
	cache := NewSessionCache(50 * time.Millisecond)
	data := ListSessionsData{Count: 1, Returned: 1, Sessions: []SessionSummary{{ID: "s1"}}}
	cache.Set("/root/mindfs", 5, data, 10*time.Millisecond)

	entry := cache.Get("/root/mindfs", 5)
	if entry == nil || !entry.IsFresh(50*time.Millisecond) {
		t.Fatal("expected fresh entry")
	}

	time.Sleep(80 * time.Millisecond)
	entry = cache.Get("/root/mindfs", 5)
	if entry == nil {
		t.Fatal("entry should still exist even if stale")
	}
	if entry.IsFresh(50 * time.Millisecond) {
		t.Fatal("entry should be stale after TTL")
	}
}

func TestSessionCacheDifferentKeys(t *testing.T) {
	cache := NewSessionCache(60 * time.Second)
	cache.Set("/root/mindfs", 5, ListSessionsData{Count: 5}, 10*time.Millisecond)
	cache.Set("/home/dev", 10, ListSessionsData{Count: 10}, 20*time.Millisecond)

	entry1 := cache.Get("/root/mindfs", 5)
	entry2 := cache.Get("/home/dev", 10)
	if entry1 == nil || entry1.Data.Count != 5 {
		t.Fatalf("expected count=5, got %+v", entry1)
	}
	if entry2 == nil || entry2.Data.Count != 10 {
		t.Fatalf("expected count=10, got %+v", entry2)
	}

	// Same cwd, different limit = different key
	entry3 := cache.Get("/root/mindfs", 10)
	if entry3 != nil {
		t.Fatal("different limit should be a cache miss")
	}
}

func TestSessionCacheSetStaleWithExistingEntry(t *testing.T) {
	cache := NewSessionCache(60 * time.Second)
	data := ListSessionsData{Count: 1, Returned: 1, Sessions: []SessionSummary{{ID: "s1"}}}
	cache.Set("/root/mindfs", 5, data, 50*time.Millisecond)

	ok := cache.SetStale("/root/mindfs", 5, "bridge timeout", 200*time.Millisecond)
	if !ok {
		t.Fatal("expected SetStale to return true for existing entry")
	}

	entry := cache.Get("/root/mindfs", 5)
	if entry == nil {
		t.Fatal("expected cache entry")
	}
	if !entry.Stale {
		t.Fatal("entry should be marked stale")
	}
	if entry.LastError != "bridge timeout" {
		t.Fatalf("unexpected error: %s", entry.LastError)
	}
	if entry.LastLatency != 200*time.Millisecond {
		t.Fatalf("unexpected latency: %v", entry.LastLatency)
	}
	// Data should still be accessible
	if entry.Data.Count != 1 {
		t.Fatalf("stale data should still be present: %+v", entry.Data)
	}
}

func TestSessionCacheSetStaleWithNoEntry(t *testing.T) {
	cache := NewSessionCache(60 * time.Second)
	ok := cache.SetStale("/root/mindfs", 5, "bridge error", 10*time.Millisecond)
	if ok {
		t.Fatal("expected SetStale to return false for missing entry")
	}
}

func TestSessionCacheSetFailed(t *testing.T) {
	cache := NewSessionCache(60 * time.Second)
	cache.SetFailed("/root/mindfs", 5, "node not found", 5*time.Millisecond)

	entry := cache.Get("/root/mindfs", 5)
	if entry == nil {
		t.Fatal("expected a failed entry")
	}
	if entry.Stale != true {
		t.Fatal("failed entry should be stale")
	}
	if entry.LastError != "node not found" {
		t.Fatalf("unexpected error: %s", entry.LastError)
	}
	if !entry.CachedAt.IsZero() {
		t.Fatal("failed entry should have zero CachedAt (never had real data)")
	}
}

func TestSessionCacheEntryCount(t *testing.T) {
	cache := NewSessionCache(60 * time.Second)
	if cache.EntryCount() != 0 {
		t.Fatal("expected 0 entries")
	}
	cache.Set("/a", 5, ListSessionsData{Count: 1}, 0)
	cache.Set("/b", 5, ListSessionsData{Count: 1}, 0)
	if cache.EntryCount() != 2 {
		t.Fatalf("expected 2 entries, got %d", cache.EntryCount())
	}
}

func TestSessionCacheStatus(t *testing.T) {
	cache := NewSessionCache(30 * time.Second)
	status := cache.Status()
	if status.Available {
		t.Fatal("initial status should not be available")
	}
	if status.Checked {
		t.Fatal("initial status should not be checked")
	}
	if status.State != "unchecked" {
		t.Fatalf("expected initial state=unchecked, got %s", status.State)
	}
	if status.CacheEntries != 0 {
		t.Fatal("initial cache should have 0 entries")
	}
	if status.TTL != 30*time.Second {
		t.Fatalf("unexpected TTL: %v", status.TTL)
	}

	cache.Set("/root/mindfs", 5, ListSessionsData{Count: 1}, 100*time.Millisecond)
	status = cache.Status()
	if !status.Available {
		t.Fatal("should be available after successful set")
	}
	if !status.Checked {
		t.Fatal("should be checked after successful set")
	}
	if status.State != "available" {
		t.Fatalf("expected state=available, got %s", status.State)
	}
	if status.LastLatency != 100*time.Millisecond {
		t.Fatalf("unexpected latency: %v", status.LastLatency)
	}
	if status.LastError != "" {
		t.Fatalf("unexpected error: %s", status.LastError)
	}
	if status.CacheEntries != 1 {
		t.Fatalf("expected 1 entry, got %d", status.CacheEntries)
	}

	cache.SetStale("/root/mindfs", 5, "timeout", 200*time.Millisecond)
	status = cache.Status()
	if status.Available {
		t.Fatal("should not be available after failure")
	}
	if !status.Checked {
		t.Fatal("should be checked after failure")
	}
	if status.State != "unavailable" {
		t.Fatalf("expected state=unavailable, got %s", status.State)
	}
	if status.LastError != "timeout" {
		t.Fatalf("unexpected error: %s", status.LastError)
	}
}

func TestCacheEntryAge(t *testing.T) {
	entry := CacheEntry{}
	if entry.Age() != 0 {
		t.Fatal("zero CachedAt should have zero Age")
	}
}

func TestCacheEntryIsFreshDefaultTTL(t *testing.T) {
	entry := CacheEntry{CachedAt: time.Now()}
	if !entry.IsFresh(0) {
		t.Fatal("should be fresh with default TTL")
	}
}
