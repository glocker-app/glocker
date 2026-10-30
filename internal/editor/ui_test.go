package editor

import (
	"os"
	"path/filepath"
	"testing"
)

// Rendering every section (including stanzas missing from the file) must not
// add anything to the config.
func TestViewingDoesNotEdit(t *testing.T) {
	p := filepath.Join(t.TempDir(), "conf.yaml")
	must(t, os.WriteFile(p, []byte("user: \"tester\"\n"), 0o644))
	d, err := Load(p)
	must(t, err)
	a := newApp(d, ".new")
	for i := range a.sections {
		a.sec = i
		_ = a.View()
		a.focusSide = false
		_ = a.View()
	}
	ch, err := d.Changes()
	must(t, err)
	if len(ch) != 0 {
		t.Fatalf("viewing changed the config:\n%s", ch[0].New)
	}
}
