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
	// A value with a '$' is inserted literally, not treated as a regexp group.
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

func TestInitConfig(t *testing.T) {
	t.Setenv("SUDO_USER", "")
	t.Setenv("USER", "testlogin")
	dir := t.TempDir()
	conf := filepath.Join(dir, "conf.yaml")
	sample := conf + ".sample"
	if err := os.WriteFile(sample, []byte("# a comment\nuser: \"yourusername\"\nbuddy_emails: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Non-interactive: fills the detected login, keeps comments, no prompts read.
	if err := initConfig(conf, strings.NewReader(""), false); err != nil {
		t.Fatalf("init: %v", err)
	}
	out, _ := os.ReadFile(conf)
	if !strings.Contains(string(out), `user: "testlogin"`) {
		t.Errorf("login not substituted: %s", out)
	}
	if strings.Contains(string(out), "yourusername") {
		t.Errorf("placeholder left behind: %s", out)
	}
	if !strings.Contains(string(out), "# a comment") {
		t.Errorf("comments not preserved: %s", out)
	}

	// Refuses to overwrite an existing config.
	if err := initConfig(conf, strings.NewReader(""), false); err == nil {
		t.Error("expected refusal to overwrite existing config")
	}
}

func TestInitConfigInteractive(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "conf.yaml")
	if err := os.WriteFile(conf+".sample", []byte("user: \"yourusername\"\nsync:\n  glockpeek_url: \"http://127.0.0.1:4317\"\n  token: \"\"\naccountability:\n  buddy_emails: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Answers: login, hosted? yes, URL, token, buddy email.
	answers := "alice\ny\nhttps://my.glockerapp.com\ntok_123\nbuddy@x.com\n"
	if err := initConfig(conf, strings.NewReader(answers), true); err != nil {
		t.Fatalf("init: %v", err)
	}
	out, _ := os.ReadFile(conf)
	for _, want := range []string{`user: "alice"`, `glockpeek_url: "https://my.glockerapp.com"`, `token: "tok_123"`, `buddy_emails: ["buddy@x.com"]`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}
