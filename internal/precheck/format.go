package precheck

import (
	"fmt"
	"strings"
)

// tag is the short bracketed label shown for a finding. A break-glass route that
// is open reads "KEEP" (you want it) rather than "OPEN" (a weakness).
func (f Finding) tag() string {
	if f.BreakGlass && f.Status == StatusOpen {
		return "KEEP"
	}
	switch f.Status {
	case StatusOpen:
		return "OPEN"
	case StatusClosed:
		return "OK"
	case StatusInfo:
		return "INFO"
	case StatusNA:
		return "N/A"
	default:
		return "?"
	}
}

// OpenCount is the number of enforcement-weakening open routes. It excludes the
// break-glass (recovery mode), which is meant to stay open.
func (r Report) OpenCount() int {
	n := 0
	for _, f := range r.Findings {
		if f.Status == StatusOpen && !f.BreakGlass {
			n++
		}
	}
	return n
}

// String renders the report as a readable block.
func (r Report) String() string {
	var b strings.Builder
	b.WriteString("Security check — bypass & recovery routes\n\n")
	for _, f := range r.Findings {
		fmt.Fprintf(&b, "  [%-4s] %s\n", f.tag(), f.Title)
		if f.Detail != "" {
			fmt.Fprintf(&b, "         %s\n", f.Detail)
		}
		if f.Enables != "" && f.Status == StatusOpen {
			fmt.Fprintf(&b, "         → %s\n", f.Enables)
		}
		if f.ManualFix != "" {
			fmt.Fprintf(&b, "         close: %s\n", f.ManualFix)
		}
		b.WriteString("\n")
	}
	switch n := r.OpenCount(); n {
	case 0:
		b.WriteString("No open bypass routes beyond your break-glass.\n")
	case 1:
		b.WriteString("1 open bypass route above. Closing it strengthens enforcement; " +
			"keep at least the break-glass (recovery mode) so you can always uninstall.\n")
	default:
		fmt.Fprintf(&b, "%d open bypass routes above. Closing them strengthens enforcement; "+
			"keep at least the break-glass (recovery mode) so you can always uninstall.\n", n)
	}
	return b.String()
}
