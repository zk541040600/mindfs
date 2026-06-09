package api

import "testing"

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

func TestRewriteRelayedFrontendContentKeepsRelativeAssets(t *testing.T) {
	input := `<script type="module" src="./assets/index-app.js"></script>`
	got := rewriteRelayedFrontendContent(input)
	if got != input {
		t.Fatalf("rewriteRelayedFrontendContent() = %q, want %q", got, input)
	}
}
