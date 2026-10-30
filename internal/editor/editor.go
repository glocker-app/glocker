// Package editor is the terminal config editor behind `glocker -edit`: a
// sidebar of every config stanza, typed fields, weekly schedule grids, and a
// validate → diff → confirm save that keeps the file's comments and layout.
package editor

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type Options struct {
	// ReadOnly opens the editor for browsing only (not root).
	ReadOnly bool
	// Suffix is appended to each path on save; "" writes in place.
	Suffix string
	// AfterSave runs after a successful write, e.g. to restart the daemon.
	AfterSave func() error
}

// Run opens the editor on the config at path until the user quits.
func Run(path string, opts Options) error {
	d, err := Load(path)
	if err != nil {
		return err
	}
	a := newApp(d, opts.Suffix)
	a.readOnly, a.afterSave = opts.ReadOnly, opts.AfterSave
	b := gateBanner(d, time.Now(), opts.ReadOnly)
	a.banner = &b
	_, err = tea.NewProgram(a, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run()
	return err
}
