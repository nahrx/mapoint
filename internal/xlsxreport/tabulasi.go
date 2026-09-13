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
	// No SetActiveSheet here, on purpose. The first sheet is active by
	// default, and SetActiveSheet is not the cheap flag flip it looks like:
	// it calls workSheetReader on every sheet, which parses each streamed
	// sheet back out of its temp file into the in-memory model — and Write
	// then re-marshals all of them. Profiled on a province-sized workbook:
	// that one call was 6.6s of a 12.5s export, and Write another 4.1s of
	// re-marshalling it caused. Without it, the streamed sheets go into
	// the zip as they are.

	if err := f.Write(w); err != nil {
		return fmt.Errorf("xlsxreport: write: %w", err)
	}
	return nil
}

// writeTabulasiSheet renders one variable's table with excelize's
// StreamWriter rather than the cell-by-cell API the Daftar report uses.
// The difference is not cosmetic: a province-wide workbook is six sheets
// of 17,120 rows with ten string cells each, and the regular API — which
// deduplicates every string through the shared-string table and keeps the
// whole sheet in memory — took 18.5s for it. Streamed, the same workbook
// is written in a fraction of that (see README for the measured figure).
//
// The stream API has an order it insists on: column widths and panes
// before the first row, rows in ascending order, the table (which gives
// the header its filter buttons) after the rows, and Flush last.
func writeTabulasiSheet(f *excelize.File, sheet string, st styles, totalStyle int, scope TabulasiScope, t points.TabulasiTable) error {
	sw, err := f.NewStreamWriter(sheet)
	if err != nil {
		return err
	}

	headers := []string{"No", "Kabupaten/Kota", "Kecamatan", "Desa/Kelurahan", "Nama SLS", "ID SUBSLS"}
	for _, c := range t.Columns {
		if c == "" {
			c = "(Kosong)"
		}
		headers = append(headers, c)
	}
	headers = append(headers, "Total")

	// --- widths (must precede the first row) --------------------------------
	widths := []float64{6, 16, 18, 20, 24, 20}
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
		if err := sw.SetColWidth(ci+1, ci+1, w); err != nil {
			return err
		}
	}

	// --- panes --------------------------------------------------------------
	// The stream writer wants SetPanes before the very first SetRow — the
	// info block included — so the header row has to be known up front
	// rather than discovered by laying the block out. The layout below is
	// fixed (title, gap, section, five scope rows, two counts, optional
	// truncation note, gap, footnote, gap), and the running row counter is
	// checked against this number when the block is done, so a future edit
	// to the block can't quietly freeze the wrong row.
	//
	// Header row stays put, and so do the identifying columns (through ID
	// SUBSLS) on the left, so a row never loses its name while scrolling
	// across the categories.
	headerRow := 14
	if t.Truncated {
		headerRow += 2
	}
	if err := sw.SetPanes(&excelize.Panes{
		Freeze: true, Split: false, XSplit: 6, YSplit: headerRow,
		TopLeftCell: fmt.Sprintf("G%d", headerRow+1), ActivePane: "bottomRight",
	}); err != nil {
		return err
	}

	// --- info block ---------------------------------------------------------
	row := 1
	put := func(r int, cells ...any) error {
		cell, err := excelize.CoordinatesToCellName(1, r)
		if err != nil {
			return err
		}
		return sw.SetRow(cell, cells)
	}
	styled := func(style int, v any) excelize.Cell { return excelize.Cell{StyleID: style, Value: v} }

	if err := put(row, styled(st.title, fmt.Sprintf("Tabulasi %s per SubSLS", t.Variable.Label))); err != nil {
		return err
	}
	row += 2
	if err := put(row, styled(st.section, "Keterangan Wilayah")); err != nil {
		return err
	}
	row++
	for _, e := range scope.rows() {
		if err := put(row, styled(st.label, e[0]), styled(st.label, e[1])); err != nil {
			return err
		}
		row++
	}
	if err := put(row, styled(st.label, "Jumlah SubSLS"), styled(st.label, int64(t.TotalRows))); err != nil {
		return err
	}
	row++
	if err := put(row, styled(st.label, "Jumlah Data"), styled(st.label, int64(t.GrandTotal))); err != nil {
		return err
	}
	row++
	if t.Truncated {
		row++
		if err := put(row, styled(st.warn, fmt.Sprintf("Catatan: tabel ini dibatasi hingga %d SubSLS pertama.", points.TabulasiMaxRows))); err != nil {
			return err
		}
		row++
	}
	row++
	if err := put(row, styled(st.footnote, fmt.Sprintf("Diunduh %s — Peta Titik SE2026", time.Now().Format("2 January 2006 15:04")))); err != nil {
		return err
	}
	row += 2
	if row != headerRow {
		return fmt.Errorf("info block ended on row %d, panes were frozen at %d", row, headerRow)
	}

	// --- header row ---------------------------------------------------------
	hdr := make([]any, len(headers))
	for i, h := range headers {
		hdr[i] = styled(st.tblHead, h)
	}
	if err := put(headerRow, hdr...); err != nil {
		return err
	}

	// --- data rows ----------------------------------------------------------
	for ri, r := range t.Rows {
		style := st.tblCell
		if ri%2 == 1 {
			style = st.tblFill
		}
		cells := make([]any, 0, len(headers))
		cells = append(cells,
			styled(style, ri+1), styled(style, r.KabKotaName), styled(style, r.KecamatanName),
			styled(style, r.DesaName), styled(style, r.SLSName), styled(style, r.SubSLS))
		for _, c := range t.Columns {
			cells = append(cells, styled(style, int64(r.Counts[c])))
		}
		cells = append(cells, styled(style, int64(r.Total)))
		if err := put(headerRow+1+ri, cells...); err != nil {
			return err
		}
	}

	// --- totals row ---------------------------------------------------------
	totals := make([]any, 0, len(headers))
	totals = append(totals, styled(totalStyle, ""), styled(totalStyle, "Total"),
		styled(totalStyle, ""), styled(totalStyle, ""), styled(totalStyle, ""), styled(totalStyle, ""))
	for _, c := range t.Columns {
		totals = append(totals, styled(totalStyle, int64(t.Grand[c])))
	}
	totals = append(totals, styled(totalStyle, int64(t.GrandTotal)))
	if err := put(headerRow+1+len(t.Rows), totals...); err != nil {
		return err
	}

	// --- filter buttons on the header, over the data only --------------------
	// An Excel table is the stream writer's way of getting AutoFilter; its
	// range stops above the totals row so that sorting can't pull the
	// total into the middle of the SubSLS. Table names must be unique in
	// the workbook and identifier-like, hence the key rather than the label.
	if len(t.Rows) > 0 {
		lastCol, err := excelize.ColumnNumberToName(len(headers))
		if err != nil {
			return err
		}
		noStripes := false
		if err := sw.AddTable(&excelize.Table{
			Range:          fmt.Sprintf("A%d:%s%d", headerRow, lastCol, headerRow+len(t.Rows)),
			Name:           "tab_" + t.Variable.Key,
			StyleName:      "TableStyleLight1",
			ShowRowStripes: &noStripes,
		}); err != nil {
			return err
		}
	}

	return sw.Flush()
}
