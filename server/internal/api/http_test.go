package api

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agenttypes "mindfs/server/internal/agent/types"
	"mindfs/server/internal/e2ee"
	"mindfs/server/internal/fs"
	"mindfs/server/internal/relay"
	"mindfs/server/internal/session"
)

func TestSessionResponseIncludesAuthoritativePendingState(t *testing.T) {
	handler := &HTTPHandler{}
	sess := &session.Session{
		Key:       "session-1",
		Type:      session.TypeChat,
		Name:      "Restarted session",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}

	for _, tt := range []struct {
		name    string
		pending bool
	}{
		{name: "active turn", pending: true},
		{name: "fresh process", pending: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			payload := handler.sessionResponse(sess, nil, tt.pending, agenttypes.ContextWindow{}, nil)
			pending, ok := payload["pending"].(bool)
			if !ok || pending != tt.pending {
				t.Fatalf("pending = %#v, want %t", payload["pending"], tt.pending)
			}
		})
	}
}

func TestPathForStaticAssetCleansURLPaths(t *testing.T) {
	tests := []struct {
		name        string
		requestPath string
		want        string
	}{
		{
			name:        "absolute asset path",
			requestPath: "/assets/app.js",
			want:        "assets/app.js",
		},
		{
			name:        "duplicate slash path",
			requestPath: "//assets/app.js",
			want:        "assets/app.js",
		},
		{
			name:        "root path",
			requestPath: "/",
			want:        "",
		},
		{
			name:        "relayed asset alias",
			requestPath: "/mindfs-assets/index-BhhZaySO.js",
			want:        "assets/index-BhhZaySO.js",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pathForStaticAsset(tt.requestPath)
			if got != tt.want {
				t.Fatalf("pathForStaticAsset(%q) = %q, want %q", tt.requestPath, got, tt.want)
			}
		})
	}
}

func TestServeStaticAssetNegotiatesPrecompressedRepresentations(t *testing.T) {
	staticDir := t.TempDir()
	assetsDir := filepath.Join(staticDir, "assets")
	if err := os.Mkdir(assetsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	identity := []byte("console.log('identity')")
	brotli := []byte("brotli-representation")
	gzip := []byte("gzip-representation")
	assetPath := filepath.Join(assetsDir, "index-test.js")
	if err := os.WriteFile(assetPath, identity, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(assetPath+".br", brotli, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(assetPath+".gz", gzip, 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		name           string
		method         string
		requestPath    string
		acceptEncoding string
		wantEncoding   string
		wantBody       []byte
	}{
		{
			name:           "brotli preferred on equal quality",
			method:         http.MethodGet,
			requestPath:    "/assets/index-test.js",
			acceptEncoding: "gzip, br",
			wantEncoding:   "br",
			wantBody:       brotli,
		},
		{
			name:           "higher gzip quality wins",
			method:         http.MethodGet,
			requestPath:    "/assets/index-test.js",
			acceptEncoding: "br;q=0.2, gzip;q=0.8",
			wantEncoding:   "gzip",
			wantBody:       gzip,
		},
		{
			name:           "disabled encodings use identity",
			method:         http.MethodGet,
			requestPath:    "/assets/index-test.js",
			acceptEncoding: "br;q=0, gzip;q=0",
			wantBody:       identity,
		},
		{
			name:        "missing header uses identity",
			method:      http.MethodGet,
			requestPath: "/assets/index-test.js",
			wantBody:    identity,
		},
		{
			name:           "relay alias negotiates brotli",
			method:         http.MethodGet,
			requestPath:    "/mindfs-assets/index-test.js",
			acceptEncoding: "br",
			wantEncoding:   "br",
			wantBody:       brotli,
		},
		{
			name:           "head exposes compressed representation headers",
			method:         http.MethodHead,
			requestPath:    "/assets/index-test.js",
			acceptEncoding: "gzip",
			wantEncoding:   "gzip",
			wantBody:       nil,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			handler := &HTTPHandler{StaticDir: staticDir}
			req := httptest.NewRequest(tt.method, tt.requestPath, nil)
			if tt.acceptEncoding != "" {
				req.Header.Set("Accept-Encoding", tt.acceptEncoding)
			}
			resp := httptest.NewRecorder()
			resp.Header().Set("Vary", "Origin")

			if !handler.serveStaticAsset(resp, req) {
				t.Fatal("serveStaticAsset returned false")
			}
			if got := resp.Header().Get("Content-Encoding"); got != tt.wantEncoding {
				t.Fatalf("Content-Encoding = %q, want %q", got, tt.wantEncoding)
			}
			if got := resp.Header().Get("Content-Type"); !strings.Contains(got, "javascript") {
				t.Fatalf("Content-Type = %q, want JavaScript", got)
			}
			vary := strings.Join(resp.Header().Values("Vary"), ",")
			if !strings.Contains(vary, "Origin") || !strings.Contains(vary, "Accept-Encoding") {
				t.Fatalf("Vary = %q, want Origin and Accept-Encoding", vary)
			}
			if got := resp.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
				t.Fatalf("Cache-Control = %q", got)
			}
			if !bytes.Equal(resp.Body.Bytes(), tt.wantBody) {
				t.Fatalf("body = %q, want %q", resp.Body.Bytes(), tt.wantBody)
			}
		})
	}
}

func TestAcceptedEncodingQuality(t *testing.T) {
	for _, tt := range []struct {
		name     string
		header   string
		encoding string
		want     float64
	}{
		{name: "explicit default", header: "gzip, br", encoding: "br", want: 1},
		{name: "case insensitive", header: "BR;Q=0.7", encoding: "br", want: 0.7},
		{name: "wildcard fallback", header: "*;q=0.4", encoding: "gzip", want: 0.4},
		{name: "explicit disable overrides wildcard", header: "br;q=0, *;q=0.8", encoding: "br", want: 0},
		{name: "invalid quality disables encoding", header: "gzip;q=invalid", encoding: "gzip", want: 0},
		{name: "unlisted encoding rejected", header: "zstd", encoding: "br", want: 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := acceptedEncodingQuality(tt.header, tt.encoding); got != tt.want {
				t.Fatalf("acceptedEncodingQuality(%q, %q) = %v, want %v", tt.header, tt.encoding, got, tt.want)
			}
		})
	}
}

