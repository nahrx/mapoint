package pdfreport

import (
	"strings"
	"testing"

	"github.com/go-pdf/fpdf"
)

// Every header label must wrap only at spaces inside its column. fpdf's
// SplitLines breaks a word that doesn't fit on its own line at a
// character, so a column one millimetre too narrow prints "Regsose / k"
// — which is exactly what a 17mm Regsosek column did. Any change to a
// width or a label runs into this test before it runs into a printout.
func TestHeaderLabelsWrapOnWordBoundaries(t *testing.T) {
	pdf := fpdf.New("L", "mm", "A4", "")
	pdf.SetFont("Arial", "B", 8) // the font drawTableHeader uses
	for _, set := range []struct {
		name string
		cols []column
	}{{"subslsColumns", subslsColumns}, {"wideColumns", wideColumns}, {"regsosekColumns", regsosekColumns}} {
		total := 0.0
		for _, c := range set.cols {
			total += c.width
			lines := pdf.SplitLines([]byte(c.header), c.width-2*cellPadding)
			var parts []string
			for _, l := range lines {
				parts = append(parts, string(l))
			}
			if strings.Join(parts, " ") != c.header {
				t.Errorf("%s: header %q at %.0fmm breaks mid-word: %q", set.name, c.header, c.width, parts)
			}
		}
		// Landscape A4 minus the two page margins.
		if limit := 297 - 2*pageMargin; total > limit {
			t.Errorf("%s: columns total %.0fmm, wider than the %.0fmm page", set.name, total, limit)
		}
	}
}
