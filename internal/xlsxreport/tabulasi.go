package xlsxreport

import (
	"fmt"
	"io"
	"time"

	"github.com/xuri/excelize/v2"

	"se2026-titik-maps/internal/points"
)

// TabulasiScope is the wilayah a tabulation workbook covers. Every field
// may be empty — unlike the Daftar report, a tabulation is aggregated, so
// the whole province is a perfectly reasonable download (17k rows, not
// 2 million) and nothing forces a minimum level.
type TabulasiScope struct {
	KabKotaCode string
	KabKotaName string
	Kecamatan   string
	Desa        string
	SLS         string
	SubSLS      string
}

// rows renders the scope as label/value pairs, "(Semua)" where a level is
// unset, so a province-wide workbook says so explicitly instead of
// showing blanks that read like missing data.
func (sc TabulasiScope) rows() [][2]string {
	all := func(s string) string {
		if s == "" {
			return "(Semua)"
		}
		return s
	}
	kabkota := "(Semua)"
	if sc.KabKotaCode != "" {
		kabkota = fmt.Sprintf("%s (%s)", sc.KabKotaName, sc.KabKotaCode)
	}
	return [][2]string{
		{"Kabupaten/Kota", kabkota},
		{"Kecamatan", all(sc.Kecamatan)},
		{"Desa/Kelurahan", all(sc.Desa)},
		{"SLS", all(sc.SLS)},
		{"SubSLS", all(sc.SubSLS)},
	}
}

// GenerateTabulasi writes one workbook with one sheet per table — the
// five variables of the Tabulasi menu, all for the same scope — so a single
// download is the complete tabulation rather than one tab's worth.
//
// Numbers are written as numbers, not formatted strings: the point of the
// spreadsheet is to sum, pivot and chart these further, and a "1.234" cell
// is text to Excel.
func GenerateTabulasi(w io.Writer, scope TabulasiScope, tables []points.TabulasiTable) error {
	if len(tables) == 0 {
		return fmt.Errorf("xlsxreport: tabulasi: no tables")
	}
	f := excelize.NewFile()
	defer f.Close()

	st, err := newStyles(f)
	if err != nil {
		return fmt.Errorf("xlsxreport: styles: %w", err)
	}
	// One extra style for the totals row: bold on the header fill, so it
	// reads as a summary line rather than one more SubSLS.
	totalStyle, err := f.NewStyle(&excelize.Style{
		Font: &excelize.Font{Bold: true, Size: 10},
		Fill: excelize.Fill{Type: "pattern", Color: []string{"e6e6e6"}, Pattern: 1},
		Border: []excelize.Border{
			{Type: "left", Color: "cccccc", Style: 1},
			{Type: "top", Color: "999999", Style: 2},
			{Type: "right", Color: "cccccc", Style: 1},
			{Type: "bottom", Color: "cccccc", Style: 1},
		},
	})
	if err != nil {
		return fmt.Errorf("xlsxreport: total style: %w", err)
	}

	for i, t := range tables {
		// Sheet names are capped at 31 characters by Excel; the longest
		// label here ("Penggunaan Bangunan") is 19, so no truncation
		// scheme is needed — but guard anyway so a future label can't
		// make the whole export fail.
		sheet := t.Variable.Label
		if len([]rune(sheet)) > 31 {
			sheet = string([]rune(sheet)[:31])
		}
		if i == 0 {
			if err := f.SetSheetName(f.GetSheetName(0), sheet); err != nil {
				return fmt.Errorf("xlsxreport: rename sheet: %w", err)
			}
		} else if _, err := f.NewSheet(sheet); err != nil {
			return fmt.Errorf("xlsxreport: new sheet %q: %w", sheet, err)
		}
		if err := writeTabulasiSheet(f, sheet, st, totalStyle, scope, t); err != nil {
			return fmt.Errorf("xlsxreport: sheet %q: %w", sheet, err)
		}
	}
	f.SetActiveSheet(0)

	if err := f.Write(w); err != nil {
		return fmt.Errorf("xlsxreport: write: %w", err)
	}
	return nil
}

