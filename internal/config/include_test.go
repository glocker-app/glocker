package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeCfg(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func domainNames(cfg *Config) []string {
	out := []string{}
	for _, d := range cfg.Domains.List {
		out = append(out, d.Name)
	}
	return out
}

func TestIncludeSingleFileValue(t *testing.T) {
	dir := t.TempDir()
	writeCfg(t, filepath.Join(dir, "config.yaml"), "log_level: debug\ndomains:\n  list: !include conf.d/domains.yaml\n")
	writeCfg(t, filepath.Join(dir, "conf.d/domains.yaml"), "- name: a.com\n- name: b.com\n")

	cfg, err := LoadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("log_level = %q, want debug", cfg.LogLevel)
	}
	if got, want := domainNames(cfg), []string{"a.com", "b.com"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("domains = %v, want %v", got, want)
	}
}

func TestIncludeFlattenedIntoSequence(t *testing.T) {
	// The form the generator will emit: several list files concatenated.
	dir := t.TempDir()
	writeCfg(t, filepath.Join(dir, "config.yaml"),
		"domains:\n  list:\n    - !include conf.d/a.yaml\n    - !include conf.d/b.yaml\n")
	writeCfg(t, filepath.Join(dir, "conf.d/a.yaml"), "- name: a1.com\n- name: a2.com\n")
	writeCfg(t, filepath.Join(dir, "conf.d/b.yaml"), "- name: b1.com\n")

	cfg, err := LoadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := domainNames(cfg), []string{"a1.com", "a2.com", "b1.com"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("domains = %v, want %v", got, want)
	}
}

func TestIncludeEmptyDropsFromSequence(t *testing.T) {
	dir := t.TempDir()
	writeCfg(t, filepath.Join(dir, "config.yaml"),
		"domains:\n  list:\n    - !include conf.d/a.yaml\n    - !include conf.d/empty.yaml\n")
	writeCfg(t, filepath.Join(dir, "conf.d/a.yaml"), "- name: a.com\n")
	writeCfg(t, filepath.Join(dir, "conf.d/empty.yaml"), "")

	cfg, err := LoadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := domainNames(cfg), []string{"a.com"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("domains = %v, want %v", got, want)
	}
}

func TestIncludeNestedResolvesRelative(t *testing.T) {
	dir := t.TempDir()
	writeCfg(t, filepath.Join(dir, "config.yaml"), "domains:\n  list: !include conf.d/top.yaml\n")
	writeCfg(t, filepath.Join(dir, "conf.d/top.yaml"), "- !include nested/x.yaml\n- name: direct.com\n")
	writeCfg(t, filepath.Join(dir, "conf.d/nested/x.yaml"), "- name: nested.com\n")

	cfg, err := LoadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := domainNames(cfg), []string{"nested.com", "direct.com"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("domains = %v, want %v", got, want)
	}
}

func TestIncludeMissingFileErrors(t *testing.T) {
	dir := t.TempDir()
	writeCfg(t, filepath.Join(dir, "config.yaml"), "domains:\n  list: !include conf.d/nope.yaml\n")
	if _, err := LoadFile(filepath.Join(dir, "config.yaml")); err == nil {
		t.Fatal("expected an error for a missing include target")
	}
}

func TestNoIncludeIsBackwardCompatible(t *testing.T) {
	dir := t.TempDir()
	writeCfg(t, filepath.Join(dir, "config.yaml"), "log_level: info\ndomains:\n  list:\n    - name: plain.com\n")
	cfg, err := LoadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LogLevel != "info" || len(cfg.Domains.List) != 1 || cfg.Domains.List[0].Name != "plain.com" {
		t.Fatalf("cfg = %+v", cfg)
	}
}
