// Command loadprobe is a throwaway smoke test for the conf.d/ + !include split:
// it loads a config through the new include-aware parser (config.LoadFile) and
// prints a summary, so you can eyeball that !include directives resolved and the
// big blocklists came through. On a bad/corrupt include it prints the loud error
// the daemon would see (and exits non-zero) rather than a partial config.
//
//	go run ./cmd/loadprobe [path]     # default: conf/conf.yaml
//
// Delete this whenever; it's not part of the build.
package main

import (
	"fmt"
	"os"
	"time"

	"glocker/internal/config"
)

func main() {
	path := "conf/conf.yaml"
	if len(os.Args) > 1 {
		path = os.Args[1]
	}

	fmt.Printf("Loading %s via config.LoadFile (resolves !include)…\n\n", path)
	start := time.Now()
	cfg, err := config.LoadFile(path)
	elapsed := time.Since(start)

	if err != nil {
		fmt.Printf("FAILED after %s — this is the loud error the daemon would refuse on:\n\n  %v\n", elapsed, err)
		os.Exit(1)
	}

	fmt.Printf("OK in %s\n\n", elapsed)
	fmt.Printf("  domains:            %d\n", len(cfg.Domains))
	if n := len(cfg.Domains); n > 0 {
		fmt.Printf("    first:            %s\n", cfg.Domains[0].Name)
		fmt.Printf("    last:             %s\n", cfg.Domains[n-1].Name)
	}
	fmt.Printf("  forbidden programs: %d\n", len(cfg.ForbiddenPrograms.Programs))
	fmt.Printf("  enable_hosts:       %v\n", cfg.EnableHosts)
	fmt.Printf("  sudoers enabled:    %v (user %q)\n", cfg.Sudoers.Enabled, cfg.Sudoers.User)
	fmt.Printf("  sync url:           %s\n", cfg.Sync.GlockpeekURL)
}