func TestServeStaticAssetFallsBackWithoutPrecompressedSibling(t *testing.T) {
	staticDir := t.TempDir()
	assetPath := filepath.Join(staticDir, "assets", "index-test.js")
	if err := os.Mkdir(filepath.Dir(assetPath), 0o755); err != nil {
		t.Fatal(err)
	}
	identity := []byte("console.log('identity')")
	if err := os.WriteFile(assetPath, identity, 0o644); err != nil {
		t.Fatal(err)
	}

	handler := &HTTPHandler{StaticDir: staticDir}
	req := httptest.NewRequest(http.MethodGet, "/assets/index-test.js", nil)
	req.Header.Set("Accept-Encoding", "br, gzip")
	resp := httptest.NewRecorder()

	if !handler.serveStaticAsset(resp, req) {
		t.Fatal("serveStaticAsset returned false")
	}
	if got := resp.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding = %q, want identity", got)
	}
	if got := resp.Header().Get("Vary"); got != "" {
		t.Fatalf("Vary = %q without alternative representations", got)
	}
	if !bytes.Equal(resp.Body.Bytes(), identity) {
		t.Fatalf("body = %q, want %q", resp.Body.Bytes(), identity)
	}
}

func TestServeFrontendIndexRewritesRelayedAssetRefsForReleaseVersion(t *testing.T) {
	staticDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(staticDir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(staticDir, "index.html")
	content := `<!doctype html><script type="module" src="./assets/index-test.js"></script><link rel="stylesheet" href="./assets/index-test.css">`
	if err := os.WriteFile(indexPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staticDir, "assets", "index-test.js"), []byte("console.log('ok')"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staticDir, "assets", "index-test.css"), []byte("body{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	handler := &HTTPHandler{StaticDir: staticDir, Version: "v0.3.5"}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-MindFS-Relayed", "1")
	resp := httptest.NewRecorder()

	handler.serveFrontendIndex(resp, req, staticDir, indexPath)

	body := resp.Body.String()
	if strings.Contains(body, "./assets/") {
		t.Fatalf("body still contains local assets path: %s", body)
	}
	if !strings.Contains(body, "/mindfs-assets/index-test.js") || !strings.Contains(body, "/mindfs-assets/index-test.css") {
		t.Fatalf("body missing relayed asset paths: %s", body)
	}
}

func TestServeFrontendIndexKeepsLocalAssetRefsForDevVersion(t *testing.T) {
	staticDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(staticDir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(staticDir, "index.html")
	content := `<!doctype html><script type="module" src="./assets/index-test.js"></script>`
	if err := os.WriteFile(indexPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staticDir, "assets", "index-test.js"), []byte("console.log('ok')"), 0o644); err != nil {
		t.Fatal(err)
	}

	handler := &HTTPHandler{StaticDir: staticDir, Version: "v0.3.5-9-g92b8c85-dirty"}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-MindFS-Relayed", "1")
	resp := httptest.NewRecorder()

	handler.serveFrontendIndex(resp, req, staticDir, indexPath)

	body := resp.Body.String()
	if !strings.Contains(body, "./assets/index-test.js") {
		t.Fatalf("body should keep local asset path for dev version: %s", body)
	}
	if strings.Contains(body, "/mindfs-assets/") {
		t.Fatalf("body should not contain relayed asset path for dev version: %s", body)
	}
}

func TestIsStandardReleaseVersion(t *testing.T) {
	tests := []struct {
		version string
		want    bool
	}{
		{version: "v0.3.5", want: true},
		{version: "0.3.5", want: true},
		{version: "v0.3.5-9-g92b8c85-dirty", want: false},
		{version: "dev", want: false},
		{version: "", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			if got := isStandardReleaseVersion(tt.version); got != tt.want {
				t.Fatalf("isStandardReleaseVersion(%q) = %v, want %v", tt.version, got, tt.want)
			}
		})
	}
}

func TestIndexHTMLUsesTextContentForTreeEntries(t *testing.T) {
	if strings.Contains(indexHTML, "card.innerHTML") || strings.Contains(indexHTML, "<span>\" + entry.name") {
		t.Fatalf("indexHTML should not concatenate entry.name into HTML")
	}
	if !strings.Contains(indexHTML, "name.textContent = entry.name") {
		t.Fatalf("indexHTML should render entry names with textContent")
	}
}

func TestCopyFileTightensExistingDestinationPermissions(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source.json")
	dst := filepath.Join(dir, "dest.json")
	if err := os.WriteFile(src, []byte("secret"), 0o600); err != nil {
		t.Fatalf("WriteFile source returned error: %v", err)
	}
	if err := os.WriteFile(dst, []byte("old"), 0o644); err != nil {
		t.Fatalf("WriteFile dest returned error: %v", err)
	}
	if err := os.Chmod(dst, 0o644); err != nil {
		t.Fatalf("Chmod setup returned error: %v", err)
	}

	if err := copyFile(src, dst); err != nil {
		t.Fatalf("copyFile returned error: %v", err)
	}
	payload, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("ReadFile dest returned error: %v", err)
	}
	if string(payload) != "secret" {
		t.Fatalf("dest content = %q, want secret", string(payload))
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("Stat dest returned error: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("dest mode = %o, want 600", got)
	}
}

