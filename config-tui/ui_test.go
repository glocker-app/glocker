package main

import (
	"os"
	"path/filepath"
	"testing"

	"glocker/internal/config"
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

func TestPickerTyping(t *testing.T) {
	p := newPicker("x", config.TimeWindow{}, nil, nil)
	for _, d := range []int{2, 2, 3, 0, 0, 6, 1, 5} {
		p.typeDigit(d)
	}
	w := p.window()
	if w.Start != "22:30" || w.End != "06:15" {
		t.Fatalf("got %s-%s", w.Start, w.End)
	}
	p.focus = fStartM
	p.adjust(1)
	if p.t[1] != 45 {
		t.Errorf("minute step = %d", p.t[1])
	}
}
