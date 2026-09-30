package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"

	"glocker/internal/config"
)

// envProfile is what the wizard detects about the machine so it can fill
// environment-specific config values instead of leaving placeholders.
type envProfile struct {
	login      string // whose sudo is restricted + desktop tracked
	display    string // $DISPLAY (X11 sessions)
	xauthority string // $XAUTHORITY (X11 sessions)
	suspend    string // panic-mode suspend command for this system
}

func detectEnv() envProfile {
	e := envProfile{
		login:      detectLogin(),
		display:    os.Getenv("DISPLAY"),
		xauthority: os.Getenv("XAUTHORITY"),
		suspend:    "systemctl suspend",
	}
	// Fall back to pm-suspend only on a system without systemd.
	if _, err := exec.LookPath("systemctl"); err != nil {
		if _, err := exec.LookPath("pm-suspend"); err == nil {
			e.suspend = "pm-suspend"
		}
	}
	return e
}

// initResult tells the caller what the wizard did, so the next-steps message can
// speak to the actual choices.
type initResult struct {
	login         string
	sudoRestricted bool
	hosted        bool
}

func runInit(confPath string) (initResult, error) {
	return initConfig(confPath, os.Stdin, stdinIsTTY())
}

// initConfig writes a starting config from the commented sample, filling in the
// values that depend on the machine (login, desktop session, suspend command) and
// the user's answers (restrict sudo, hosted sync, buddy email). It copies the
// sample and edits values in place, so every comment survives, and refuses to
// overwrite an existing config.
func initConfig(confPath string, in io.Reader, interactive bool) (initResult, error) {
	var res initResult
	samplePath := confPath + ".sample"
	if _, err := os.Stat(confPath); err == nil {
		return res, fmt.Errorf("%s already exists — remove or rename it first (init won't overwrite your config)", confPath)
	}
	raw, err := os.ReadFile(samplePath)
	if err != nil {
		return res, fmt.Errorf("reading sample %s: %w", samplePath, err)
	}
	out := string(raw)

	env := detectEnv()
	res.sudoRestricted = true // the sample ships sudoers.enabled: true
	r := bufio.NewReader(in)
	if interactive {
		env.login = ask(r, fmt.Sprintf("Your Linux login (whose sudo is restricted + desktop tracked) [%s]: ", env.login), env.login)
		res.sudoRestricted = !strings.HasPrefix(strings.ToLower(
			ask(r, "Restrict sudo to a daily window? This is glocker's core enforcement. [Y/n]: ", "y")), "n")
		if strings.HasPrefix(strings.ToLower(ask(r, "Sync to a hosted glockpeek dashboard? [y/N]: ", "n")), "y") {
			url := ask(r, "  glockpeek URL [https://my.glockerapp.com]: ", "https://my.glockerapp.com")
			out = setStanzaScalar(out, "sync", "enabled", "true")
			out = replaceScalar(out, "glockpeek_url", url)
			if tok := ask(r, "  ingest token (blank to add later): ", ""); tok != "" {
				out = replaceScalar(out, "token", tok)
			}
			out = setStanzaScalar(out, "sync", "encrypt", "true") // never plaintext off-box
			res.hosted = true
		}
		if email := ask(r, "Accountability buddy email (blank to skip): ", ""); email != "" {
			out = setStanzaScalar(out, "accountability", "enabled", "true")
			out = replaceList(out, "buddy_emails", email)
		}
	}
	if env.login == "" {
		return res, fmt.Errorf("could not determine your login and none was given — set the top-level `user` in %s by hand", confPath)
	}
	res.login = env.login

	// The login appears in several places — the top-level `user`, both sudoers
	// lines, the glocklock violation command, the nudge examples. Fill them all.
	out = strings.ReplaceAll(out, config.PlaceholderUser, env.login)

	if !res.sudoRestricted {
		out = setStanzaScalar(out, "sudoers", "enabled", "false")
	}

	// Desktop session, X11 only (on Wayland the tools find the session themselves,
	// so these are ignored — leave the sample's harmless defaults there).
	if env.display != "" {
		out = replaceScalar(out, "display", env.display)
	}
	if env.xauthority != "" {
		out = replaceScalar(out, "xauthority", env.xauthority)
	}

	// Panic suspend command, if this system needs the pm-suspend fallback.
	if env.suspend != "systemctl suspend" {
		out = strings.ReplaceAll(out, `"systemctl suspend"`, `"`+env.suspend+`"`)
	}

	if err := os.WriteFile(confPath, []byte(out), 0o644); err != nil {
		return res, fmt.Errorf("writing %s: %w", confPath, err)
	}
	seedPersonalDomains(filepath.Dir(confPath))
	return res, nil
}