func writeTabulasiSheet(f *excelize.File, sheet string, st styles, totalStyle int, scope TabulasiScope, t points.TabulasiTable) error {
	row := 1
	set := func(cell string, style int, value any) {
		f.SetCellValue(sheet, cell, value)
		f.SetCellStyle(sheet, cell, cell, style)
	}

	// --- info block --------------------------------------------------------
	set(fmt.Sprintf("A%d", row), st.title, fmt.Sprintf("Tabulasi %s per SubSLS", t.Variable.Label))
	row += 2
	set(fmt.Sprintf("A%d", row), st.section, "Keterangan Wilayah")
	row++
	for _, e := range scope.rows() {
		set(fmt.Sprintf("A%d", row), st.label, e[0])
		set(fmt.Sprintf("B%d", row), st.label, e[1])
		row++
	}
	set(fmt.Sprintf("A%d", row), st.label, "Jumlah SubSLS")
	set(fmt.Sprintf("B%d", row), st.label, int64(t.TotalRows))
	row++
	set(fmt.Sprintf("A%d", row), st.label, "Jumlah Data")
	set(fmt.Sprintf("B%d", row), st.label, int64(t.GrandTotal))
	row++
	if t.Truncated {
		row++
		set(fmt.Sprintf("A%d", row), st.warn, fmt.Sprintf("Catatan: tabel ini dibatasi hingga %d SubSLS pertama.", points.TabulasiMaxRows))
		row++
	}
	row++
	set(fmt.Sprintf("A%d", row), st.footnote, fmt.Sprintf("Diunduh %s — Peta Titik SE2026", time.Now().Format("2 January 2006 15:04")))
	row += 2

	// --- header row --------------------------------------------------------
	headerRow := row
	headers := append([]string{"No", "ID SUBSLS"}, make([]string, 0, len(t.Columns)+1)...)
	for _, c := range t.Columns {
		if c == "" {
			c = "(Kosong)"
		}
		headers = append(headers, c)
	}
	headers = append(headers, "Total")
	for ci, h := range headers {
		cell, err := excelize.CoordinatesToCellName(ci+1, headerRow)
		if err != nil {
			return err
		}
		set(cell, st.tblHead, h)
	}

	// --- data rows ---------------------------------------------------------
	for ri, r := range t.Rows {
		rowNum := headerRow + 1 + ri
		style := st.tblCell
		if ri%2 == 1 {
			style = st.tblFill
		}
		values := make([]any, 0, len(headers))
		values = append(values, ri+1, r.SubSLS)
		for _, c := range t.Columns {
			values = append(values, int64(r.Counts[c]))
		}
		values = append(values, int64(r.Total))
		for ci, v := range values {
			cell, err := excelize.CoordinatesToCellName(ci+1, rowNum)
			if err != nil {
				return err
			}
			set(cell, style, v)
		}
	}

	// --- totals row --------------------------------------------------------
	totalRow := headerRow + 1 + len(t.Rows)
	totals := make([]any, 0, len(headers))
	totals = append(totals, "", "Total")
	for _, c := range t.Columns {
		totals = append(totals, int64(t.Grand[c]))
	}
	totals = append(totals, int64(t.GrandTotal))
	for ci, v := range totals {
		cell, err := excelize.CoordinatesToCellName(ci+1, totalRow)
		if err != nil {
			return err
		}
		set(cell, totalStyle, v)
	}

	// --- widths, freeze, filter --------------------------------------------
	widths := []float64{6, 20}
	for _, c := range t.Columns {
		// Wide enough for the label, within reason — the longest
		// Penggunaan Bangunan label is ~100 characters and would otherwise
		// make a column a screen wide.
		w := float64(len([]rune(c)))*1.1 + 2
		if w < 12 {
			w = 12
		}
		if w > 40 {
			w = 40
		}
		widths = append(widths, w)
	}
	widths = append(widths, 12)
	for ci, w := range widths {
		col, err := excelize.ColumnNumberToName(ci + 1)
		if err != nil {
			return err
		}
		if err := f.SetColWidth(sheet, col, col, w); err != nil {
			return err
		}
	}

	// Header row stays put, and so does ID SUBSLS on the left — the same
	// two sticky edges the on-screen table has.
	if err := f.SetPanes(sheet, &excelize.Panes{
		Freeze: true, Split: false, XSplit: 2, YSplit: headerRow,
		TopLeftCell: fmt.Sprintf("C%d", headerRow+1), ActivePane: "bottomRight",
	}); err != nil {
		return err
	}
	if len(t.Rows) > 0 {
		lastCol, err := excelize.ColumnNumberToName(len(headers))
		if err != nil {
			return err
		}
		// AutoFilter over the data only, so the totals row doesn't get
		// sorted into the middle of the SubSLS.
		if err := f.AutoFilter(sheet, fmt.Sprintf("A%d:%s%d", headerRow, lastCol, headerRow+len(t.Rows)), nil); err != nil {
			return err
		}
	}
	return nil
}
