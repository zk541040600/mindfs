package update

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestInstallPackageCopiesBridgeAssets(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows update uses detached PowerShell replacement process")
	}
	prefix := t.TempDir()
	exe := filepath.Join(prefix, "bin", "mindfs")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	pkgDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(pkgDir, "mindfs"), []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	bridgeDir := filepath.Join(pkgDir, "server", "internal", "agent", "pi_sdk_bridge")
	if err := os.MkdirAll(bridgeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bridgeDir, "probe.mjs"), []byte("// probe\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	svc := &Service{executable: exe}
	if err := svc.installPackage(pkgDir); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(prefix, "share", "mindfs", "server", "internal", "agent", "pi_sdk_bridge", "probe.mjs")
	got, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("expected installed bridge probe at %s: %v", want, err)
	}
	if string(got) != "// probe\n" {
		t.Fatalf("unexpected installed bridge probe content: %q", string(got))
	}
}

func TestParseReleaseNotesVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		text string
		want string
	}{
		{name: "tag heading", text: "# MindFS v0.2.3\n\n## Fixes\n", want: "v0.2.3"},
		{name: "sdk build heading", text: "# MindFS v0.3.4-sdk.1\n\n## Fixes\n", want: "v0.3.4-sdk.1"},
		{name: "version without prefix", text: "# MindFS 0.2.3\n", want: "0.2.3"},
		{name: "invalid heading", text: "# Latest v0.2.3\n", want: ""},
		{name: "empty", text: "", want: ""},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := parseReleaseNotesVersion(tt.text)
			if got != tt.want {
				t.Fatalf("parseReleaseNotesVersion(%q) = %q, want %q", tt.text, got, tt.want)
			}
		})
	}
}

func TestLatestReleaseNotesBody(t *testing.T) {
	t.Parallel()

	text := "# MindFS v0.2.3\n\n## 优化和修复\n- latest\n\n# MindFS v0.2.2\n\n## 修复\n- old\n"
	want := "# MindFS v0.2.3\n\n## 优化和修复\n- latest"
	if got := latestReleaseNotesBody(text); got != want {
		t.Fatalf("latestReleaseNotesBody() = %q, want %q", got, want)
	}
}

func TestIsNewerVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		latest  string
		current string
		want    bool
	}{
		{name: "higher patch", latest: "0.1.1", current: "0.1.0", want: true},
		{name: "lower patch", latest: "0.1.0", current: "0.1.1", want: false},
		{name: "same version", latest: "0.1.0", current: "0.1.0", want: false},
		{name: "prefixed tag", latest: "v0.2.0", current: "0.1.9", want: true},
		{name: "git describe current", latest: "0.1.0", current: "v0.1.0-2-gabc123", want: false},
		{name: "sdk build same official core", latest: "0.3.4", current: "v0.3.4-sdk.1", want: false},
		{name: "official update newer than sdk build core", latest: "0.3.5", current: "v0.3.4-sdk.1", want: true},
		{name: "invalid current treated as older", latest: "0.1.0", current: "dev", want: true},
		{name: "invalid latest ignored", latest: "dev", current: "0.1.0", want: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := isNewerVersion(tt.latest, tt.current)
			if got != tt.want {
				t.Fatalf("isNewerVersion(%q, %q) = %t, want %t", tt.latest, tt.current, got, tt.want)
			}
		})
	}
}
