package ops

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBackupFilesListsBackupRunDirectories(t *testing.T) {
	destination := t.TempDir()
	older := filepath.Join(destination, "2026-07-16-030000")
	newer := filepath.Join(destination, "2026-07-17-030000")
	for _, path := range []string{older, newer} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(newer, "database.sql.gz"), []byte("backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Date(2026, 7, 16, 3, 0, 0, 0, time.UTC)
	newTime := oldTime.Add(24 * time.Hour)
	if err := os.Chtimes(older, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newer, newTime, newTime); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := BackupFiles(context.Background(), destination, &out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "backup run") || !strings.Contains(text, filepath.Base(older)) || !strings.Contains(text, filepath.Base(newer)) {
		t.Fatalf("backup inventory did not list run directories:\n%s", text)
	}
	if strings.Index(text, filepath.Base(newer)) > strings.Index(text, filepath.Base(older)) {
		t.Fatalf("backup runs are not newest first:\n%s", text)
	}
}

func TestBackupFilesReportsEmptyInventory(t *testing.T) {
	var out bytes.Buffer
	if err := BackupFiles(context.Background(), t.TempDir(), &out); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "No backup runs found") {
		t.Fatalf("empty inventory = %q", got)
	}
}
