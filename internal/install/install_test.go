package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunningAsRoot(t *testing.T) {
	// Test real user ID check
	realUID := os.Getuid()
	expectedReal := (realUID == 0)

	if RunningAsRoot(true) != expectedReal {
		t.Errorf("RunningAsRoot(true) = %v, expected %v", RunningAsRoot(true), expectedReal)
	}

	// Test effective user ID check
	effectiveUID := os.Geteuid()
	expectedEffective := (effectiveUID == 0)

	if RunningAsRoot(false) != expectedEffective {
		t.Errorf("RunningAsRoot(false) = %v, expected %v", RunningAsRoot(false), expectedEffective)
	}
}

// Note: Most install package functions require root privileges and make system modifications,
// so comprehensive testing would require a test environment with elevated privileges.
// The functions are designed to be tested manually during actual installation/uninstallation.

// A missing config file is reported with a pointer to `glocker -init`, not a raw
// parse error. The check runs before any privileged step, so no root is needed.
func TestInstallGlocker_MissingConfigPointsToInit(t *testing.T) {
	err := InstallGlocker(filepath.Join(t.TempDir(), "conf.yaml"))
	if err == nil {
		t.Fatal("expected an error for a missing config")
	}
	if !strings.Contains(err.Error(), "-init") {
		t.Errorf("error should point at -init, got: %v", err)
	}
}