func TestCopyFilePreservesDestinationOnCopyError(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source-dir")
	dst := filepath.Join(dir, "dest.json")
	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatalf("Mkdir source returned error: %v", err)
	}
	if err := os.WriteFile(dst, []byte("old config"), 0o600); err != nil {
		t.Fatalf("WriteFile dest returned error: %v", err)
	}

	if err := copyFile(src, dst); err == nil {
		t.Fatal("copyFile error = nil, want copy error")
	}
	payload, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("ReadFile dest returned error: %v", err)
	}
	if string(payload) != "old config" {
		t.Fatalf("dest content = %q, want old config", string(payload))
	}
	matches, err := filepath.Glob(filepath.Join(dir, "dest.json.tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary files remain: %#v", matches)
	}
}

func TestReplaceAgentConfigBackupDirRestoresOldOnPromotionFailure(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "pi-backup")
	if err := os.Mkdir(dst, 0o755); err != nil {
		t.Fatalf("Mkdir old backup returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dst, "keep.json"), []byte("old backup"), 0o600); err != nil {
		t.Fatalf("WriteFile old backup returned error: %v", err)
	}

	if _, err := replaceAgentConfigBackupDir(dst, filepath.Join(dir, "missing-staging")); err == nil {
		t.Fatal("replaceAgentConfigBackupDir error = nil, want promotion error")
	}
	payload, err := os.ReadFile(filepath.Join(dst, "keep.json"))
	if err != nil {
		t.Fatalf("old backup was not restored: %v", err)
	}
	if string(payload) != "old backup" {
		t.Fatalf("old backup content = %q, want old backup", string(payload))
	}
}

func TestReplaceAgentConfigBackupDirRollbackRestoresOldBackup(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "pi-backup")
	staging := filepath.Join(dir, "staging")
	if err := os.Mkdir(dst, 0o755); err != nil {
		t.Fatalf("Mkdir old backup returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dst, "old.json"), []byte("old backup"), 0o600); err != nil {
		t.Fatalf("WriteFile old backup returned error: %v", err)
	}
	if err := os.Mkdir(staging, 0o755); err != nil {
		t.Fatalf("Mkdir staging returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staging, "new.json"), []byte("new backup"), 0o600); err != nil {
		t.Fatalf("WriteFile staging returned error: %v", err)
	}

	finalize, err := replaceAgentConfigBackupDir(dst, staging)
	if err != nil {
		t.Fatalf("replaceAgentConfigBackupDir returned error: %v", err)
	}
	finalize(false)
	payload, err := os.ReadFile(filepath.Join(dst, "old.json"))
	if err != nil {
		t.Fatalf("old backup was not restored: %v", err)
	}
	if string(payload) != "old backup" {
		t.Fatalf("old backup content = %q, want old backup", string(payload))
	}
	if _, err := os.Stat(filepath.Join(dst, "new.json")); !os.IsNotExist(err) {
		t.Fatalf("new backup remained after rollback: %v", err)
	}
}

func TestReplaceAgentConfigBackupDirDoesNotDeleteNeighborOldBackup(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "pi-backup")
	staging := filepath.Join(dir, "staging")
	neighborOld := dst + ".old"
	if err := os.Mkdir(dst, 0o755); err != nil {
		t.Fatalf("Mkdir old backup returned error: %v", err)
	}
	if err := os.Mkdir(staging, 0o755); err != nil {
		t.Fatalf("Mkdir staging returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staging, "new.json"), []byte("new backup"), 0o600); err != nil {
		t.Fatalf("WriteFile staging returned error: %v", err)
	}
	if err := os.Mkdir(neighborOld, 0o755); err != nil {
		t.Fatalf("Mkdir neighbor .old returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(neighborOld, "keep.json"), []byte("neighbor backup"), 0o600); err != nil {
		t.Fatalf("WriteFile neighbor .old returned error: %v", err)
	}

	finalize, err := replaceAgentConfigBackupDir(dst, staging)
	if err != nil {
		t.Fatalf("replaceAgentConfigBackupDir returned error: %v", err)
	}
	finalize(true)
	payload, err := os.ReadFile(filepath.Join(neighborOld, "keep.json"))
	if err != nil {
		t.Fatalf("neighbor .old backup was removed: %v", err)
	}
	if string(payload) != "neighbor backup" {
		t.Fatalf("neighbor .old content = %q, want neighbor backup", string(payload))
	}
}

func TestNormalizeAgentConfigRequestRejectsUnsafeGeneratedID(t *testing.T) {
	if _, _, _, err := normalizeAgentConfigRequest("../agent", "backup"); err == nil {
		t.Fatal("normalizeAgentConfigRequest accepted unsafe agent name")
	}
	if _, _, id, err := normalizeAgentConfigRequest("pi", "backup"); err != nil || id != "pi-backup" {
		t.Fatalf("normalizeAgentConfigRequest valid = id %q err %v, want pi-backup", id, err)
	}
}

func TestAgentConfigBackupPathRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	if _, err := agentConfigBackupPath(root, "../escape"); err == nil {
		t.Fatal("agentConfigBackupPath accepted traversal")
	}
	if _, err := agentConfigBackupDir(root, "../escape"); err == nil {
		t.Fatal("agentConfigBackupDir accepted unsafe id")
	}
	if _, err := agentConfigBackupEntryPath(root, "pi-backup", "other-backup/001-config.json"); err == nil {
		t.Fatal("agentConfigBackupEntryPath accepted path outside entry")
	}
	got, err := agentConfigBackupEntryPath(root, "pi-backup", "pi-backup/001-config.json")
	if err != nil {
		t.Fatalf("agentConfigBackupEntryPath valid returned error: %v", err)
	}
	if rel, err := filepath.Rel(root, got); err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		t.Fatalf("path %q escaped root %q", got, root)
	}
}

