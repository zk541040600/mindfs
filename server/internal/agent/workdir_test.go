package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureStableWorkDirSanitizesPathSegments(t *testing.T) {
	kind := "workdir-test"
	base := filepath.Join(os.TempDir(), "mindfs-"+kind)
	t.Cleanup(func() { _ = os.RemoveAll(base) })

	got, err := EnsureStableWorkDir(kind, "../escape/agent")
	if err != nil {
		t.Fatalf("EnsureStableWorkDir returned error: %v", err)
	}
	baseWithSep := base + string(filepath.Separator)
	if got != base && !strings.HasPrefix(got, baseWithSep) {
		t.Fatalf("workdir %q escaped base %q", got, base)
	}
	if filepath.Base(got) != ".._escape_agent" {
		t.Fatalf("workdir basename = %q, want sanitized agent name", filepath.Base(got))
	}
	if !IsTemporaryWorkDir(got) {
		t.Fatalf("sanitized workdir should be recognized as temporary: %q", got)
	}
}

func TestEnsureStableWorkDirDefaultsTraversalOnlyName(t *testing.T) {
	kind := "workdir-test-default"
	base := filepath.Join(os.TempDir(), "mindfs-"+kind)
	t.Cleanup(func() { _ = os.RemoveAll(base) })

	got, err := EnsureStableWorkDir(kind, "..")
	if err != nil {
		t.Fatalf("EnsureStableWorkDir returned error: %v", err)
	}
	if filepath.Base(got) != "default" {
		t.Fatalf("workdir basename = %q, want default", filepath.Base(got))
	}
}
