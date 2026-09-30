package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReplaceScalar(t *testing.T) {
	src := "user: \"yourusername\"   # your login\nother: 1\n"
	got := replaceScalar(src, "user", "noufal")
	if !strings.Contains(got, `user: "noufal"   # your login`) {
		t.Errorf("scalar replace lost the value or comment: %q", got)
	}
	if got := replaceScalar(`token: ""`, "token", "polar_oat_$1"); !strings.Contains(got, `token: "polar_oat_$1"`) {
		t.Errorf("dollar in value mangled: %q", got)
	}
}

func TestReplaceList(t *testing.T) {
	got := replaceList("  buddy_emails: []    # who\n", "buddy_emails", "a@b.com")
	if !strings.Contains(got, `buddy_emails: ["a@b.com"]    # who`) {
		t.Errorf("list replace wrong: %q", got)
	}
}

func TestSetStanzaScalar(t *testing.T) {
	src := "a:\n  enabled: true\n  x: 1\nb:\n  enabled: true   # keep me\n"
	got := setStanzaScalar(src, "b", "enabled", "false")
	if !strings.Contains(got, "a:\n  enabled: true") {
		t.Errorf("stanza a should be untouched:\n%s", got)
	}
	if !strings.Contains(got, "b:\n  enabled: false   # keep me") {
		t.Errorf("stanza b enabled not set (or comment lost):\n%s", got)
	}
}

func TestInitConfig(t *testing.T) {
	t.Setenv("SUDO_USER", "")
	t.Setenv("USER", "testlogin")
	t.Setenv("DISPLAY", "")
	t.Setenv("XAUTHORITY", "")
	dir := t.TempDir()
	conf := filepath.Join(dir, "conf.yaml")
	sample := "# a comment\nuser: \"yourusername\"\nsudoers:\n  enabled: true\n  allowed_sudoers_line: \"yourusername ALL=(ALL) NOPASSWD:ALL\"\nviolation_tracking:\n  command: \"sudo -u yourusername /usr/local/bin/glocklock\"\n"
	if err := os.WriteFile(conf+".sample", []byte(sample), 0o644); err != nil {
		t.Fatal(err)
	}

	// Non-interactive: login fills the top-level user AND the sudoers/command lines.
	res, err := initConfig(conf, strings.NewReader(""), false)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if res.login != "testlogin" {
		t.Errorf("login = %q, want testlogin", res.login)
	}
	out, _ := os.ReadFile(conf)
	s := string(out)
	for _, want := range []string{
		`user: "testlogin"`,
		`testlogin ALL=(ALL) NOPASSWD:ALL`,
		`sudo -u testlogin /usr/local/bin/glocklock`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	if strings.Contains(s, "yourusername") {
		t.Errorf("placeholder left behind:\n%s", s)
	}
	if !strings.Contains(s, "# a comment") {
		t.Errorf("comments not preserved:\n%s", s)
	}

	// Refuses to overwrite.
	if _, err := initConfig(conf, strings.NewReader(""), false); err == nil {
		t.Error("expected refusal to overwrite existing config")
	}
}

func TestInitConfigInteractive(t *testing.T) {
	t.Setenv("DISPLAY", "")
	t.Setenv("XAUTHORITY", "")
	dir := t.TempDir()
	conf := filepath.Join(dir, "conf.yaml")
	sample := "user: \"yourusername\"\n" +
		"sudoers:\n  enabled: true\n" +
		"sync:\n  enabled: false\n  glockpeek_url: \"http://127.0.0.1:4317\"\n  token: \"\"\n  encrypt: false\n" +
		"accountability:\n  enabled: false\n  buddy_emails: []\n"
	if err := os.WriteFile(conf+".sample", []byte(sample), 0o644); err != nil {
		t.Fatal(err)
	}
	// answers: login, restrict-sudo (n), hosted (y), url, token, buddy email
	answers := "alice\nn\ny\nhttps://my.glockerapp.com\ntok_123\nbuddy@x.com\n"
	res, err := initConfig(conf, strings.NewReader(answers), true)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if !res.hosted || res.sudoRestricted {
		t.Errorf("result = %+v, want hosted=true sudoRestricted=false", res)
	}
	s := string(mustRead(t, conf))
	for _, want := range []string{
		`user: "alice"`,
		"sudoers:\n  enabled: false",
		"sync:\n  enabled: true",
		`glockpeek_url: "https://my.glockerapp.com"`,
		`token: "tok_123"`,
		"encrypt: true",
		"accountability:\n  enabled: true",
		`buddy_emails: ["buddy@x.com"]`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