func TestAgentConfigJSONWritersUseAtomicFileReplacement(t *testing.T) {
	configRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("HOME", configRoot)

	manifest := []agentConfigManifestEntry{{ID: "pi-backup", Agent: "pi", Name: "backup"}}
	if err := writeAgentConfigManifest(manifest); err != nil {
		t.Fatalf("writeAgentConfigManifest returned error: %v", err)
	}
	manifestPath, err := agentConfigManifestPath()
	if err != nil {
		t.Fatalf("agentConfigManifestPath returned error: %v", err)
	}
	assertModeAndNoTemps(t, manifestPath, 0o644)
	gotManifest, err := readAgentConfigManifest()
	if err != nil {
		t.Fatalf("readAgentConfigManifest returned error: %v", err)
	}
	if len(gotManifest) != 1 || gotManifest[0].ID != "pi-backup" {
		t.Fatalf("manifest = %#v, want pi-backup entry", gotManifest)
	}

	backups := map[string][]string{"pi-backup": {"FOO=bar"}}
	if err := writeAgentEnvBackups(backups); err != nil {
		t.Fatalf("writeAgentEnvBackups returned error: %v", err)
	}
	envPath, err := agentEnvPath()
	if err != nil {
		t.Fatalf("agentEnvPath returned error: %v", err)
	}
	assertModeAndNoTemps(t, envPath, 0o644)
	gotEnv, err := readAgentEnvBackups()
	if err != nil {
		t.Fatalf("readAgentEnvBackups returned error: %v", err)
	}
	if got := gotEnv["pi-backup"]; len(got) != 1 || got[0] != "FOO=bar" {
		t.Fatalf("env backups = %#v, want pi-backup FOO=bar", gotEnv)
	}
}

func assertModeAndNoTemps(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%q) returned error: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %o, want %o", path, got, want)
	}
	temps, err := filepath.Glob(filepath.Join(filepath.Dir(path), filepath.Base(path)+".tmp-*"))
	if err != nil {
		t.Fatalf("Glob temp files for %q returned error: %v", path, err)
	}
	if len(temps) != 0 {
		t.Fatalf("temp files left behind for %q: %#v", path, temps)
	}
}

