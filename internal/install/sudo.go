package install

import (
	"bufio"
	"log"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"glocker/internal/config"
	"glocker/internal/sudoplan"
	"glocker/internal/utils"
)

// takeOverSudo makes glocker's sudoers.d drop-in the managed user's only sudo
// path (see sudoplan), so the allow windows actually restrict. When it can't,
// the sudo window stays off and sudo is left as it was; the rest of the install
// goes ahead, since blocking doesn't depend on it.
func takeOverSudo(cfg *config.Config) {
	if !cfg.Sudoers.Enabled {
		return
	}
	sys := sudoplan.Default()
	plan, id, ok := sudoplan.Detect(sys)
	if !ok {
		names := make([]string, len(sudoplan.Plans))
		for i, p := range sudoplan.Plans {
			names[i] = p.Name
		}
		log.Printf("⚠️  The sudo window isn't supported on %q (supported: %s). Sudo is left as it is.", id, strings.Join(names, ", "))
		releaseLegacyLine(cfg)
		return
	}
	actions, err := sudoplan.Survey(sys, plan, cfg.User)
	if err != nil {
		log.Printf("⚠️  Sudo window not enabled: %v", err)
		releaseLegacyLine(cfg)
		return
	}
	if actions.Empty() && sudoplan.Active(sys) {
		log.Println("✓ Sudo takeover already in place")
		return
	}

	log.Printf("Taking over %s's sudo access so the sudo window applies (%s):", cfg.User, plan.Name)
	for _, l := range actions.Describe(sys, cfg.User) {
		log.Printf("   %s", l)
	}
	log.Println("   glocker -uninstall puts these back.")
	if term.IsTerminal(int(os.Stdin.Fd())) && !confirm("Proceed? [Y/n] ") {
		log.Println("Skipped — sudo window not enabled.")
		return
	}

	line := cfg.Sudoers.BlockedSudoersLine
	if utils.InAnyWindow(cfg.Sudoers.AllowWindows, time.Now()) {
		line = cfg.Sudoers.AllowedSudoersLine
	}
	if err := sudoplan.Takeover(sys, plan, cfg.User, line, actions); err != nil {
		log.Printf("⚠️  Sudo takeover failed and was rolled back: %v", err)
		return
	}
	log.Println("✓ Sudo window active. Group changes apply from your next login.")
}

// releaseLegacyLine handles an upgrade where the sudo window can't be taken
// over: an older version's GLOCKER-MANAGED line in /etc/sudoers would otherwise
// stay frozen at whatever it last said, possibly the blocked line. It's set to
// the allowed line and left for the user to keep or remove.
func releaseLegacyLine(cfg *config.Config) {
	data, err := os.ReadFile(config.SudoersPath)
	if err != nil || !strings.Contains(string(data), config.SudoersMarker) {
		return
	}
	if err := replaceBlockedWithAllowed(cfg); err != nil {
		log.Printf("⚠️  Couldn't reset the old %s line in %s: %v", config.SudoersMarker, config.SudoersPath, err)
		return
	}
	log.Printf("   The old %s line in %s now holds the allowed line; remove it with visudo if you don't want it.", config.SudoersMarker, config.SudoersPath)
}

func confirm(prompt string) bool {
	os.Stdout.WriteString(prompt)
	ans, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	ans = strings.ToLower(strings.TrimSpace(ans))
	return ans == "" || strings.HasPrefix(ans, "y")
}
