// Package pdfreport renders the "Daftar Hasil Pendataan" PDF for the
// Daftar menu's download button: every row in one fully-drilled-down
// SubSLS, laid out as a wrapped table (assignment_id deliberately
// excluded — it's an internal id, not something field staff need on a
// printed list).
package pdfreport

import (
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/go-pdf/fpdf"

	"se2026-titik-maps/internal/points"
)

// Region describes the wilayah — and, optionally, the extra attribute
// filters — a report is scoped to. All fields are expected to be
// already-validated (see points.Parse*) — this package only formats them,
// it doesn't validate. JenisPrelist, KeberadaanKeluarga and Status are ""
// when that filter wasn't applied; a set filter's raw value (including
// points.EmptyValue for "filter for a blank column") is rendered via
// attrLabel. Search is "" when no name search was applied.
type Region struct {
	KabKotaCode string
	KabKotaName string
	Kecamatan   string
	Desa        string
	SLS         string
	SubSLS      string

	JenisPrelist       string
	KeberadaanKeluarga string
	Status             string
	Search             string
}

// FullCode reconstructs the 16-digit level_6_full_code these five codes
// pin exactly (4+3+3+4+2 digits) — shown once in the header instead of
// repeated in every table row, since it's identical for the whole report.
func (r Region) FullCode() string {
	return r.KabKotaCode + r.Kecamatan + r.Desa + r.SLS + r.SubSLS
}

var columns = []struct {
	header string
	width  float64 // mm
}{
	{"No", 10},
	{"Nama", 43},
	{"Alamat", 55},
	{"Jenis Prelist", 26},
	{"Keberadaan Usaha", 28},
	{"Keberadaan Keluarga", 45},
	{"Status", 40},
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

// Generate writes the report PDF to w. items should already be sorted the
// way the report should read (see points.Service.ListAll).
func Generate(w io.Writer, region Region, items []points.Point, truncated bool) error {
	pdf := fpdf.New("L", "mm", "A4", "")
	pdf.SetMargins(pageMargin, pageMargin, pageMargin)
	pdf.SetAutoPageBreak(false, pageMargin)
	tr := pdf.UnicodeTranslatorFromDescriptor("cp1252")

	pdf.AddPage()
	writeHeader(pdf, tr, region, len(items), truncated)
	drawTableHeader(pdf, tr)

	// GetPageSize returns (width, height) — for landscape A4 that's
	// (297, 210). The page-break boundary is a vertical (Y) limit, so it
	// must come from height, the SECOND return value.
	_, pageH := pdf.GetPageSize()
	bottom := pageH - pageMargin

	fill := false
	for i, p := range items {
		row := []string{
			strconv.Itoa(i + 1),
			dashIfEmpty(p.Nama),
			dashIfEmpty(p.Alamat),
			dashIfEmpty(p.JenisPrelist),
			strconv.Itoa(int(p.KeberadaanUsaha)),
			dashIfEmpty(p.KeberadaanKeluarga),
			dashIfEmpty(p.Status),
		}
		for i := range row {
			row[i] = tr(row[i])
		}

		wr := wrapRow(pdf, row)
		if pdf.GetY()+wr.height+rowSafetyMargin > bottom {
			pdf.AddPage()
			drawTableHeader(pdf, tr)
		}
		drawWrappedRow(pdf, wr, fill)
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
	rows := [][2]string{
		{"Kabupaten/Kota", fmt.Sprintf("%s (%s)", region.KabKotaName, region.KabKotaCode)},
		{"Kecamatan", region.Kecamatan},
		{"Desa/Kelurahan", region.Desa},
		{"SLS", region.SLS},
		{"SubSLS", region.SubSLS},
		{"Kode Wilayah (ID SUBSLS)", region.FullCode()},
		{"Jumlah Data", strconv.Itoa(total)},
	}
	for _, kv := range rows {
		pdf.CellFormat(50, 5.5, tr(kv[0]), "", 0, "L", false, 0, "")
		pdf.CellFormat(0, 5.5, tr(": "+kv[1]), "", 1, "L", false, 0, "")
	}

	var extra [][2]string
	if region.JenisPrelist != "" {
		extra = append(extra, [2]string{"Jenis Prelist", attrLabel(region.JenisPrelist)})
	}
	if region.KeberadaanKeluarga != "" {
		extra = append(extra, [2]string{"Keberadaan Keluarga", attrLabel(region.KeberadaanKeluarga)})
	}
	if region.Status != "" {
		extra = append(extra, [2]string{"Status", attrLabel(region.Status)})
	}
	if region.Search != "" {
		extra = append(extra, [2]string{"Cari Nama", region.Search})
	}
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

func drawTableHeader(pdf *fpdf.Fpdf, tr func(string) string) {
	pdf.SetFont("Arial", "B", 8)
	pdf.SetFillColor(230, 230, 230)
	startX, y := pdf.GetXY()
	x := startX
	for _, c := range columns {
		pdf.SetXY(x, y)
		pdf.CellFormat(c.width, 7, tr(c.header), "1", 0, "C", true, 0, "")
		x += c.width
	}
	pdf.SetXY(startX, y+7)
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
func wrapRow(pdf *fpdf.Fpdf, row []string) wrappedRow {
	wr := wrappedRow{lines: make([][][]byte, len(row))}
	maxLines := 1
	for i, text := range row {
		lines := pdf.SplitLines([]byte(text), columns[i].width-2*cellPadding)
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

func drawWrappedRow(pdf *fpdf.Fpdf, wr wrappedRow, fill bool) {
	startX, y := pdf.GetXY()
	if fill {
		pdf.SetFillColor(247, 247, 247)
	}
	x := startX
	for i, lines := range wr.lines {
		pdf.Rect(x, y, columns[i].width, wr.height, fillMode(fill))
		for li, line := range lines {
			pdf.SetXY(x+cellPadding, y+cellPadding+float64(li)*lineHeight)
			pdf.CellFormat(columns[i].width-2*cellPadding, lineHeight, string(line), "", 0, "L", false, 0, "")
		}
		x += columns[i].width
	}
	pdf.SetXY(startX, y+wr.height)
}

func fillMode(fill bool) string {
	if fill {
		return "DF"
	}
	return "D"
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// attrLabel renders an attribute filter's raw value for display, turning
// points.EmptyValue (the sentinel for "filter for a blank column") into a
// human-readable label instead of the literal sentinel string.
func attrLabel(val string) string {
	if val == points.EmptyValue {
		return "(Kosong)"
	}
	return val
}
