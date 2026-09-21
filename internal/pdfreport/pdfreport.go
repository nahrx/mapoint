// Package pdfreport renders the "Daftar Hasil Pendataan" PDF for the
// Daftar menu's download button: every row in the filtered wilayah
// (desa/kelurahan level or narrower), laid out as a wrapped table
// (assignment_id deliberately excluded — it's an internal id, not
// something field staff need on a printed list). The jumlah_usaha count
// stays out — see points.Point's doc comment on KeberadaanUsaha — but the
// keberadaan_BKU status column ("Keberadaan Usaha" on screen) is in, paid
// for by a narrower Catatan; see columnsFor/rowFor.
package pdfreport

import (
	"fmt"
	"io"
	"iter"
	"strconv"
	"time"

	"github.com/go-pdf/fpdf"

	"se2026-titik-maps/internal/points"
	"se2026-titik-maps/internal/report"
)

// Region is the wilayah/filter metadata a report is scoped to — see
// report.Region, shared with xlsxreport so both export formats describe
// their scope identically.
type Region = report.Region

type column struct {
	header string
	width  float64 // mm
}

// Landscape A4 is 297mm wide; minus pageMargin on both sides that leaves
// 277mm for the table, which both column sets below stay just under.
var (
	// subslsColumns is used when the report covers exactly one SubSLS: the
	// ID SUBSLS is identical on every row there, so the header states it
	// once (see report.Region.WilayahRows) and the table spends the space
	// on Nama/Alamat/Catatan instead. Total: 276mm.
	//
	// Keberadaan Usaha's 24mm came out of Catatan (60 -> 37) and Keberadaan
	// Keluarga (34 -> 30): Catatan is the one column that wraps freely
	// without losing anything, and the longest Keberadaan Usaha value
	// ("7. Data diperoleh dari Kantor Pusat (KP)") wraps to two lines at
	// 24mm, which the row height already accounts for. Nomor Bangunan and
	// Regsosek are 18mm because that is the narrowest column in which
	// SplitLines keeps "Bangunan" / "Regsosek" whole (see
	// TestHeaderLabelsWrapOnWordBoundaries) — at 17mm the header would
	// have read "Regsose / k".
	subslsColumns = []column{
		{"No", 8},
		{"Nama", 36},
		{"Alamat", 40},
		{"Nomor Bangunan", 18},
		{"Jenis Prelist", 18},
		{"Keberadaan Keluarga", 30},
		{"Keberadaan Usaha", 24},
		{"Status", 30},
		{"Ass. Baru", 17},
		{"Regsosek", 18},
		{"Catatan", 37},
	}

	// wideColumns is used for anything broader than one SubSLS (a whole
	// desa/kelurahan, or an SLS): level_6_full_code then differs from row
	// to row, so it has to be a column of its own or the report couldn't
	// tell you which SubSLS a given row belongs to. Total: 275mm. Same
	// trade as above for Keberadaan Usaha: Catatan 52 -> 30, Keberadaan
	// Keluarga 30 -> 28, Alamat 33 -> 31, with Nomor Bangunan and Regsosek
	// widened to 18mm for the same whole-word reason.
	wideColumns = []column{
		{"No", 8},
		{"Nama", 32},
		{"Alamat", 31},
		{"ID SUBSLS", 26},
		{"Nomor Bangunan", 18},
		{"Jenis Prelist", 17},
		{"Keberadaan Keluarga", 28},
		{"Keberadaan Usaha", 22},
		{"Status", 28},
		{"Ass. Baru", 17},
		{"Regsosek", 18},
		{"Catatan", 30},
	}
)

// flagMark is the printable stand-in for the checkmark the web table
// shows. fpdf writes cp1252, which has no U+2713, so a "V" is used rather
// than letting the glyph silently drop out of the PDF.
func flagMark(ada bool) string {
	if ada {
		return "V"
	}
	return "-"
}

// columnsFor picks the column set matching the report's scope, and rowFor
// must stay in step with it — both switch on the same condition.
func columnsFor(region Region) []column {
	if region.PinnedToSubSLS() {
		return subslsColumns
	}
	return wideColumns
}

