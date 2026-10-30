package install

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestAddShellVersion(t *testing.T) {
	base := []byte(`{"uuid":"x","shell-version":["48","49"],"url":"u"}`)

	// A newer major is appended and the rest of the metadata is preserved.
	out, changed, err := addShellVersion(base, 50)
	if err != nil {
		t.Fatalf("addShellVersion: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true for a new major")
	}
	var meta map[string]any
	if err := json.Unmarshal(out, &meta); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if got := toStringList(meta["shell-version"]); !slices.Contains(got, "50") || !slices.Contains(got, "48") {
		t.Errorf("shell-version = %v, want 48 and 50 present", got)
	}
	if meta["uuid"] != "x" || meta["url"] != "u" {
		t.Errorf("other fields not preserved: %+v", meta)
	}

	// An already-covered major is a no-op (no rewrite churn).
	if _, changed, err := addShellVersion(base, 49); err != nil || changed {
		t.Errorf("addShellVersion(49) changed=%v err=%v, want false/nil", changed, err)
	}

	// Unknown version (0) never changes the list; handled by syncShellVersion,
	// but guard the pure function too.
	if _, changed, _ := addShellVersion(base, 0); changed {
		t.Error("addShellVersion(0) should not change the list")
	}
}

func TestAppendEnabledExtension(t *testing.T) {
	const uuid = "glocker-usage@glocker.app"
	cases := []struct {
		name    string
		cur     string
		want    string
		changed bool
	}{
		{"gvariant empty", "@as []", "['glocker-usage@glocker.app']", true},
		{"plain empty", "[]", "['glocker-usage@glocker.app']", true},
		{"blank", "", "['glocker-usage@glocker.app']", true},
		{"one existing other", "['foo@bar']", "['foo@bar', 'glocker-usage@glocker.app']", true},
		{"already present", "['foo@bar', 'glocker-usage@glocker.app']", "['foo@bar', 'glocker-usage@glocker.app']", false},
		{"already present alone", "['glocker-usage@glocker.app']", "['glocker-usage@glocker.app']", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, changed := appendEnabledExtension(c.cur, uuid)
			if got != c.want || changed != c.changed {
				t.Errorf("appendEnabledExtension(%q) = (%q, %v), want (%q, %v)", c.cur, got, changed, c.want, c.changed)
			}
		})
	}
}

func TestRemoveEnabledExtension(t *testing.T) {
	const uuid = "glocker-usage@glocker.app"
	cases := []struct {
		name    string
		cur     string
		want    string
		changed bool
	}{
		{"only one -> typed empty", "['glocker-usage@glocker.app']", "@as []", true},
		{"among others", "['foo@bar', 'glocker-usage@glocker.app', 'baz@qux']", "['foo@bar', 'baz@qux']", true},
		{"absent", "['foo@bar']", "['foo@bar']", false},
		{"empty", "@as []", "@as []", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, changed := removeEnabledExtension(c.cur, uuid)
			if got != c.want || changed != c.changed {
				t.Errorf("removeEnabledExtension(%q) = (%q, %v), want (%q, %v)", c.cur, got, changed, c.want, c.changed)
			}
		})
	}
}

func TestParseGnomeMajor(t *testing.T) {
	cases := map[string]int{
		"GNOME Shell 48.7\n": 48,
		"GNOME Shell 45.0":   45,
		"GNOME Shell 3.38.6": 3,
		"garbage":            0,
		"":                   0,
	}
	for in, want := range cases {
		if got := parseGnomeMajor(in); got != want {
			t.Errorf("parseGnomeMajor(%q) = %d, want %d", in, got, want)
		}
	}
}