func TestIsLocalCLIRequestRequiresTokenLoopbackAndWhitelistedRoute(t *testing.T) {
	handler := &HTTPHandler{LocalCLIToken: "secret-token"}
	req := httptest.NewRequest(http.MethodPost, "/api/dirs", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	req.Header.Set(localCLIHeaderName, "secret-token")

	if !handler.isLocalCLIRequest(req) {
		t.Fatal("expected local CLI request to be accepted")
	}
}

func TestIsLocalCLIRequestAllowsRelayBindStart(t *testing.T) {
	handler := &HTTPHandler{LocalCLIToken: "secret-token"}
	req := httptest.NewRequest(http.MethodPost, "/api/relay/bind/start", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	req.Header.Set(localCLIHeaderName, "secret-token")

	if !handler.isLocalCLIRequest(req) {
		t.Fatal("expected local CLI relay bind request to be accepted")
	}
}

func TestIsLocalCLIRequestRejectsNonWhitelistedRoute(t *testing.T) {
	handler := &HTTPHandler{LocalCLIToken: "secret-token"}
	req := httptest.NewRequest(http.MethodGet, "/api/tree", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	req.Header.Set(localCLIHeaderName, "secret-token")

	if handler.isLocalCLIRequest(req) {
		t.Fatal("expected non-whitelisted route to be rejected")
	}
}

func TestIsLocalCLIRequestRejectsRemoteAddress(t *testing.T) {
	handler := &HTTPHandler{LocalCLIToken: "secret-token"}
	req := httptest.NewRequest(http.MethodPost, "/api/dirs", nil)
	req.RemoteAddr = "192.0.2.1:54321"
	req.Header.Set(localCLIHeaderName, "secret-token")

	if handler.isLocalCLIRequest(req) {
		t.Fatal("expected remote address to be rejected")
	}
}

func TestIsLocalCLIRequestRejectsInvalidToken(t *testing.T) {
	handler := &HTTPHandler{LocalCLIToken: "secret-token"}
	req := httptest.NewRequest(http.MethodPost, "/api/dirs", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	req.Header.Set(localCLIHeaderName, "wrong-token")

	if handler.isLocalCLIRequest(req) {
		t.Fatal("expected invalid token to be rejected")
	}
}

func TestRelayStatusWithE2EEDoesNotSetNodeIDWhenE2EEDisabled(t *testing.T) {
	handler := &HTTPHandler{AppContext: &AppContext{
		E2EE: e2ee.NewManager(e2ee.Config{Enabled: false, NodeID: "node-id", PairingSecret: "secret"}),
	}}
	status := handler.relayStatusWithE2EE(relay.Status{NodeID: "relay-node"})

	if status.E2EERequired {
		t.Fatal("expected E2EERequired to be false")
	}
	if status.E2EENodeID != "" {
		t.Fatalf("E2EENodeID = %q, want empty", status.E2EENodeID)
	}
	if status.NodeID != "relay-node" {
		t.Fatalf("NodeID = %q, want relay-node", status.NodeID)
	}
}

func TestRequireRequestProofDoesNotRefreshSessionOnInvalidProof(t *testing.T) {
	manager := e2ee.NewManager(e2ee.Config{Enabled: true, NodeID: "node-id", PairingSecret: "secret"})
	clientID := "client-1"
	if _, err := manager.OpenSessionForClient(clientID, e2ee.DerivedKey{Transport: []byte("0123456789abcdef0123456789abcdef")}); err != nil {
		t.Fatalf("OpenSessionForClient returned error: %v", err)
	}
	before, err := manager.SessionForClientNoTouch(clientID)
	if err != nil {
		t.Fatalf("SessionForClientNoTouch before returned error: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	handler := &HTTPHandler{AppContext: &AppContext{E2EE: manager}}
	req := httptest.NewRequest(http.MethodGet, "/api/file?root=r&path=a.txt", nil)
	req.Header.Set(clientIDHeaderName, clientID)
	req.Header.Set(e2eeTSHeaderName, time.Now().UTC().Format(time.RFC3339))
	req.Header.Set(e2eeProofHeaderName, "invalid-proof")

	if _, err := handler.requireRequestProof(req); err == nil {
		t.Fatal("requireRequestProof error = nil, want invalid proof")
	}
	after, err := manager.SessionForClientNoTouch(clientID)
	if err != nil {
		t.Fatalf("SessionForClientNoTouch after returned error: %v", err)
	}
	if !after.LastSeenAt.Equal(before.LastSeenAt) {
		t.Fatalf("LastSeenAt changed after invalid proof: before=%s after=%s", before.LastSeenAt, after.LastSeenAt)
	}
}

func TestRequireRequestProofRejectsReplayWithoutRefreshingSession(t *testing.T) {
	manager := e2ee.NewManager(e2ee.Config{Enabled: true, NodeID: "node-id", PairingSecret: "secret"})
	key := []byte("0123456789abcdef0123456789abcdef")
	clientID := "client-1"
	if _, err := manager.OpenSessionForClient(clientID, e2ee.DerivedKey{Transport: key}); err != nil {
		t.Fatalf("OpenSessionForClient returned error: %v", err)
	}
	handler := &HTTPHandler{AppContext: &AppContext{E2EE: manager}}
	request := func() *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/api/file?root=r&path=a.txt", nil)
		ts := time.Now().UTC().Format(time.RFC3339Nano)
		req.Header.Set(clientIDHeaderName, clientID)
		req.Header.Set(e2eeTSHeaderName, ts)
		req.Header.Set(e2eeProofHeaderName, e2ee.BuildRequestProof(key, http.MethodGet, requestProofPath(req), ts, clientID))
		return req
	}
	req := request()
	if _, err := handler.requireRequestProof(req); err != nil {
		t.Fatalf("first requireRequestProof returned error: %v", err)
	}
	afterFirst, err := manager.SessionForClientNoTouch(clientID)
	if err != nil {
		t.Fatalf("SessionForClientNoTouch after first proof: %v", err)
	}
	if _, err := handler.requireRequestProof(req); err == nil || !strings.Contains(err.Error(), "e2ee_proof_replayed") {
		t.Fatalf("replayed requireRequestProof error = %v, want e2ee_proof_replayed", err)
	}
	afterReplay, err := manager.SessionForClientNoTouch(clientID)
	if err != nil {
		t.Fatalf("SessionForClientNoTouch after replay: %v", err)
	}
	if !afterReplay.LastSeenAt.Equal(afterFirst.LastSeenAt) {
		t.Fatalf("LastSeenAt changed after replay: first=%s replay=%s", afterFirst.LastSeenAt, afterReplay.LastSeenAt)
	}
}

func TestHandleE2EEOpenRejectsReplayedClientNonce(t *testing.T) {
	manager := e2ee.NewManager(e2ee.Config{Enabled: true, NodeID: "node", PairingSecret: "secret"})
	handler := &HTTPHandler{AppContext: &AppContext{E2EE: manager}}
	_, clientEphPK, err := e2ee.GenerateECDHKeypair()
	if err != nil {
		t.Fatalf("GenerateECDHKeypair returned error: %v", err)
	}
	clientID := "client-open"
	open := func(nonce string) *httptest.ResponseRecorder {
		payload := map[string]string{
			"client_id":     clientID,
			"node_id":       "node",
			"client_eph_pk": clientEphPK,
			"client_nonce":  nonce,
			"proof":         e2ee.BuildOpenProof("secret", "node", clientEphPK, nonce),
		}
		body, marshalErr := json.Marshal(payload)
		if marshalErr != nil {
			t.Fatalf("Marshal payload: %v", marshalErr)
		}
		resp := httptest.NewRecorder()
		handler.handleE2EEOpen(resp, httptest.NewRequest(http.MethodPost, "/api/e2ee/open", bytes.NewReader(body)))
		return resp
	}

	first := open("nonce-1")
	if first.Code != http.StatusOK {
		t.Fatalf("first open status = %d body=%s", first.Code, first.Body.String())
	}
	firstSession, err := manager.SessionForClientNoTouch(clientID)
	if err != nil {
		t.Fatalf("SessionForClientNoTouch after first open: %v", err)
	}
	replayed := open("nonce-1")
	if replayed.Code != http.StatusConflict || !strings.Contains(replayed.Body.String(), "e2ee_open_replayed") {
		t.Fatalf("replayed open status = %d body=%s", replayed.Code, replayed.Body.String())
	}
	afterReplay, err := manager.SessionForClientNoTouch(clientID)
	if err != nil {
		t.Fatalf("SessionForClientNoTouch after replay: %v", err)
	}
	if afterReplay.ID != firstSession.ID {
		t.Fatalf("replayed open replaced session: got %s want %s", afterReplay.ID, firstSession.ID)
	}
	second := open("nonce-2")
	if second.Code != http.StatusOK {
		t.Fatalf("new nonce open status = %d body=%s", second.Code, second.Body.String())
	}
}

func TestHandleE2EEOpenNegotiatesProtocolVersion(t *testing.T) {
	manager := e2ee.NewManager(e2ee.Config{Enabled: true, NodeID: "node", PairingSecret: "secret"})
	handler := &HTTPHandler{AppContext: &AppContext{E2EE: manager}}
	_, clientEphPK, err := e2ee.GenerateECDHKeypair()
	if err != nil {
		t.Fatalf("GenerateECDHKeypair: %v", err)
	}
	payload := map[string]any{
		"client_id":     "client-v2",
		"node_id":       "node",
		"client_eph_pk": clientEphPK,
		"client_nonce":  "nonce-v2",
		"proof":         e2ee.BuildOpenProof("secret", "node", clientEphPK, "nonce-v2"),
		"proto_version": e2ee.ProtocolVersionV2,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Marshal payload: %v", err)
	}
	resp := httptest.NewRecorder()
	handler.handleE2EEOpen(resp, httptest.NewRequest(http.MethodPost, "/api/e2ee/open", bytes.NewReader(body)))
	if resp.Code != http.StatusOK {
		t.Fatalf("open status = %d body=%s", resp.Code, resp.Body.String())
	}
	var response struct {
		ProtoVersion int    `json:"proto_version"`
		NodeEphPK    string `json:"node_eph_pk"`
		ServerNonce  string `json:"server_nonce"`
		ServerProof  string `json:"server_proof"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &response); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	if response.ProtoVersion != e2ee.ProtocolVersionV2 {
		t.Fatalf("response protocol = %d, want %d", response.ProtoVersion, e2ee.ProtocolVersionV2)
	}
	expectedProof := e2ee.BuildAcceptProofForProtocol("secret", "node", clientEphPK, response.NodeEphPK, "nonce-v2", response.ServerNonce, e2ee.ProtocolVersionV2)
	if !e2ee.VerifyProof(expectedProof, response.ServerProof) {
		t.Fatal("v2 response proof did not bind the negotiated protocol")
	}
	sess, err := manager.SessionForClientNoTouch("client-v2")
	if err != nil {
		t.Fatalf("SessionForClientNoTouch: %v", err)
	}
	if sess.ProtocolVersion != e2ee.ProtocolVersionV2 {
		t.Fatalf("session protocol = %d, want %d", sess.ProtocolVersion, e2ee.ProtocolVersionV2)
	}
}

func TestHandleE2EEOpenRejectsUnsupportedProtocolVersion(t *testing.T) {
	manager := e2ee.NewManager(e2ee.Config{Enabled: true, NodeID: "node", PairingSecret: "secret"})
	handler := &HTTPHandler{AppContext: &AppContext{E2EE: manager}}
	body := bytes.NewBufferString(`{"client_id":"client","node_id":"node","client_eph_pk":"key","client_nonce":"nonce","proof":"proof","proto_version":99}`)
	resp := httptest.NewRecorder()
	handler.handleE2EEOpen(resp, httptest.NewRequest(http.MethodPost, "/api/e2ee/open", body))
	if resp.Code != http.StatusBadRequest || !strings.Contains(resp.Body.String(), "e2ee_protocol_unsupported") {
		t.Fatalf("unsupported protocol status = %d body=%s", resp.Code, resp.Body.String())
	}
}

func TestRelayStatusWithE2EEDoesNotFallbackNodeIDWhenEnabled(t *testing.T) {
	handler := &HTTPHandler{AppContext: &AppContext{
		E2EE: e2ee.NewManager(e2ee.Config{Enabled: true, NodeID: "e2ee-node", PairingSecret: "secret"}),
	}}
	status := handler.relayStatusWithE2EE(relay.Status{})

	if !status.E2EERequired {
		t.Fatal("expected E2EERequired to be true")
	}
	if status.E2EENodeID != "e2ee-node" {
		t.Fatalf("E2EENodeID = %q, want e2ee-node", status.E2EENodeID)
	}
	if status.NodeID != "" {
		t.Fatalf("NodeID = %q, want empty", status.NodeID)
	}
}

func TestHandleFileRawEncryptsResponseWhenE2EERequired(t *testing.T) {
	rootDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootDir, "secret.txt"), []byte("raw secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := fs.NewRegistry(filepath.Join(t.TempDir(), "registry.json"))
	root, err := registry.Upsert(rootDir)
	if err != nil {
		t.Fatalf("registry.Upsert: %v", err)
	}
	key := []byte("0123456789abcdef0123456789abcdef")
	clientID := "client-raw"
	manager := e2ee.NewManager(e2ee.Config{Enabled: true, NodeID: "node", PairingSecret: "secret"})
	if _, err := manager.OpenSessionForClient(clientID, e2ee.DerivedKey{Transport: key, ProtocolVersion: e2ee.ProtocolVersionV2}); err != nil {
		t.Fatalf("OpenSessionForClient: %v", err)
	}
	handler := &HTTPHandler{AppContext: &AppContext{Dirs: registry, E2EE: manager}}
	query := url.Values{"root": {root.ID}, "path": {"secret.txt"}, "raw": {"1"}}
	req := httptest.NewRequest(http.MethodGet, "/api/file?"+query.Encode(), nil)
	ts := time.Now().UTC().Format(time.RFC3339)
	req.Header.Set(clientIDHeaderName, clientID)
	req.Header.Set(e2eeTSHeaderName, ts)
	proof := e2ee.BuildRequestProof(key, http.MethodGet, requestProofPath(req), ts, clientID)
	req.Header.Set(e2eeProofHeaderName, proof)
	resp := httptest.NewRecorder()

	handler.handleFile(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	if strings.Contains(resp.Body.String(), "raw secret") {
		t.Fatal("protected raw response leaked plaintext")
	}
	if got := resp.Header().Get(e2eeHeaderName); got != "1" {
		t.Fatalf("%s = %q, want 1", e2eeHeaderName, got)
	}
	var payload struct {
		e2ee.CipherEnvelope
		ContentType string `json:"content_type"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode encrypted payload: %v", err)
	}
	plain, err := e2ee.DecryptBytesWithAAD(key, &payload.CipherEnvelope, rawResponseAAD(proof, payload.ContentType))
	if err != nil {
		t.Fatalf("DecryptBytesWithAAD: %v", err)
	}
	if string(plain) != "raw secret" {
		t.Fatalf("plaintext = %q, want raw secret", string(plain))
	}
	if !strings.HasPrefix(payload.ContentType, "text/plain") {
		t.Fatalf("content_type = %q, want text/plain", payload.ContentType)
	}
	if _, err := e2ee.DecryptBytesWithAAD(key, &payload.CipherEnvelope, rawResponseAAD("different-proof", payload.ContentType)); err == nil {
		t.Fatal("raw response decrypted under a different request proof")
	}
	if _, err := e2ee.DecryptBytes(key, &payload.CipherEnvelope); err == nil {
		t.Fatal("raw response decrypted without authenticated context")
	}
}

func TestProtectedEndpointBindsV2ResponseToRequestProof(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	clientID := "client-v2-response"
	manager := e2ee.NewManager(e2ee.Config{Enabled: true, NodeID: "node", PairingSecret: "secret"})
	if _, err := manager.OpenSessionForClient(clientID, e2ee.DerivedKey{Transport: key, ProtocolVersion: e2ee.ProtocolVersionV2}); err != nil {
		t.Fatalf("OpenSessionForClient: %v", err)
	}
	handler := &HTTPHandler{AppContext: &AppContext{E2EE: manager}}
	protected := handler.protectedEndpoint(func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	req := httptest.NewRequest(http.MethodGet, "/api/tree?root=root-1", nil)
	req.Header.Set(e2eeHeaderName, "1")
	ts := time.Now().UTC().Format(time.RFC3339)
	proof := e2ee.BuildRequestProof(key, req.Method, requestProofPath(req), ts, clientID)
	req.Header.Set(clientIDHeaderName, clientID)
	req.Header.Set(e2eeTSHeaderName, ts)
	req.Header.Set(e2eeProofHeaderName, proof)
	resp := httptest.NewRecorder()

	protected(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	if got := resp.Header().Get(e2eeHeaderName); got != "1" {
		t.Fatalf("%s = %q, want 1", e2eeHeaderName, got)
	}
	var envelope e2ee.CipherEnvelope
	if err := json.Unmarshal(resp.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode protected response: %v", err)
	}
	var payload map[string]string
	if err := e2ee.DecryptJSONWithAAD(key, &envelope, []byte(proof), &payload); err != nil {
		t.Fatalf("DecryptJSONWithAAD: %v", err)
	}
	if payload["status"] != "ok" {
		t.Fatalf("payload = %#v, want status=ok", payload)
	}
	if err := e2ee.DecryptJSONWithAAD(key, &envelope, []byte("different-proof"), &payload); err == nil {
		t.Fatal("protected response decrypted under a different request proof")
	}
	if err := e2ee.DecryptJSON(key, &envelope, &payload); err == nil {
		t.Fatal("protected response decrypted without authenticated context")
	}
}

func TestWriteProtectedJSONPreservesV1Compatibility(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	resp := httptest.NewRecorder()
	err := writeProtectedJSON(resp, http.StatusOK, &e2ee.Session{Key: key, ProtocolVersion: e2ee.ProtocolVersionV1}, "ignored-proof", map[string]string{"status": "ok"})
	if err != nil {
		t.Fatalf("writeProtectedJSON: %v", err)
	}
	var envelope e2ee.CipherEnvelope
	if err := json.Unmarshal(resp.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode protected response: %v", err)
	}
	var payload map[string]string
	if err := e2ee.DecryptJSON(key, &envelope, &payload); err != nil {
		t.Fatalf("DecryptJSON: %v", err)
	}
	if payload["status"] != "ok" {
		t.Fatalf("payload = %#v, want status=ok", payload)
	}
}

func TestHandleUploadRejectsPlainMultipartWhenE2EERequired(t *testing.T) {
	rootDir := t.TempDir()
	registry := fs.NewRegistry(filepath.Join(t.TempDir(), "registry.json"))
	root, err := registry.Upsert(rootDir)
	if err != nil {
		t.Fatalf("registry.Upsert: %v", err)
	}
	key := []byte("0123456789abcdef0123456789abcdef")
	clientID := "client-upload-plain"
	manager := e2ee.NewManager(e2ee.Config{Enabled: true, NodeID: "node", PairingSecret: "secret"})
	if _, err := manager.OpenSessionForClient(clientID, e2ee.DerivedKey{Transport: key}); err != nil {
		t.Fatalf("OpenSessionForClient: %v", err)
	}
	handler := &HTTPHandler{AppContext: &AppContext{Dirs: registry, E2EE: manager}}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("files", "secret.txt")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write([]byte("plaintext upload")); err != nil {
		t.Fatalf("multipart write: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("multipart close: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/upload?root="+url.QueryEscape(root.ID), &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	setE2EEProofHeaders(t, req, key, clientID)
	resp := httptest.NewRecorder()

	handler.handleUpload(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s, want 400", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "e2ee_upload_payload_required") {
		t.Fatalf("body = %s, want e2ee_upload_payload_required", resp.Body.String())
	}
	if _, err := os.Stat(filepath.Join(rootDir, "secret.txt")); !os.IsNotExist(err) {
		t.Fatalf("plaintext upload was saved; stat err=%v", err)
	}
}

func TestHandleUploadDecryptsProtectedPayloadWhenE2EERequired(t *testing.T) {
	rootDir := t.TempDir()
	registry := fs.NewRegistry(filepath.Join(t.TempDir(), "registry.json"))
	root, err := registry.Upsert(rootDir)
	if err != nil {
		t.Fatalf("registry.Upsert: %v", err)
	}
	key := []byte("0123456789abcdef0123456789abcdef")
	clientID := "client-upload-protected"
	manager := e2ee.NewManager(e2ee.Config{Enabled: true, NodeID: "node", PairingSecret: "secret"})
	if _, err := manager.OpenSessionForClient(clientID, e2ee.DerivedKey{Transport: key}); err != nil {
		t.Fatalf("OpenSessionForClient: %v", err)
	}
	envelope, err := e2ee.EncryptBytes(key, []byte("encrypted upload"))
	if err != nil {
		t.Fatalf("EncryptBytes: %v", err)
	}
	payload := protectedUploadRequest{
		Dir: "incoming",
		Files: []protectedUploadFile{{
			Name:        "secret.txt",
			ContentType: "text/plain",
			Envelope:    *envelope,
		}},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	handler := &HTTPHandler{AppContext: &AppContext{Dirs: registry, E2EE: manager}}
	req := httptest.NewRequest(http.MethodPost, "/api/upload?root="+url.QueryEscape(root.ID), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(e2eeHeaderName, "1")
	setE2EEProofHeaders(t, req, key, clientID)
	resp := httptest.NewRecorder()

	handler.handleUpload(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", resp.Code, resp.Body.String())
	}
	if strings.Contains(resp.Body.String(), "encrypted upload") || strings.Contains(string(body), "encrypted upload") {
		t.Fatal("protected upload leaked plaintext in request or response body")
	}
	if got := resp.Header().Get(e2eeHeaderName); got != "1" {
		t.Fatalf("%s = %q, want 1", e2eeHeaderName, got)
	}
	saved, err := os.ReadFile(filepath.Join(rootDir, "incoming", "secret.txt"))
	if err != nil {
		t.Fatalf("ReadFile saved upload: %v", err)
	}
	if string(saved) != "encrypted upload" {
		t.Fatalf("saved upload = %q, want encrypted upload", string(saved))
	}
}

func setE2EEProofHeaders(t *testing.T, req *http.Request, key []byte, clientID string) {
	t.Helper()
	ts := time.Now().UTC().Format(time.RFC3339)
	req.Header.Set(clientIDHeaderName, clientID)
	req.Header.Set(e2eeTSHeaderName, ts)
	req.Header.Set(e2eeProofHeaderName, e2ee.BuildRequestProof(key, req.Method, requestProofPath(req), ts, clientID))
}

func TestRelayStatusSessionAllowsPublicStatusWithoutE2EEHeader(t *testing.T) {
	handler := &HTTPHandler{AppContext: &AppContext{
		E2EE: e2ee.NewManager(e2ee.Config{Enabled: true, NodeID: "e2ee-node", PairingSecret: "secret"}),
	}}
	req := httptest.NewRequest(http.MethodGet, "/api/relay/status", nil)

	sess, err := handler.relayStatusSession(req)
	if err != nil {
		t.Fatalf("relayStatusSession() error = %v", err)
	}
	if sess != nil {
		t.Fatalf("relayStatusSession() = %+v, want nil public session", sess)
	}
}

func TestPublicRelayStatusRedactsSensitiveRelayFields(t *testing.T) {
	status := publicRelayStatus(relay.Status{
		Bound:        true,
		NoRelayer:    false,
		PendingCode:  "pc_secret",
		NodeName:     "node-name",
		NodeID:       "node-id",
		E2EENodeID:   "e2ee-node",
		RelayBaseURL: "https://relay.example.com",
		NodeURL:      "https://relay.example.com/n/node-id/",
		LastError:    "err",
		E2EERequired: true,
	})

	if !status.E2EERequired || status.E2EENodeID != "e2ee-node" {
		t.Fatalf("public E2EE fields = required:%v node:%q", status.E2EERequired, status.E2EENodeID)
	}
	if status.PendingCode != "" || status.NodeID != "" || status.NodeURL != "" || status.RelayBaseURL != "" || status.NodeName != "" || status.LastError != "" {
		t.Fatalf("public status leaked sensitive fields: %+v", status)

	}
}