// rowFor renders one row of the report table. Catatan comes from ListAll's
// includeCatatan=true, so it's always populated here even though it's
// empty for every other Point-returning endpoint. The jumlah_usaha count
// (points.Point.KeberadaanUsaha) is still not a column — see that field's
// doc comment; "Keberadaan Usaha" here is keberadaan_BKU.
func rowFor(region Region, no int, p points.Point) []string {
	if region.PinnedToSubSLS() {
		return []string{
			strconv.Itoa(no),
			report.DashIfEmpty(p.Nama),
			report.DashIfEmpty(p.Alamat),
			strconv.Itoa(int(p.NomorBangunan)),
			report.DashIfEmpty(p.JenisPrelist),
			report.DashIfEmpty(p.KeberadaanKeluarga),
			report.DashIfEmpty(p.KeberadaanBKU),
			report.DashIfEmpty(p.Status),
			flagMark(p.AdaAssignmentBaru),
			flagMark(p.AdaRegsosek),
			report.DashIfEmpty(p.Catatan),
		}
	}
	return []string{
		strconv.Itoa(no),
		report.DashIfEmpty(p.Nama),
		report.DashIfEmpty(p.Alamat),
		report.DashIfEmpty(p.SubSLS),
		strconv.Itoa(int(p.NomorBangunan)),
		report.DashIfEmpty(p.JenisPrelist),
		report.DashIfEmpty(p.KeberadaanKeluarga),
		report.DashIfEmpty(p.KeberadaanBKU),
		report.DashIfEmpty(p.Status),
		flagMark(p.AdaAssignmentBaru),
		flagMark(p.AdaRegsosek),
		report.DashIfEmpty(p.Catatan),
	}
}

const (
	lineHeight  = 5.0 // mm, per wrapped line
	cellPadding = 1.0 // mm
	pageMargin  = 10.0
	// rowSafetyMargin absorbs any tiny rounding gap between rowHeight's
	// SplitLines-based estimate and what MultiCell actually renders, so a
	// row is never left silently clipped right at the page boundary.
	rowSafetyMargin = 0.5
)

// Generate writes the report PDF to w. rows should already come in the
// order the report should read (see points.Service.ListAll); total is how
// many of them there are, for the "Jumlah Data" line in the header, which
// is drawn before the first row is seen.
func Generate(w io.Writer, region Region, total int, rows iter.Seq[points.Point], truncated bool) error {
	pdf := fpdf.New("L", "mm", "A4", "")
	pdf.SetMargins(pageMargin, pageMargin, pageMargin)
	pdf.SetAutoPageBreak(false, pageMargin)
	tr := pdf.UnicodeTranslatorFromDescriptor("cp1252")

	cols := columnsFor(region)

	pdf.AddPage()
	writeHeader(pdf, tr, region, total, truncated)
	drawTableHeader(pdf, tr, cols)

	// GetPageSize returns (width, height) — for landscape A4 that's
	// (297, 210). The page-break boundary is a vertical (Y) limit, so it
	// must come from height, the SECOND return value.
	_, pageH := pdf.GetPageSize()
	bottom := pageH - pageMargin

	fill := false
	n := 0
	for p := range rows {
		n++
		row := rowFor(region, n, p)
		for i := range row {
			row[i] = tr(row[i])
		}

		wr := wrapRow(pdf, row, cols)
		if pdf.GetY()+wr.height+rowSafetyMargin > bottom {
			pdf.AddPage()
			drawTableHeader(pdf, tr, cols)
		}
		drawWrappedRow(pdf, wr, cols, fill)
		fill = !fill
	}

	pdf.SetY(-15)
	pdf.SetFont("Arial", "I", 8)
	pdf.CellFormat(0, 5, tr(fmt.Sprintf("Dicetak %s — Peta Titik SE2026", time.Now().Format("2 January 2006 15:04"))), "", 0, "L", false, 0, "")
	pdf.CellFormat(0, 5, fmt.Sprintf("Halaman %d", pdf.PageNo()), "", 0, "R", false, 0, "")

	pdf.AliasNbPages("")
	if err := pdf.Output(w); err != nil {
		return err
	}
	// Output() itself returns any error fpdf accumulated internally, but
	// check once more explicitly — a silently-set f.err earlier in the
	// document (e.g. from a character CellFormat can't encode) must never
	// result in a PDF that looks complete but has content quietly dropped.
	if pdf.Err() {
		return pdf.Error()
	}
	return nil
}

func writeHeader(pdf *fpdf.Fpdf, tr func(string) string, region Region, total int, truncated bool) {
	pdf.SetFont("Arial", "B", 16)
	pdf.CellFormat(0, 9, tr("Daftar Hasil Pendataan"), "", 1, "L", false, 0, "")
	pdf.Ln(1)

	pdf.SetFont("Arial", "B", 10)
	pdf.CellFormat(0, 6, tr("Keterangan Wilayah"), "", 1, "L", false, 0, "")

	pdf.SetFont("Arial", "", 10)
	for _, kv := range region.WilayahRows(total) {
		pdf.CellFormat(50, 5.5, tr(kv[0]), "", 0, "L", false, 0, "")
		pdf.CellFormat(0, 5.5, tr(": "+kv[1]), "", 1, "L", false, 0, "")
	}

	extra := region.ExtraFilters()
	if len(extra) > 0 {
		pdf.Ln(2)
		pdf.SetFont("Arial", "B", 10)
		pdf.CellFormat(0, 6, tr("Filter Tambahan"), "", 1, "L", false, 0, "")
		pdf.SetFont("Arial", "", 10)
		for _, kv := range extra {
			pdf.CellFormat(50, 5.5, tr(kv[0]), "", 0, "L", false, 0, "")
			pdf.CellFormat(0, 5.5, tr(": "+kv[1]), "", 1, "L", false, 0, "")
		}
	}

	if truncated {
		pdf.SetFont("Arial", "I", 9)
		pdf.SetTextColor(180, 60, 30)
		pdf.CellFormat(0, 5.5, tr(fmt.Sprintf("Catatan: daftar ini dibatasi hingga %d baris pertama.", points.ReportMaxRows)), "", 1, "L", false, 0, "")
		pdf.SetTextColor(0, 0, 0)
	}
	pdf.Ln(3)
}

