package pdfreport

import (
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/go-pdf/fpdf"

	"se2026-titik-maps/internal/regsosek"
	"se2026-titik-maps/internal/report"
)

// regsosekSubtitle is the one-line explanation of what these rows are,
// printed under the title so a printed copy is self-describing.
const regsosekSubtitle = "Daftar keluarga SE2026 dengan status tidak ditemukan, tapi ditemukan di Reg2022."

// regsosekColumns totals 275mm, inside the 277mm a landscape A4 leaves
// between margins. Assignment ID is left out for the same reason it is in
// the other PDF: it's an internal id, not something a field team reads off
// paper. The Excel export keeps it.
var regsosekColumns = []column{
	{"No", 8},
	{"Nama", 40},
	{"Nama KK", 34},
	{"No KK", 30},
	{"NIK KK", 30},
	{"ID SubSLS", 27},
	{"Match Status", 38},
	{"Alamat Regsosek", 38},
	{"Nama Matched", 30},
}

func regsosekRow(no int, r regsosek.Row) []string {
	return []string{
		strconv.Itoa(no),
		report.DashIfEmpty(r.Nama),
		report.DashIfEmpty(r.NamaKK),
		report.DashIfEmpty(r.NoKK),
		report.DashIfEmpty(r.NIKKK),
		report.DashIfEmpty(r.SubSLS),
		report.DashIfEmpty(r.MatchStatus),
		report.DashIfEmpty(r.AlamatRegsosek),
		report.DashIfEmpty(r.NamaMatched),
	}
}

// GenerateRegsosek writes the "Daftar Match Regsosek" PDF to w. It reuses
// this package's header and table-drawing helpers, so page breaks, row
// wrapping and the wilayah/filter block behave exactly as in the main
// report — only the columns differ.
func GenerateRegsosek(w io.Writer, region Region, rows []regsosek.Row, truncated bool) error {
	pdf := fpdf.New("L", "mm", "A4", "")
	pdf.SetMargins(pageMargin, pageMargin, pageMargin)
	pdf.SetAutoPageBreak(false, pageMargin)
	tr := pdf.UnicodeTranslatorFromDescriptor("cp1252")

	cols := regsosekColumns

	pdf.AddPage()
	writeRegsosekHeader(pdf, tr, region, len(rows), truncated)
	drawTableHeader(pdf, tr, cols)

	_, pageH := pdf.GetPageSize()
	bottom := pageH - pageMargin

	fill := false
	for i, r := range rows {
		row := regsosekRow(i+1, r)
		for j := range row {
			row[j] = tr(row[j])
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
	if pdf.Err() {
		return pdf.Error()
	}
	return nil
}

// writeRegsosekHeader is writeHeader plus the subtitle explaining what the
// list contains.
func writeRegsosekHeader(pdf *fpdf.Fpdf, tr func(string) string, region Region, total int, truncated bool) {
	pdf.SetFont("Arial", "B", 16)
	pdf.CellFormat(0, 9, tr("Daftar Match Regsosek"), "", 1, "L", false, 0, "")
	pdf.SetFont("Arial", "I", 9)
	pdf.SetTextColor(90, 100, 110)
	pdf.CellFormat(0, 5, tr(regsosekSubtitle), "", 1, "L", false, 0, "")
	pdf.SetTextColor(0, 0, 0)
	pdf.Ln(2)

	pdf.SetFont("Arial", "B", 10)
	pdf.CellFormat(0, 6, tr("Keterangan Wilayah"), "", 1, "L", false, 0, "")

	pdf.SetFont("Arial", "", 10)
	for _, kv := range region.WilayahRows(total) {
		pdf.CellFormat(50, 5.5, tr(kv[0]), "", 0, "L", false, 0, "")
		pdf.CellFormat(0, 5.5, tr(": "+kv[1]), "", 1, "L", false, 0, "")
	}

	if extra := region.ExtraFilters(); len(extra) > 0 {
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
		pdf.CellFormat(0, 5.5, tr(fmt.Sprintf("Catatan: daftar ini dibatasi hingga %d baris pertama.", regsosek.ReportMaxRows)), "", 1, "L", false, 0, "")
		pdf.SetTextColor(0, 0, 0)
	}
	pdf.Ln(3)
}
