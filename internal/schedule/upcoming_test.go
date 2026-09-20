package schedule

import (
	"testing"
	"time"

	"glocker/internal/config"
	"glocker/internal/state"
)

// base is a fixed weekday mid-morning, far from midnight so no test window
// wraps around by accident.
var base = time.Date(2026, 9, 21, 10, 0, 0, 0, time.Local)

// win builds a window running from base+startMin to base+endMin on base's day.
func win(startMin, endMin int) config.TimeWindow {
	return config.TimeWindow{
		Start: base.Add(time.Duration(startMin) * time.Minute).Format("15:04"),
		End:   base.Add(time.Duration(endMin) * time.Minute).Format("15:04"),
		Days:  []string{base.Weekday().String()[:3]},
	}
}

func domainCfg(d config.Domain) *config.Config {
	return &config.Config{Domains: config.DomainsConfig{Enabled: true, List: []config.Domain{d}}}
}

func TestUpcomingDomainBlockStarts(t *testing.T) {
	cfg := domainCfg(config.Domain{Name: "youtube.com", BlockWindows: []config.TimeWindow{win(7, 120)}})

	got := Upcoming(cfg, base, DefaultHorizon)
	if len(got) != 1 {
		t.Fatalf("want 1 change, got %d (%+v)", len(got), got)
	}
	c := got[0]
	if c.Name != "youtube.com" || c.Verb() != "block" {
		t.Errorf("unexpected change: %+v (verb %q)", c, c.Verb())
	}
	if want := base.Add(7 * time.Minute); !c.At.Equal(want) {
		t.Errorf("At = %v, want %v", c.At, want)
	}
}

func TestUpcomingIgnoresBlocksLifting(t *testing.T) {
	// Window is already running and ends in 5 minutes. Something freeing up is
	// not something to watch out for, so it must not be listed.
	cfg := domainCfg(config.Domain{Name: "reddit.com", BlockWindows: []config.TimeWindow{win(-30, 5)}})

	if got := Upcoming(cfg, base, DefaultHorizon); len(got) != 0 {
		t.Errorf("want no changes for a block that only lifts, got %+v", got)
	}
}

// A block that lifts and then comes back inside the horizon must still report
// the re-block — the reason Upcoming looks for rising edges rather than
// filtering "the first flip" down to tightenings after the fact.
func TestUpcomingReportsReblockAfterALift(t *testing.T) {
	cfg := domainCfg(config.Domain{Name: "reddit.com", BlockWindows: []config.TimeWindow{
		win(-30, 3),  // running now, ends in 3 minutes
		win(10, 120), // back on in 10
	}})

	got := Upcoming(cfg, base, DefaultHorizon)
	if len(got) != 1 {
		t.Fatalf("want 1 change, got %d (%+v)", len(got), got)
	}
	if c := got[0]; c.Verb() != "block" || !c.At.Equal(base.Add(10*time.Minute)) {
		t.Errorf("unexpected change: %+v", c)
	}
}

func TestUpcomingIgnoresDistantAndPermanentDomains(t *testing.T) {
	cfg := &config.Config{Domains: config.DomainsConfig{Enabled: true, List: []config.Domain{
		{Name: "permanent.example", BlockWindows: nil},                           // always blocked, never flips
		{Name: "later.example", BlockWindows: []config.TimeWindow{win(60, 120)}}, // outside the horizon
	}}}

	if got := Upcoming(cfg, base, DefaultHorizon); len(got) != 0 {
		t.Errorf("want no changes, got %+v", got)
	}
}

func TestUpcomingDisabledMechanismsAreSilent(t *testing.T) {
	cfg := domainCfg(config.Domain{Name: "youtube.com", BlockWindows: []config.TimeWindow{win(7, 120)}})
	cfg.Domains.Enabled = false

	if got := Upcoming(cfg, base, DefaultHorizon); len(got) != 0 {
		t.Errorf("want no changes when domain blocking is off, got %+v", got)
	}
}

func TestUpcomingProgramKillAndSudoLock(t *testing.T) {
	cfg := &config.Config{
		Programs: config.ProgramsConfig{Enabled: true, List: []config.ForbiddenProgram{
			// Allowed until 10 minutes from now, killed outside that window.
			{Name: "steam", AllowWindows: []config.TimeWindow{win(-60, 10)}},
		}},
		Sudoers: config.SudoersConfig{Enabled: true, AllowWindows: []config.TimeWindow{win(-60, 3)}},
	}

	got := Upcoming(cfg, base, DefaultHorizon)
	if len(got) != 2 {
		t.Fatalf("want 2 changes, got %d (%+v)", len(got), got)
	}
	// Earliest first: sudo locks at +4, steam becomes killable at +11.
	if c := got[0]; c.Kind != KindSudo || c.Verb() != "lock" || c.Label() != "sudo" {
		t.Errorf("first change: %+v (verb %q)", c, c.Verb())
	}
	if c := got[1]; c.Kind != KindProgram || c.Verb() != "kill" || c.Name != "steam" {
		t.Errorf("second change: %+v (verb %q)", c, c.Verb())
	}
	if !got[0].At.Before(got[1].At) {
		t.Errorf("changes are not sorted: %v then %v", got[0].At, got[1].At)
	}
}

func TestUpcomingProgramExtensionLapsing(t *testing.T) {
	orig := state.ProgramExtensionsPath
	state.ProgramExtensionsPath = t.TempDir() + "/extensions.json"
	state.ClearProgramExtensions()
	t.Cleanup(func() {
		state.ProgramExtensionsPath = orig
		state.ClearProgramExtensions()
	})

	// Killed 24/7, but a runtime grant is keeping it alive for 8 more minutes.
	cfg := &config.Config{Programs: config.ProgramsConfig{Enabled: true, List: []config.ForbiddenProgram{
		{Name: "firefox-esr", Extendible: true},
	}}}
	if err := state.AddProgramExtension(state.ProgramExtension{
		Program:   "firefox-esr",
		GrantedAt: base,
		ExpiresAt: base.Add(8 * time.Minute),
	}); err != nil {
		t.Fatalf("AddProgramExtension: %v", err)
	}

	got := Upcoming(cfg, base, DefaultHorizon)
	if len(got) != 1 {
		t.Fatalf("want 1 change, got %d (%+v)", len(got), got)
	}
	if c := got[0]; c.Name != "firefox-esr" || !c.At.Equal(base.Add(8*time.Minute)) {
		t.Errorf("unexpected change: %+v", c)
	}
}
