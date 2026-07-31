package logging

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCleanupStaleFileBodySourcesRemovesOnlyStaleParts(t *testing.T) {
	baseDir := t.TempDir()
	now := time.Now()
	staleDir := filepath.Join(baseDir, "request-log-parts-api-request-stale")
	freshDir := filepath.Join(baseDir, "request-log-parts-api-request-fresh")
	unrelatedDir := filepath.Join(baseDir, "unrelated-log-dir")
	for _, dir := range []string{staleDir, freshDir, unrelatedDir} {
		if errMkdir := os.MkdirAll(dir, 0755); errMkdir != nil {
			t.Fatalf("MkdirAll(%q): %v", dir, errMkdir)
		}
	}
	old := now.Add(-(staleFileBodySourceMaxAge + time.Hour))
	if errChtimes := os.Chtimes(staleDir, old, old); errChtimes != nil {
		t.Fatalf("Chtimes(%q): %v", staleDir, errChtimes)
	}

	NewFileRequestLogger(false, baseDir, "", 0)
	if _, errStat := os.Stat(staleDir); !os.IsNotExist(errStat) {
		t.Fatalf("stale directory still exists, stat error = %v", errStat)
	}
	for _, dir := range []string{freshDir, unrelatedDir} {
		if _, errStat := os.Stat(dir); errStat != nil {
			t.Fatalf("directory %q was removed or unreadable: %v", dir, errStat)
		}
	}
}
