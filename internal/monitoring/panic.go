package monitoring

import (
	"log"
	"os/exec"
	"strings"
	"time"

	"glocker/internal/config"
	"glocker/internal/state"
)

// MonitorPanicMode enforces panic mode: while the panic deadline (set by
// -panic) is in the future, it keeps the machine suspended via cfg.Panic.Command,
// re-suspending on an early wake. A 5-second grace after each suspend lets a
// normal resume settle before it suspends again.
func MonitorPanicMode(cfg *config.Config) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		now := time.Now()
		panicUntil := state.GetPanicUntil()
		// Compare Unix seconds, not the monotonic clock — it pauses across suspend.
		if panicUntil.IsZero() || now.Unix() >= panicUntil.Unix() {
			continue
		}
		if last := state.GetLastSuspendTime(); !last.IsZero() && now.Unix()-last.Unix() < 5 {
			continue // grace period after a suspend
		}
		if cfg.Panic.Command != "" {
			ExecuteSuspendCommand(cfg.Panic.Command)
			state.SetLastSuspendTime(now)
		}
	}
}

// ExecuteSuspendCommand runs the configured panic/suspend command.
func ExecuteSuspendCommand(command string) {
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return
	}
	if err := exec.Command(parts[0], parts[1:]...).Run(); err != nil {
		log.Printf("panic: suspend command %q failed: %v", command, err)
	}
}
