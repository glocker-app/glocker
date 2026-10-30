// config-tui is a standalone prototype of a terminal editor for glocker's
// config. It edits a copy-safe way: changes go to <file>.new unless -inplace.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	inplace := flag.Bool("inplace", false, "overwrite the files instead of writing <file>.new")
	nogate := flag.Bool("nogate", false, "allow editing without root (prototyping)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: config-tui [-inplace] [-nogate] <conf.yaml>\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	d, err := Load(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	suffix := ".new"
	if *inplace {
		suffix = ""
	}
	a := newApp(d, suffix)
	// -nogate stands in for root, so it shows the edit-mode banner.
	a.readOnly = os.Geteuid() != 0 && !*nogate
	b := gateBanner(d, time.Now(), a.readOnly)
	a.banner = &b
	if _, err := tea.NewProgram(a, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
