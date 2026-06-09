package pisdkbridge

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCachedClientReturnsFreshCache(t *testing.T) {
	dir := t.TempDir()
	probe := filepath.Join(dir, "probe.mjs")
	writeFile(t, probe, "// fake\n", 0o644)

	node := filepath.Join(dir, "fake-node")
	writeFile(t, node, `#!/bin/sh
callCount=$((callCount + 1))
cat <<JSON
{"type":"response","command":"list-sessions","success":true,"data":{"count":1,"returned":1,"sessions":[{"id":"sid-1","cwd":"/root/mindfs","name":"cached"}]}}
JSON
`, 0o755)

	client := NewClient(ClientOptions{NodeCommand: node, ProbePath: probe, Timeout: time.Second})
	cached := NewCachedClient(client, 60*time.Second)

	// First call should hit the bridge
	data, err := cached.ListSessions(context.Background(), "/root/mindfs", 5)
	if err != nil {
		t.Fatal(err)
	}
	if data.Count != 1 || data.Sessions[0].ID != "sid-1" {
		t.Fatalf("unexpected first call data: %+v", data)
	}

	// Second call should return cached data without hitting the bridge
	data2, err := cached.ListSessions(context.Background(), "/root/mindfs", 5)
	if err != nil {
		t.Fatal(err)
	}
	if data2.Count != 1 || data2.Sessions[0].ID != "sid-1" {
		t.Fatalf("unexpected cached data: %+v", data2)
	}

	// Verify cache entry exists
	if cached.Cache().EntryCount() != 1 {
		t.Fatalf("expected 1 cache entry, got %d", cached.Cache().EntryCount())
	}
}

func TestCachedClientBridgeFailsClosedNoCache(t *testing.T) {
	dir := t.TempDir()
	probe := filepath.Join(dir, "probe.mjs")
	writeFile(t, probe, "// fake\n", 0o644)

	node := filepath.Join(dir, "fail-node")
	writeFile(t, node, `#!/bin/sh
echo '{"type":"response","command":"list-sessions","success":false,"error":{"code":"E_FAIL","message":"bridge down"}}'
exit 1
`, 0o755)

	client := NewClient(ClientOptions{NodeCommand: node, ProbePath: probe, Timeout: time.Second})
	cached := NewCachedClient(client, 60*time.Second)

	// Bridge failure with no prior cache should return empty (fail-closed)
	data, err := cached.ListSessions(context.Background(), "/root/mindfs", 5)
	if err != nil {
		t.Fatalf("cached client should not propagate bridge error: %v", err)
	}
	if data.Count != 0 || len(data.Sessions) != 0 {
		t.Fatalf("expected empty data on fail-closed, got %+v", data)
	}

	// Status should reflect the failure
	status := cached.BridgeStatus()
	if status.Available {
		t.Fatal("bridge should not be available after failure")
	}
	if status.LastError == "" || !strings.Contains(status.LastError, "E_FAIL") {
		t.Fatalf("expected E_FAIL in last error, got: %s", status.LastError)
	}
}

func TestCachedClientBridgeFailsReturnsStaleCache(t *testing.T) {
	dir := t.TempDir()
	probe := filepath.Join(dir, "probe.mjs")
	writeFile(t, probe, "// fake\n", 0o644)

	// Write a node that succeeds first, then we'll replace it
	successNode := filepath.Join(dir, "bridge.sh")
	writeFile(t, successNode, `#!/bin/sh
cat <<JSON
{"type":"response","command":"list-sessions","success":true,"data":{"count":1,"returned":1,"sessions":[{"id":"sid-stale","cwd":"/root/mindfs","name":"stale data"}]}}
JSON
`, 0o755)

	client := NewClient(ClientOptions{NodeCommand: successNode, ProbePath: probe, Timeout: time.Second})
	cached := NewCachedClient(client, 50*time.Millisecond)

	// Populate cache
	data, err := cached.ListSessions(context.Background(), "/root/mindfs", 5)
	if err != nil {
		t.Fatal(err)
	}
	if data.Sessions[0].ID != "sid-stale" {
		t.Fatalf("unexpected initial data: %+v", data)
	}

	// Wait for cache to expire
	time.Sleep(80 * time.Millisecond)

	// Replace bridge script with one that fails
	writeFile(t, successNode, `#!/bin/sh
echo '{"type":"response","command":"list-sessions","success":false,"error":{"code":"E_DOWN","message":"bridge down"}}'
exit 1
`, 0o755)

	// Should return stale data instead of failing
	data2, err := cached.ListSessions(context.Background(), "/root/mindfs", 5)
	if err != nil {
		t.Fatalf("should not error with stale cache: %v", err)
	}
	if data2.Sessions[0].ID != "sid-stale" {
		t.Fatalf("expected stale data, got %+v", data2)
	}

	// Status should show bridge unavailable
	status := cached.BridgeStatus()
	if status.Available {
		t.Fatal("bridge should not be available after failure")
	}
}

func TestCachedClientBridgeStatus(t *testing.T) {
	dir := t.TempDir()
	probe := filepath.Join(dir, "probe.mjs")
	writeFile(t, probe, "// fake\n", 0o644)

	node := filepath.Join(dir, "fake-node")
	writeFile(t, node, `#!/bin/sh
cat <<JSON
{"type":"response","command":"list-sessions","success":true,"data":{"count":0,"returned":0,"sessions":[]}}
JSON
`, 0o755)

	client := NewClient(ClientOptions{NodeCommand: node, ProbePath: probe, Timeout: time.Second})
	cached := NewCachedClient(client, 30*time.Second)

	status := cached.BridgeStatus()
	if status.TTL != 30*time.Second {
		t.Fatalf("unexpected TTL: %v", status.TTL)
	}

	// After a call, status should update
	_, _ = cached.ListSessions(context.Background(), "/root/mindfs", 5)
	status = cached.BridgeStatus()
	if !status.Available {
		t.Fatal("should be available after successful call")
	}
	if status.CacheEntries != 1 {
		t.Fatalf("expected 1 cache entry, got %d", status.CacheEntries)
	}
}

func TestCachedClientExposesUnderlyingClient(t *testing.T) {
	client := NewClient(ClientOptions{Timeout: 5 * time.Second})
	cached := NewCachedClient(client, 0)
	if cached.Client() != client {
		t.Fatal("Client() should return the underlying client")
	}
}
