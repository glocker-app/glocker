// configtui runs the `glocker -edit` editor on any config file, without root,
// for working on the editor itself. Saves go to <file>.new unless -inplace.
//
//	go run ./cmd/configtui -nogate conf/conf.yaml
package main

import (
	"flag"
	"fmt"
	"os"

	"glocker/internal/editor"
)

func main() {
	inplace := flag.Bool("inplace", false, "overwrite the files instead of writing <file>.new")
	nogate := flag.Bool("nogate", false, "allow editing without root (shows the edit-mode banner)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: configtui [-inplace] [-nogate] <conf.yaml>\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	suffix := ".new"
	if *inplace {
		suffix = ""
	}
	opts := editor.Options{ReadOnly: os.Geteuid() != 0 && !*nogate, Suffix: suffix}
	if err := editor.Run(flag.Arg(0), opts); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