// seedPersonalDomains copies conf.d/personal-domains.yaml.sample to
// conf.d/personal-domains.yaml when the latter is absent, so the user has an
// editable, update_domains.py-safe file for their own blocked sites.
func seedPersonalDomains(confDir string) {
	live := filepath.Join(confDir, "conf.d", "personal-domains.yaml")
	if _, err := os.Stat(live); err == nil {
		return // keep the user's existing file
	}
	data, err := os.ReadFile(filepath.Join(confDir, "conf.d", "personal-domains.yaml.sample"))
	if err != nil {
		return // no sample to seed from
	}
	_ = os.MkdirAll(filepath.Dir(live), 0o755)
	_ = os.WriteFile(live, data, 0o644)
}

// detectLogin picks the human user to restrict/track: SUDO_USER when run under
// sudo, else $USER, else the current user. Never "root".
func detectLogin() string {
	if u := os.Getenv("SUDO_USER"); u != "" && u != "root" {
		return u
	}
	if u := os.Getenv("USER"); u != "" && u != "root" {
		return u
	}
	if u, err := user.Current(); err == nil && u.Username != "" && u.Username != "root" {
		return u.Username
	}
	return ""
}

func stdinIsTTY() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// ask prints label, reads a line, and returns it trimmed — or def when the reply
// is empty (or input is closed).
func ask(r *bufio.Reader, label, def string) string {
	fmt.Print(label)
	line, err := r.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" || err != nil {
		return def
	}
	return line
}

// replaceScalar swaps the quoted value of a unique `key: "..."`, preserving the
// key's indentation and any trailing inline comment. A '$' in the value is
// inserted literally (no regexp expansion).
func replaceScalar(text, key, val string) string {
	re := regexp.MustCompile(`(?m)^(\s*` + regexp.QuoteMeta(key) + `:\s*)"[^"]*"`)
	return re.ReplaceAllStringFunc(text, func(m string) string {
		return re.FindStringSubmatch(m)[1] + `"` + val + `"`
	})
}

// replaceList swaps an empty `key: []` for a single-element list `key: ["val"]`,
// preserving indentation and any trailing inline comment.
func replaceList(text, key, val string) string {
	re := regexp.MustCompile(`(?m)^(\s*` + regexp.QuoteMeta(key) + `:\s*)\[\]`)
	return re.ReplaceAllStringFunc(text, func(m string) string {
		return re.FindStringSubmatch(m)[1] + `["` + val + `"]`
	})
}

// setStanzaScalar sets `key: value` inside a top-level `stanza:` block, leaving
// the same key in other stanzas untouched (needed for keys like `enabled` that
// recur). rawValue is written verbatim — quote it yourself for a string. Preserves
// indentation and any trailing comment.
func setStanzaScalar(text, stanza, key, rawValue string) string {
	lines := strings.Split(text, "\n")
	stanzaRe := regexp.MustCompile(`^` + regexp.QuoteMeta(stanza) + `:`)
	keyRe := regexp.MustCompile(`^(\s+` + regexp.QuoteMeta(key) + `:\s*)(?:\[\]|"[^"]*"|\S+)(.*)$`)
	in := false
	for i, ln := range lines {
		if stanzaRe.MatchString(ln) {
			in = true
			continue
		}
		if !in {
			continue
		}
		if ln != "" && ln[0] != ' ' && ln[0] != '\t' { // next top-level line ends the stanza
			in = false
			continue
		}
		if m := keyRe.FindStringSubmatch(ln); m != nil {
			lines[i] = m[1] + rawValue + m[2]
			break
		}
	}
	return strings.Join(lines, "\n")
}
