package utils

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCopyFile(t *testing.T) {
	// Create a temporary directory for testing
	tmpDir := t.TempDir()

	// Create a source file
	srcPath := filepath.Join(tmpDir, "source.txt")
	content := []byte("test content\n")
	if err := os.WriteFile(srcPath, content, 0644); err != nil {
		t.Fatalf("Failed to create source file: %v", err)
	}

	// Copy the file
	dstPath := filepath.Join(tmpDir, "destination.txt")
	if err := CopyFile(srcPath, dstPath); err != nil {
		t.Fatalf("CopyFile failed: %v", err)
	}

	// Verify destination exists
	if _, err := os.Stat(dstPath); os.IsNotExist(err) {
		t.Error("Destination file was not created")
	}

	// Verify content matches
	dstContent, err := os.ReadFile(dstPath)
	if err != nil {
		t.Fatalf("Failed to read destination file: %v", err)
	}
	if string(dstContent) != string(content) {
		t.Errorf("Content mismatch: got %q, want %q", dstContent, content)
	}

	// Verify permissions match
	srcInfo, _ := os.Stat(srcPath)
	dstInfo, _ := os.Stat(dstPath)
	if srcInfo.Mode() != dstInfo.Mode() {
		t.Errorf("Permissions mismatch: got %v, want %v", dstInfo.Mode(), srcInfo.Mode())
	}
}

func TestCopyFile_NonExistentSource(t *testing.T) {
	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "nonexistent.txt")
	dstPath := filepath.Join(tmpDir, "destination.txt")

	err := CopyFile(srcPath, dstPath)
	if err == nil {
		t.Error("Expected error when copying non-existent file")
	}
}

func TestCopyDir(t *testing.T) {
	tmpDir := t.TempDir()

	// Create source directory structure
	srcDir := filepath.Join(tmpDir, "source")
	if err := os.MkdirAll(filepath.Join(srcDir, "subdir"), 0755); err != nil {
		t.Fatalf("Failed to create source directory: %v", err)
	}

	// Create files in source directory
	file1 := filepath.Join(srcDir, "file1.txt")
	file2 := filepath.Join(srcDir, "subdir", "file2.txt")
	if err := os.WriteFile(file1, []byte("content1"), 0644); err != nil {
		t.Fatalf("Failed to create file1: %v", err)
	}
	if err := os.WriteFile(file2, []byte("content2"), 0644); err != nil {
		t.Fatalf("Failed to create file2: %v", err)
	}

	// Copy directory
	dstDir := filepath.Join(tmpDir, "destination")
	if err := CopyDir(srcDir, dstDir); err != nil {
		t.Fatalf("CopyDir failed: %v", err)
	}

	// Verify structure
	dstFile1 := filepath.Join(dstDir, "file1.txt")
	dstFile2 := filepath.Join(dstDir, "subdir", "file2.txt")

	if _, err := os.Stat(dstFile1); os.IsNotExist(err) {
		t.Error("file1.txt was not copied")
	}
	if _, err := os.Stat(dstFile2); os.IsNotExist(err) {
		t.Error("subdir/file2.txt was not copied")
	}

	// Verify contents
	content1, _ := os.ReadFile(dstFile1)
	if string(content1) != "content1" {
		t.Errorf("file1 content mismatch: got %q, want %q", content1, "content1")
	}

	content2, _ := os.ReadFile(dstFile2)
	if string(content2) != "content2" {
		t.Errorf("file2 content mismatch: got %q, want %q", content2, "content2")
	}
}

func TestRunningAsRoot(t *testing.T) {
	// Test both real and effective UID checks
	realRoot := RunningAsRoot(true)
	effectiveRoot := RunningAsRoot(false)

	// We can't assume the test is running as root or non-root
	// Just verify the function doesn't panic and returns a bool
	_ = realRoot
	_ = effectiveRoot

	// If running as root (UID 0), both should be true
	// If running as non-root with setuid, effective could be true while real is false
	// We just test that the function works
	t.Logf("Running as root (real): %v", realRoot)
	t.Logf("Running as root (effective): %v", effectiveRoot)
}

func TestReplaceFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	if err := os.WriteFile(src, []byte("new-content"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("OLD"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := ReplaceFile(src, dst); err != nil {
		t.Fatalf("ReplaceFile: %v", err)
	}

	// Content is replaced and src's mode is preserved.
	if b, _ := os.ReadFile(dst); string(b) != "new-content" {
		t.Errorf("dst content = %q, want new-content", b)
	}
	if fi, _ := os.Stat(dst); fi.Mode().Perm() != 0o755 {
		t.Errorf("dst mode = %v, want 0755", fi.Mode().Perm())
	}

	// No temp file left behind after a successful rename.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".dst.tmp-") {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}

func TestReplaceFileCreatesNewDestination(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "does-not-exist-yet")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ReplaceFile(src, dst); err != nil {
		t.Fatalf("ReplaceFile to new dst: %v", err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "x" {
		t.Errorf("dst not created correctly: %q", b)
	}
}

func TestJoinFileLinesTrailingNewline(t *testing.T) {
	cases := map[string][]string{
		"root ALL=(ALL) ALL\nme ALL=(ALL) ALL # M\n": {"root ALL=(ALL) ALL", "me ALL=(ALL) ALL # M"},
		"a\n\nb\n": {"a", "", "b"},
		"a\n":      {"a", ""},
	}
	for want, in := range cases {
		if got := JoinFileLines(in); got != want {
			t.Errorf("JoinFileLines(%q) = %q, want %q", in, got, want)
		}
	}
}