// headerLineHeight is the per-line height of a wrapped header label at
// the 8pt bold the header uses.
const headerLineHeight = 3.6

// drawTableHeader draws the column header row, wrapping any label that is
// wider than its column and giving the whole row the height of its
// tallest cell.
//
// It used to be one CellFormat per column at a fixed 7mm, which does not
// wrap and does not clip: a label wider than its cell simply ran on into
// the neighbour. Measured with GetStringWidth at this font, that was
// already the case before Keberadaan Usaha arrived — "Nomor Bangunan" is
// 23.7mm in a 15mm column, "Keberadaan Keluarga" 29.0mm in 28mm — so the
// header row had overlapping text on every page. Adding a 22mm
// "Keberadaan Usaha" (25.4mm) made it one more. Wrapping fixes all of
// them at once: the header now takes two lines (about 9mm), and each
// label sits inside its own box.
func drawTableHeader(pdf *fpdf.Fpdf, tr func(string) string, cols []column) {
	pdf.SetFont("Arial", "B", 8)
	pdf.SetFillColor(230, 230, 230)
	startX, y := pdf.GetXY()

	// Wrap every label first, so the row height is known before anything
	// is drawn: every cell's box must be the same height regardless of how
	// many lines its own label needs.
	lines := make([][][]byte, len(cols))
	maxLines := 1
	for i, c := range cols {
		lines[i] = pdf.SplitLines([]byte(tr(c.header)), c.width-2*cellPadding)
		if len(lines[i]) > maxLines {
			maxLines = len(lines[i])
		}
	}
	h := float64(maxLines)*headerLineHeight + 2*cellPadding

	x := startX
	for i, c := range cols {
		// Box first (fill + border at the full row height), then the text
		// vertically centred inside it — a label with fewer lines than the
		// tallest one still sits in the middle of its cell.
		pdf.Rect(x, y, c.width, h, "FD")
		top := y + (h-float64(len(lines[i]))*headerLineHeight)/2
		for j, ln := range lines[i] {
			pdf.SetXY(x, top+float64(j)*headerLineHeight)
			pdf.CellFormat(c.width, headerLineHeight, string(ln), "", 0, "C", false, 0, "")
		}
		x += c.width
	}
	pdf.SetXY(startX, y+h)
	pdf.SetFont("Arial", "", 9)
}

// wrappedRow holds one row's text already broken into per-column lines,
// plus the row height that follows from that split.
type wrappedRow struct {
	lines  [][][]byte // lines[col][lineIndex]
	height float64
}

// wrapRow splits every column's text into wrapped lines exactly once and
// derives the row height from that same split. drawWrappedRow later renders
// those same lines directly (no second, independent wrap computation) —
// there is deliberately only one source of truth for how a row wraps, so
// the height used to decide page breaks can never disagree with what
// actually gets drawn.
func wrapRow(pdf *fpdf.Fpdf, row []string, cols []column) wrappedRow {
	wr := wrappedRow{lines: make([][][]byte, len(row))}
	maxLines := 1
	for i, text := range row {
		lines := pdf.SplitLines([]byte(text), cols[i].width-2*cellPadding)
		if len(lines) == 0 {
			lines = [][]byte{[]byte("")}
		}
		wr.lines[i] = lines
		if len(lines) > maxLines {
			maxLines = len(lines)
		}
	}
	wr.height = float64(maxLines) * lineHeight
	return wr
}

func drawWrappedRow(pdf *fpdf.Fpdf, wr wrappedRow, cols []column, fill bool) {
	startX, y := pdf.GetXY()
	if fill {
		pdf.SetFillColor(247, 247, 247)
	}
	x := startX
	for i, lines := range wr.lines {
		pdf.Rect(x, y, cols[i].width, wr.height, fillMode(fill))
		for li, line := range lines {
			pdf.SetXY(x+cellPadding, y+cellPadding+float64(li)*lineHeight)
			pdf.CellFormat(cols[i].width-2*cellPadding, lineHeight, string(line), "", 0, "L", false, 0, "")
		}
		x += cols[i].width
	}
	pdf.SetXY(startX, y+wr.height)
}

func fillMode(fill bool) string {
	if fill {
		return "DF"
	}
	return "D"
}
