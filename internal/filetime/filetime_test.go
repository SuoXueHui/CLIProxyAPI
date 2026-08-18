package filetime

import (
	"os"
	"testing"
	"time"
)

func TestCreationTimeDoesNotFollowModifiedTime(t *testing.T) {
	path := t.TempDir() + "/auth.json"
	if err := os.WriteFile(path, []byte(`{"type":"codex"}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat auth file: %v", err)
	}
	created := CreationTime(path)
	if created.IsZero() {
		t.Skip("filesystem does not expose a creation time")
	}

	future := time.Now().Add(24 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatalf("change auth file mtime: %v", err)
	}
	updatedInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("restat auth file: %v", err)
	}
	updatedCreated := CreationTime(path)
	if updatedCreated.IsZero() {
		t.Fatal("creation time disappeared after mtime update")
	}
	if !updatedCreated.Equal(created) {
		t.Fatalf("creation time changed from %v to %v after mtime update", created, updatedCreated)
	}
	if !updatedInfo.ModTime().Equal(future) {
		t.Fatalf("mtime = %v, want %v", updatedInfo.ModTime(), future)
	}
}
