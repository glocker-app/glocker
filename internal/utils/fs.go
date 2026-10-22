package utils

import (
	"os"
	"path/filepath"
	"strings"
)

// CopyFile copies a single file from src to dst, preserving permissions.
func CopyFile(src, dst string) error {
	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	destFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destFile.Close()

	_, err = destFile.ReadFrom(sourceFile)
	if err != nil {
		return err
	}

	// Copy file permissions
	sourceInfo, err := os.Stat(src)
	if err != nil {
		return err
	}

	return os.Chmod(dst, sourceInfo.Mode())
}

// ReplaceFile atomically installs src's contents at dst, preserving src's
// permissions. It writes to a temp file in dst's directory and renames it over
// dst, so it succeeds even when dst is a currently-running executable — a plain
// open-for-write (as CopyFile does) fails there with ETXTBSY ("text file
// busy"). The rename swaps the directory entry; any running process keeps the
// old inode. The temp file lives in dst's dir so the rename stays on one
// filesystem (a cross-device rename would fail).
func ReplaceFile(src, dst string) error {
	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	sourceInfo, err := sourceFile.Stat()
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // best-effort cleanup if we bail before the rename

	if _, err := tmp.ReadFrom(sourceFile); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(sourceInfo.Mode()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, dst)
}

// CopyDir recursively copies a directory from src to dst.
func CopyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Calculate destination path
		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		dstPath := filepath.Join(dst, relPath)

		if info.IsDir() {
			return os.MkdirAll(dstPath, info.Mode())
		}

		return CopyFile(path, dstPath)
	})
}

// RunningAsRoot checks if the program is running with root privileges.
// If real is true, checks the real user ID (who ran the command).
// If real is false, checks the effective user ID (current privileges, affected by setuid).
func RunningAsRoot(real bool) bool {
	var uid int
	if real {
		uid = os.Getuid() // Real user ID - who actually ran the command
	} else {
		uid = os.Geteuid() // Effective user ID - current privileges (affected by setuid)
	}
	return uid == 0
}

// JoinFileLines joins lines with "\n" and guarantees a trailing newline.
// sudo-rs's visudo (Ubuntu's default sudo since 25.10) rejects a sudoers file
// whose last line has no terminator; classic sudo tolerates it.
func JoinFileLines(lines []string) string {
	s := strings.Join(lines, "\n")
	if !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	return s
}
