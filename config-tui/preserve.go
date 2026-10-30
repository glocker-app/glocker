package main

// yaml.v3 re-encodes a file with blank lines dropped and comment alignment
// collapsed. To keep saves minimal, edits are computed as a line diff between
// the re-encoded original (baseline) and the re-encoded edit, then replayed
// onto the original text, so untouched lines keep their exact formatting.

import (
	"strings"

	"github.com/pmezard/go-difflib/difflib"
)

func splitLines(b []byte) []string {
	s := strings.TrimSuffix(string(b), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func normalize(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = strings.Join(strings.Fields(l), " ")
	}
	return out
}

func opcodes(a, b []string) []difflib.OpCode {
	return difflib.NewMatcherWithJunk(a, b, false, nil).GetOpCodes()
}

// preserve replays the baseline→edited changes onto orig.
func preserve(orig, baseline, edited []byte) []byte {
	if orig == nil {
		return edited
	}
	o, base, ed := splitLines(orig), splitLines(baseline), splitLines(edited)

	// Map each baseline line to its original line (-1 if none).
	m := make([]int, len(base))
	for i := range m {
		m[i] = -1
	}
	for _, op := range opcodes(normalize(base), normalize(o)) {
		if op.Tag == 'e' {
			for k := 0; k < op.I2-op.I1; k++ {
				m[op.I1+k] = op.J1 + k
			}
		}
	}

	var out []string
	cur := 0 // next original line not yet emitted
	// keep emits original lines up to and including base line k; drop skips
	// line k itself but keeps original-only lines (blank lines) before it.
	keep := func(k int) {
		if j := m[k]; j >= cur {
			out = append(out, o[cur:j+1]...)
			cur = j + 1
		} else {
			out = append(out, base[k])
		}
	}
	drop := func(k int) {
		if j := m[k]; j >= cur {
			out = append(out, o[cur:j]...)
			cur = j + 1
		}
	}
	for _, op := range opcodes(base, ed) {
		switch op.Tag {
		case 'e':
			for k := op.I1; k < op.I2; k++ {
				keep(k)
			}
		case 'd', 'r':
			for k := op.I1; k < op.I2; k++ {
				drop(k)
			}
			out = append(out, ed[op.J1:op.J2]...)
		case 'i':
			out = append(out, ed[op.J1:op.J2]...)
		}
	}
	out = append(out, o[cur:]...)
	return []byte(strings.Join(out, "\n") + "\n")
}
