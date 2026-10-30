// config-tui is a standalone prototype of a terminal editor for glocker's
// config. It edits a copy-safe way: changes go to <file>.new unless -inplace.
package main

import (
	"flag"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	inplace := flag.Bool("inplace", false, "overwrite the files instead of writing <file>.new")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: config-tui [-inplace] <conf.yaml>\n")
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
	if _, err := tea.NewProgram(newApp(d, suffix), tea.WithAltScreen(), tea.WithMouseCellMotion()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
