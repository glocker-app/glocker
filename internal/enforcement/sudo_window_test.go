package enforcement

import (
	"testing"
	"time"

	"glocker/internal/config"
)

// An overnight allow window stays open after midnight: Mon 22:00-02:00 must
// allow sudo at 01:00 on Tuesday, and the live check must agree with
// IsSudoAllowed (which -status previews from).
func TestIsSudoersAllowed_OvernightWindow(t *testing.T) {
	cfg := &config.Config{Sudoers: config.SudoersConfig{
		Enabled:      true,
		AllowWindows: []config.TimeWindow{{Start: "22:00", End: "02:00", Days: []string{"Mon"}}},
	}}
	loc := time.UTC
	cases := []struct {
		at   time.Time
		want bool
	}{
		{time.Date(2026, 10, 19, 23, 0, 0, 0, loc), true},  // Mon 23:00
		{time.Date(2026, 10, 20, 1, 0, 0, 0, loc), true},   // Tue 01:00, same window
		{time.Date(2026, 10, 20, 3, 0, 0, 0, loc), false},  // Tue 03:00, after it
		{time.Date(2026, 10, 20, 23, 0, 0, 0, loc), false}, // Tue 23:00, wrong day
	}
	for _, c := range cases {
		if got := isSudoersAllowed(cfg, c.at); got != c.want {
			t.Errorf("isSudoersAllowed(%s) = %v, want %v", c.at.Format("Mon 15:04"), got, c.want)
		}
		if got := IsSudoAllowed(cfg, c.at); got != c.want {
			t.Errorf("IsSudoAllowed(%s) = %v, want %v", c.at.Format("Mon 15:04"), got, c.want)
		}
	}
}
