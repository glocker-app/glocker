package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The invariant these tests protect: a bad !include must fail LOUD (non-nil
// error, nil Config) and never silently yield a partial/empty config — because
// for a blocker, silently dropping the blocklist means failing open.

func mustErrNilCfg(t *testing.T, cfg *Config, err error, what string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected an error, got nil (cfg=%+v)", what, cfg)
	}
	if cfg != nil {
		t.Fatalf("%s: config must be nil on error, got %+v", what, cfg)
	}
}

func TestIncludeCycleErrorsNotHang(t *testing.T) {
	dir := t.TempDir()
	writeCfg(t, filepath.Join(dir, "config.yaml"), "domains: !include conf.d/a.yaml\n")
	writeCfg(t, filepath.Join(dir, "conf.d/a.yaml"), "- !include b.yaml\n")
	writeCfg(t, filepath.Join(dir, "conf.d/b.yaml"), "- !include a.yaml\n")
	// If the depth guard were missing this would hang and the test would time out.
	cfg, err := LoadFile(filepath.Join(dir, "config.yaml"))
	mustErrNilCfg(t, cfg, err, "a<->b cycle")
}

func TestIncludeSelfCycleErrors(t *testing.T) {
	dir := t.TempDir()
	writeCfg(t, filepath.Join(dir, "config.yaml"), "domains: !include conf.d/a.yaml\n")
	writeCfg(t, filepath.Join(dir, "conf.d/a.yaml"), "- !include a.yaml\n")
	cfg, err := LoadFile(filepath.Join(dir, "config.yaml"))
	mustErrNilCfg(t, cfg, err, "self cycle")
}

func TestIncludeMissingTargetErrors(t *testing.T) {
	dir := t.TempDir()
	writeCfg(t, filepath.Join(dir, "config.yaml"), "domains: !include conf.d/gone.yaml\n")
	cfg, err := LoadFile(filepath.Join(dir, "config.yaml"))
	mustErrNilCfg(t, cfg, err, "missing include")
}

func TestIncludeMalformedIncludedYAMLErrors(t *testing.T) {
	dir := t.TempDir()
	writeCfg(t, filepath.Join(dir, "config.yaml"), "domains: !include conf.d/bad.yaml\n")
	writeCfg(t, filepath.Join(dir, "conf.d/bad.yaml"), "- name: ok\n  bad: : : garbage\n:::\n")
	cfg, err := LoadFile(filepath.Join(dir, "config.yaml"))
	mustErrNilCfg(t, cfg, err, "malformed included yaml")
}

func TestIncludeMalformedMainYAMLErrors(t *testing.T) {
	dir := t.TempDir()
	writeCfg(t, filepath.Join(dir, "config.yaml"), ":::\n  - not: valid: yaml\n:::\n")
	cfg, err := LoadFile(filepath.Join(dir, "config.yaml"))
	mustErrNilCfg(t, cfg, err, "malformed main yaml")
}

func TestIncludeMappingIntoListErrors(t *testing.T) {
	// A mapping spliced into the domains list must error, NOT decode to a blank
	// domain. This is the critical silent-corruption guard.
	dir := t.TempDir()
	writeCfg(t, filepath.Join(dir, "config.yaml"),
		"domains:\n  - !include conf.d/map.yaml\n")
	writeCfg(t, filepath.Join(dir, "conf.d/map.yaml"), "foo: bar\nbaz: qux\n")
	cfg, err := LoadFile(filepath.Join(dir, "config.yaml"))
	mustErrNilCfg(t, cfg, err, "mapping into list")
	if err != nil && !strings.Contains(err.Error(), "must contain a list") {
		t.Fatalf("want a 'must contain a list' error, got: %v", err)
	}
}

func TestIncludeScalarIntoListErrors(t *testing.T) {
	dir := t.TempDir()
	writeCfg(t, filepath.Join(dir, "config.yaml"),
		"domains:\n  - !include conf.d/scalar.yaml\n")
	writeCfg(t, filepath.Join(dir, "conf.d/scalar.yaml"), "just a bare string\n")
	cfg, err := LoadFile(filepath.Join(dir, "config.yaml"))
	mustErrNilCfg(t, cfg, err, "scalar into list")
}

func TestIncludeWrongTypeForValueErrors(t *testing.T) {
	// `domains.list: !include x` where x is a mapping -> decode of a mapping into
	// []Domain must error (loud), not silently give empty domains.
	dir := t.TempDir()
	writeCfg(t, filepath.Join(dir, "config.yaml"), "domains:\n  list: !include conf.d/map.yaml\n")
	writeCfg(t, filepath.Join(dir, "conf.d/map.yaml"), "not: a list\n")
	cfg, err := LoadFile(filepath.Join(dir, "config.yaml"))
	mustErrNilCfg(t, cfg, err, "mapping as list-valued key")
}

func TestIncludeDirectoryErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "conf.d", "adir.yaml"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeCfg(t, filepath.Join(dir, "config.yaml"), "domains: !include conf.d/adir.yaml\n")
	cfg, err := LoadFile(filepath.Join(dir, "config.yaml"))
	mustErrNilCfg(t, cfg, err, "include points at a directory")
}

