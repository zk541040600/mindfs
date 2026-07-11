package githubimport

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mindfs/server/internal/fs"
)

func TestParseGitHubRepoURLRejectsDotSegments(t *testing.T) {
	tests := []string{
		"https://github.com/owner/.",
		"https://github.com/owner/..",
		"https://github.com/./repo",
		"https://github.com/../repo",
	}
	for _, raw := range tests {
		t.Run(raw, func(t *testing.T) {
			if _, _, _, err := parseGitHubRepoURL(raw); err == nil {
				t.Fatalf("parseGitHubRepoURL(%q) error = nil, want invalid url", raw)
			}
		})
	}
}

func TestSanitizeNameFallsBackForDotSegments(t *testing.T) {
	if got := sanitizeName(".."); got != "repo" {
		t.Fatalf("sanitizeName(..) = %q, want repo", got)
	}
	if got := sanitizeName("."); got != "repo" {
		t.Fatalf("sanitizeName(.) = %q, want repo", got)
	}
}

func TestRunCloneFailureDoesNotRemoveConcurrentTargetDirectory(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "repo")

	oldCloneRepository := cloneRepository
	cloneRepository = func(ctx context.Context, repoURL, targetPath string) error {
		if !strings.HasPrefix(filepath.Base(targetPath), ".repo-import-") {
			t.Fatalf("clone target = %q, want hidden temporary clone dir", targetPath)
		}
		if err := os.WriteFile(filepath.Join(target, "keep.txt"), []byte("external"), 0o644); err != nil {
			t.Fatalf("write concurrent target file: %v", err)
		}
		return errors.New("clone failed")
	}
	t.Cleanup(func() { cloneRepository = oldCloneRepository })

	svc, err := NewService(fakeRegistrar{})
	if err != nil {
		t.Fatal(err)
	}
	status := Status{TaskID: "task-1", URL: "https://github.com/owner/repo", Status: "pending"}
	svc.setStatus(status)
	svc.run(context.Background(), status, parent)

	payload, err := os.ReadFile(filepath.Join(target, "keep.txt"))
	if err != nil {
		t.Fatalf("concurrent target directory was removed: %v", err)
	}
	if string(payload) != "external" {
		t.Fatalf("concurrent target content = %q, want external", string(payload))
	}
	if matches, err := filepath.Glob(filepath.Join(parent, ".repo-import-*")); err != nil {
		t.Fatal(err)
	} else if len(matches) != 0 {
		t.Fatalf("temporary clone directories remain: %#v", matches)
	}
}

type fakeRegistrar struct{}

func (fakeRegistrar) UpsertRoot(path string) (fs.RootInfo, error) {
	return fs.RootInfo{ID: "root", RootPath: path}, nil
}
