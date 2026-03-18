package resource

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileMD5(t *testing.T) {
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "sample.txt")

	if err := os.WriteFile(filePath, []byte("hello world"), 0o644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	got, err := fileMD5(filePath)
	if err != nil {
		t.Fatalf("fileMD5 returned error: %v", err)
	}

	const want = "5eb63bbbe01eeed093cb22bb8f5acdc3"
	if got != want {
		t.Fatalf("fileMD5 = %q, want %q", got, want)
	}
}

func TestFileMD5MissingFile(t *testing.T) {
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "missing.txt")

	_, err := fileMD5(filePath)
	if err == nil {
		t.Fatal("fileMD5 returned nil error for missing file")
	}

	if !strings.Contains(err.Error(), "open") {
		t.Fatalf("fileMD5 error = %q, want message containing %q", err.Error(), "open")
	}
}
