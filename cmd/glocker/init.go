package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
)

// runInit writes a starting config from the commented sample, filling in the
// values people most often miss — the login to restrict/track (the top-level
// `user`), and optionally the hosted-dashboard URL/token and an accountability
// buddy email. It copies the sample verbatim and substitutes only the values, so
// every comment in the sample survives. It refuses to overwrite an existing config.
func runInit(confPath string) error {
	return initConfig(confPath, os.Stdin, stdinIsTTY())
}

// initConfig is the testable core: prompts are read from `in` and only asked when
// `interactive` is true.
func initConfig(confPath string, in io.Reader, interactive bool) error {
	samplePath := confPath + ".sample"
	if _, err := os.Stat(confPath); err == nil {
		return fmt.Errorf("%s already exists — remove or rename it first (init won't overwrite your config)", confPath)
	}
	raw, err := os.ReadFile(samplePath)
	if err != nil {
		return fmt.Errorf("reading sample %s: %w", samplePath, err)
	}
	out := string(raw)

	login := detectLogin()
	r := bufio.NewReader(in)
	if interactive {
		login = ask(r, fmt.Sprintf("Your Linux login (whose sudo is restricted + desktop tracked) [%s]: ", login), login)
		if ans := ask(r, "Use a hosted glockpeek dashboard? [y/N]: ", "n"); strings.HasPrefix(strings.ToLower(ans), "y") {
			url := ask(r, "  glockpeek URL [https://my.glockerapp.com]: ", "https://my.glockerapp.com")
			out = replaceScalar(out, "glockpeek_url", url)
			if tok := ask(r, "  ingest token (blank to add later): ", ""); tok != "" {
				out = replaceScalar(out, "token", tok)
			}
		}
		if email := ask(r, "Accountability buddy email (blank to skip): ", ""); email != "" {
			out = replaceList(out, "buddy_emails", email)
		}
	}
	if login == "" {
		return fmt.Errorf("could not determine your login and none was given — set the top-level `user` in %s by hand", confPath)
	}
	out = replaceScalar(out, "user", login)

	if err := os.WriteFile(confPath, []byte(out), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", confPath, err)
	}

	// Seed the personal blocklist from its sample so the !include resolves at
	// install and the user has a clear place for their own sites. Best-effort:
	// skipped when there's no sample (e.g. a minimal/standalone config layout).
	seedPersonalDomains(filepath.Dir(confPath))
	return nil
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
// sudo, else $USER, else the current user. Never "root" (glocker restricts a
// person's account, not root).
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

// replaceScalar swaps the quoted value of `key: "..."` in the sample text,
// preserving the key's indentation and any trailing inline comment. A value
// containing '$' is inserted literally (no regexp expansion).
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