func TestIncludePermissionDeniedErrors(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses file permissions")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "conf.d", "secret.yaml")
	writeCfg(t, target, "- name: a.com\n")
	if err := os.Chmod(target, 0o000); err != nil {
		t.Fatal(err)
	}
	writeCfg(t, filepath.Join(dir, "config.yaml"), "domains: !include conf.d/secret.yaml\n")
	cfg, err := LoadFile(filepath.Join(dir, "config.yaml"))
	mustErrNilCfg(t, cfg, err, "permission-denied include")
}

func TestIncludeDepthLimitErrors(t *testing.T) {
	// A legitimately-too-deep chain (not a cycle) also stops with an error.
	dir := t.TempDir()
	writeCfg(t, filepath.Join(dir, "config.yaml"), "domains: !include conf.d/n0.yaml\n")
	for i := 0; i < maxIncludeDepth+3; i++ {
		writeCfg(t, filepath.Join(dir, "conf.d", fmt.Sprintf("n%d.yaml", i)),
			fmt.Sprintf("- !include n%d.yaml\n", i+1))
	}
	cfg, err := LoadFile(filepath.Join(dir, "config.yaml"))
	mustErrNilCfg(t, cfg, err, "over-deep include chain")
}

func TestIncludeMixedLiteralAndIncludeOrder(t *testing.T) {
	dir := t.TempDir()
	writeCfg(t, filepath.Join(dir, "config.yaml"),
		"domains:\n  list:\n    - name: first.com\n    - !include conf.d/mid.yaml\n    - name: last.com\n")
	writeCfg(t, filepath.Join(dir, "conf.d/mid.yaml"), "- name: mid1.com\n- name: mid2.com\n")
	cfg, err := LoadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(domainNames(cfg), ",")
	if want := "first.com,mid1.com,mid2.com,last.com"; got != want {
		t.Fatalf("order = %q, want %q", got, want)
	}
}

func TestIncludeMappingValueSection(t *testing.T) {
	// A whole non-list section via include (a mapping into a struct field).
	dir := t.TempDir()
	writeCfg(t, filepath.Join(dir, "config.yaml"), "sudoers: !include conf.d/sudoers.yaml\n")
	writeCfg(t, filepath.Join(dir, "conf.d/sudoers.yaml"), "enabled: true\nallowed_sudoers_line: \"alice ALL=(ALL) ALL\"\n")
	cfg, err := LoadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Sudoers.Enabled || cfg.Sudoers.AllowedSudoersLine != "alice ALL=(ALL) ALL" {
		t.Fatalf("sudoers = %+v", cfg.Sudoers)
	}
}

func TestIncludeEmptyValueYieldsEmptyList(t *testing.T) {
	dir := t.TempDir()
	writeCfg(t, filepath.Join(dir, "config.yaml"), "domains: !include conf.d/empty.yaml\n")
	writeCfg(t, filepath.Join(dir, "conf.d/empty.yaml"), "")
	cfg, err := LoadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Domains.List) != 0 {
		t.Fatalf("expected empty domains, got %v", domainNames(cfg))
	}
}

func TestIncludeAbsolutePath(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "external.yaml")
	writeCfg(t, abs, "- name: abs.com\n")
	writeCfg(t, filepath.Join(dir, "config.yaml"), "domains:\n  list: !include "+abs+"\n")
	cfg, err := LoadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if got := domainNames(cfg); len(got) != 1 || got[0] != "abs.com" {
		t.Fatalf("domains = %v", got)
	}
}

func TestIncludeLargeListNoQuadratic(t *testing.T) {
	// The real blocklist is ~500k entries; make sure a big include loads
	// correctly and in linear time (a quadratic splice would blow the timeout).
	const n = 40000
	dir := t.TempDir()
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "- name: d%d.example\n", i)
	}
	writeCfg(t, filepath.Join(dir, "conf.d/big.yaml"), b.String())
	// Split across two includes to exercise the flatten path too.
	writeCfg(t, filepath.Join(dir, "conf.d/one.yaml"), "- name: one.com\n")
	writeCfg(t, filepath.Join(dir, "config.yaml"),
		"domains:\n  list:\n    - !include conf.d/one.yaml\n    - !include conf.d/big.yaml\n")
	cfg, err := LoadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Domains.List) != n+1 {
		t.Fatalf("domain count = %d, want %d", len(cfg.Domains.List), n+1)
	}
	if cfg.Domains.List[0].Name != "one.com" || cfg.Domains.List[1].Name != "d0.example" || cfg.Domains.List[n].Name != fmt.Sprintf("d%d.example", n-1) {
		t.Fatalf("unexpected ordering at boundaries")
	}
}

func TestResolveIncludesBytesRoundTrip(t *testing.T) {
	// The path configcheck uses: resolve -> bytes -> (strict) decode.
	dir := t.TempDir()
	writeCfg(t, filepath.Join(dir, "conf.d/a.yaml"), "- name: rt.com\n")
	resolved, err := ResolveIncludesBytes([]byte("domains:\n  - !include conf.d/a.yaml\n"), dir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(resolved), "!include") {
		t.Fatalf("resolved bytes still contain !include:\n%s", resolved)
	}
	if !strings.Contains(string(resolved), "rt.com") {
		t.Fatalf("resolved bytes missing included content:\n%s", resolved)
	}
}

func TestResolveIncludesBytesMissingErrors(t *testing.T) {
	_, err := ResolveIncludesBytes([]byte("domains: !include conf.d/nope.yaml\n"), t.TempDir())
	if err == nil {
		t.Fatal("expected error for missing include in ResolveIncludesBytes")
	}
}
