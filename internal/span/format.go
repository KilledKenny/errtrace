package span

import (
	"fmt"
	"strings"
)

// Format prints s in gopls text form: path:L:C-C when the span starts and
// ends on one line (including a point span), and path:L:C-L2:C2 otherwise.
// A span with only offsets prints as path:#O-#O2, and a span with no position
// prints as the bare path. The result can be read back with Parse.
func Format(s Span) string {
	var b strings.Builder
	b.WriteString(s.File)
	switch {
	case s.Start.HasPosition():
		end := s.End
		if !end.HasPosition() {
			end = s.Start
		}
		fmt.Fprintf(&b, ":%d:%d-", s.Start.Line, s.Start.Col)
		if end.Line != s.Start.Line {
			fmt.Fprintf(&b, "%d:", end.Line)
		}
		fmt.Fprintf(&b, "%d", end.Col)
	case s.Start.HasOffset():
		end := s.End
		if !end.HasOffset() {
			end = s.Start
		}
		fmt.Fprintf(&b, ":#%d-#%d", s.Start.Offset, end.Offset)
	}
	return b.String()
}
