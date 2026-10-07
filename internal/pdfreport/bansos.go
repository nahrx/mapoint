package pdfreport

import (
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/go-pdf/fpdf"

	"se2026-titik-maps/internal/bansos"
	"se2026-titik-maps/internal/report"
)

// bansosSubtitle is the one-line explanation of what these rows are, so a
// printed page is still self-describing once it leaves the screen.
const bansosSubtitle = "Penerima bantuan sosial yang dicocokkan dengan prelist SE2026. " +
	"Kolom RT/RW/Alamat KTP hanya terisi kalau desa KTP sama dengan desa DTSEN."

// bansosColumns totals 265mm of the 277mm a landscape A4 leaves, so there
// is room to spare — this table has fewer and shorter columns than the
// Daftar one.
//
// Assignment ID is not among them, for the same reason the Daftar PDF
// leaves it out: a 36-character UUID is not something anyone reads off
// paper, and it costs more width than every KTP column together. It is in
// the Excel export, which is where that kind of key is actually used.
var bansosColumns = []column{
	{"No", 10},
	{"Nama", 50},
	{"ID SubSLS", 28},
	{"Kode Desa DTSEN", 24},
	{"Alamat DTSEN", 58},
	{"RT KTP", 14},
	{"RW KTP", 14},
	{"Alamat KTP", 58},
}

func bansosRow(no int, r bansos.Row) []string {
	return []string{
		strconv.Itoa(no),
		report.DashIfEmpty(r.BansosNama),
		report.DashIfEmpty(r.SubSLS),
		report.DashIfEmpty(r.DtsenKodeDesa),
		report.DashIfEmpty(r.DtsenAlamat),
		report.DashIfEmpty(r.RtKTP),
		report.DashIfEmpty(r.RwKTP),
		report.DashIfEmpty(r.AlamatKTP),
	}
}

// GenerateBansos writes the "Daftar Bansos" PDF to w. Like GenerateRegsosek
// it reuses this package's header and table helpers, so page breaks, row
// wrapping and the wilayah block behave exactly as in the main report —
// only the columns differ.
func GenerateBansos(w io.Writer, region Region, rows []bansos.Row, truncated bool) error {
	pdf := fpdf.New("L", "mm", "A4", "")
	pdf.SetMargins(pageMargin, pageMargin, pageMargin)
	pdf.SetAutoPageBreak(false, pageMargin)
	tr := pdf.UnicodeTranslatorFromDescriptor("cp1252")

	cols := bansosColumns

	pdf.AddPage()
	writeBansosHeader(pdf, tr, region, len(rows), truncated)
	drawTableHeader(pdf, tr, cols)

	_, pageH := pdf.GetPageSize()
	bottom := pageH - pageMargin

	fill := false
	for i, r := range rows {
		row := bansosRow(i+1, r)
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

func writeBansosHeader(pdf *fpdf.Fpdf, tr func(string) string, region Region, total int, truncated bool) {
	pdf.SetFont("Arial", "B", 16)
	pdf.CellFormat(0, 9, tr("Daftar Bansos"), "", 1, "L", false, 0, "")
	pdf.SetFont("Arial", "I", 9)
	pdf.SetTextColor(90, 100, 110)
	// The subtitle is long enough to need two lines at this width.
	pdf.MultiCell(0, 4.5, tr(bansosSubtitle), "", "L", false)
	pdf.SetTextColor(0, 0, 0)
	pdf.Ln(2)

	pdf.SetFont("Arial", "B", 10)
	pdf.CellFormat(0, 6, tr("Keterangan Wilayah"), "", 1, "L", false, 0, "")

	pdf.SetFont("Arial", "", 10)
	for _, kv := range region.WilayahRows(total) {
		pdf.CellFormat(50, 5.5, tr(kv[0]), "", 0, "L", false, 0, "")
		pdf.CellFormat(0, 5.5, tr(": "+kv[1]), "", 1, "L", false, 0, "")
	}

	// Only the search can land here today; the wilayah levels are already
	// above. Printed for the same reason as in the other reports: a page
	// that came from a search must say so, or its row count looks wrong.
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
		pdf.CellFormat(0, 5.5, tr(fmt.Sprintf("Catatan: daftar ini dibatasi hingga %d baris pertama.", bansos.ReportMaxRows)), "", 1, "L", false, 0, "")
		pdf.SetTextColor(0, 0, 0)
	}
	pdf.Ln(3)
}
