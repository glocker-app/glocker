package sudoplan

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestRealSystem runs a takeover and restore against the real /etc, with the
// real gpasswd and visudo. It's for a throwaway container or VM only, so it
// needs GLOCKER_SUDOPLAN_REAL_USER set to the user to take over:
//
//	go test -c -o sudoplan.test ./internal/sudoplan
//	docker run ... GLOCKER_SUDOPLAN_REAL_USER=alice ./sudoplan.test -test.run TestRealSystem -test.v
func TestRealSystem(t *testing.T) {
	user := os.Getenv("GLOCKER_SUDOPLAN_REAL_USER")
	if user == "" {
		t.Skip("set GLOCKER_SUDOPLAN_REAL_USER to run against the real system")
	}
	s := Default()
	before := map[string]string{}
	for _, p := range []string{s.Sudoers, s.GroupFile} {
		before[p] = read(t, p)
	}
	dropIns := activeDropIns(s.SudoersDir)

	p, id, ok := Detect(s)
	if !ok {
		t.Fatalf("unsupported distro %q", id)
	}
	a, err := Survey(s, p, user)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s plan: %v", p.Name, a.Describe(s, user))
	fallback := NeedsFallback(s, a)
	if fallback {
		dropIns = append(dropIns, filepath.Base(FallbackFile(s, user)))
		sort.Strings(dropIns)
	}
	t.Logf("fallback on restore: %v", fallback)
	if err := Takeover(s, p, user, user+" ALL=(ALL) ALL", a); err != nil {
		t.Fatal(err)
	}
	if left, err := Survey(s, p, user); err != nil || len(left.Groups)+len(left.Files) > 0 {
		t.Fatalf("after takeover: %+v %v", left, err)
	}
	if !strings.Contains(read(t, s.dropIn()), user+" ALL=(ALL) ALL") {
		t.Fatal("drop-in missing the line")
	}
	if err := WriteLine(s, user+" ALL=(ALL) NOPASSWD: /usr/bin/true"); err != nil {
		t.Fatal(err)
	}

	if err := Restore(s); err != nil {
		t.Fatal(err)
	}
	for p, want := range before {
		if p == s.Sudoers && a.LegacyLine {
			continue // glocker's old line is stripped for good
		}
		if got := read(t, p); got != want {
			t.Errorf("%s changed:\n--- before\n%s\n--- after\n%s", p, want, got)
		}
	}
	if got := activeDropIns(s.SudoersDir); strings.Join(got, ",") != strings.Join(dropIns, ",") {
		t.Errorf("sudoers.d = %v, want %v", got, dropIns)
	}
}
