package agent

import (
	"os"
	"path/filepath"
	"strings"
)

func EnsureStableWorkDir(kind, agentName string) (string, error) {
	base := filepath.Join(os.TempDir(), "mindfs-"+stableWorkDirName(kind))
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(base, stableWorkDirName(agentName))
	if err := os.MkdirAll(path, 0o755); err != nil {
		return "", err
	}
	return path, nil
}

func stableWorkDirName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "default"
	}
	name = strings.NewReplacer("/", "_", "\\", "_").Replace(name)
	if name == "." || name == ".." {
		return "default"
	}
	return name
}

func IsTemporaryWorkDir(path string) bool {
	normalizedPath := NormalizeComparablePath(path)
	normalizedTemp := NormalizeComparablePath(os.TempDir())
	if normalizedPath == "" || normalizedTemp == "" {
		return false
	}
	rel, err := filepath.Rel(normalizedTemp, normalizedPath)
	if err != nil || rel == ".." || filepath.IsAbs(rel) {
		return false
	}
	return rel == "." || !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
