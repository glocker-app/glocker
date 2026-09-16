package cli

import "strings"

// Restrained styling for the `-status` output. The aesthetic is deliberately
// quiet: dim greys carry structure, values use the terminal's default
// foreground, a single muted amber accent marks the title and section heads,
// and colour appears only where it MEANS something — a green dot for nominal
// state, red for anything that needs attention.
//
// These helpers ALWAYS emit 24-bit truecolor ANSI. GetStatusResponse runs both
// server-side in the daemon (its text is shipped over the socket to a client
// whose terminal it can't see) and client-side in the socket-down fallback, so
// it can't decide whether colour is wanted. The CLIENT strips ANSI when its
// stdout isn't a TTY or NO_COLOR is set (see stripANSI in cmd/glocker/main.go).

const ansiReset = "\x1b[0m"

// fg wraps s in a 24-bit truecolor foreground sequence.
func fg(r, g, b int, s string) string {
	return "\x1b[38;2;" + itoa(r) + ";" + itoa(g) + ";" + itoa(b) + "m" + s + ansiReset
}

// fgBold wraps s in a bold + truecolor foreground sequence.
func fgBold(r, g, b int, s string) string {
	return "\x1b[1;38;2;" + itoa(r) + ";" + itoa(g) + ";" + itoa(b) + "m" + s + ansiReset
}

// Muted palette. One amber accent, dim grey for structure, and green/red used
// only for semantic state.
func accent(s string) string  { return fg(200, 160, 85, s) } // title + section heads
func accentB(s string) string { return fgBold(200, 160, 85, s) }
func dim(s string) string     { return fg(105, 115, 130, s) } // labels / structure
func good(s string) string    { return fg(120, 185, 130, s) } // nominal state
func alert(s string) string   { return fg(205, 95, 95, s) }   // needs attention
func alertB(s string) string  { return fgBold(205, 95, 95, s) }

// padRight pads s with spaces to a display width of w (rune count), for
// aligning the label column.
func padRight(s string, w int) string {
	if n := w - len([]rune(s)); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

// frameWidth is the inner width (in dashes) of the boxed reports.
const frameWidth = 46

// frame renders the dim-gutter boxed layout shared by -status and -info: a bold
// amber title header, amber section heads, and aligned label/value rows down a
// dim left gutter. There is deliberately no right border, so a value that runs
// long can never knock the box out of alignment. Build one with newFrame, add
// rows/sections, then finish with close().
type frame struct {
	b     strings.Builder
	width int
}

// newFrame writes the top border: "╭─ <title><subtitle> ────" with the title in
// bold amber and the subtitle dim. subtitle may be empty.
func newFrame(title, subtitle string) *frame {
	f := &frame{width: frameWidth}
	rule := strings.Repeat("─", max(4, f.width-len([]rune(title))-len([]rune(subtitle))-1))
	f.b.WriteString("\n  " + dim("╭─ ") + accentB(title) + dim(subtitle+" ") + dim(rule) + "\n")
	return f
}

// line writes one gutter line; spacer a blank one; detail an indented sub-line.
func (f *frame) line(s string)   { f.b.WriteString("  " + dim("│  ") + s + "\n") }
func (f *frame) spacer()         { f.b.WriteString("  " + dim("│") + "\n") }
func (f *frame) detail(s string) { f.b.WriteString("  " + dim("│    ") + s + "\n") }

// row writes a "label   value" line with the label dim and left-aligned in a
// fixed column so values line up.
func (f *frame) row(label, value string) { f.line(dim(padRight(label, 15)) + value) }

// section starts a new group: a blank gutter line, then "├─ <label><suffix> ──"
// with the label in amber and the suffix dim.
func (f *frame) section(label, suffix string) {
	f.spacer()
	rule := strings.Repeat("─", max(4, f.width-len([]rune(label+suffix))))
	f.b.WriteString("  " + dim("├─ ") + accent(label) + dim(suffix+" ") + dim(rule) + "\n")
}

// head is a section with no suffix.
func (f *frame) head(label string) { f.section(label, "") }

// close writes the bottom border and returns the whole frame as a string.
func (f *frame) close() string {
	f.b.WriteString("  " + dim("╰"+strings.Repeat("─", f.width)) + "\n")
	return f.b.String()
}

// itoa is a tiny int->string for small non-negative ints, avoiding an fmt
// import in the hot path of building every colored token.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [3]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
