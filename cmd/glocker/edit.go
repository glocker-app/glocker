package main

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"strings"

	"glocker/internal/config"
	"glocker/internal/editor"
)

// runEdit opens the installed config in the editor. Without root it's
// read-only; with root, a save is applied by restarting the daemon.
func runEdit() {
	opts := editor.Options{ReadOnly: os.Geteuid() != 0}
	if !opts.ReadOnly {
		opts.AfterSave = restartDaemon
	}
	if err := editor.Run(config.GlockerConfigFile, opts); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			log.Fatalf("%s doesn't exist — is glocker installed?", config.GlockerConfigFile)
		}
		if errors.Is(err, fs.ErrPermission) {
			log.Fatalf("can't read %s: run with sudo, or log out and back in if you were just added to the glocker group", config.GlockerConfigFile)
		}
		log.Fatal(err)
	}
}

func restartDaemon() error {
	out, err := exec.Command("systemctl", "restart", "glocker.service").CombinedOutput()
	if err != nil {
		return fmt.Errorf("restarting glocker failed: %v %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
