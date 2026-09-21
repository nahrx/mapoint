// Package xlsxreport renders the "Daftar Hasil Pendataan" Excel workbook
// for the Daftar menu's download button — same data and scope as
// pdfreport (see report.Region), just as an .xlsx sheet instead of a
// printable PDF. Assignment ID is included as its own column here but not
// in the PDF: a spreadsheet is meant for further sorting, filtering and
// lookup, not just reading on paper, so it pulls its weight here in a way
// it wouldn't on a printed page. ID SUBSLS is always a column here too,
// where the PDF only adds one for reports spanning more than one SubSLS.
// Catatan is a column in both formats. Keberadaan Usaha is temporarily
// dropped from both — see points.Point's doc comment on that field.
package xlsxreport

import (
	"fmt"
	"io"
	"iter"
	"math"
	"time"

	"github.com/xuri/excelize/v2"

	"se2026-titik-maps/internal/points"
	"se2026-titik-maps/internal/report"
)

const sheetName = "Daftar Hasil Pendataan"

// coordCell renders one coordinate for a spreadsheet cell: the number
// itself when there is one, an empty cell when there isn't. Rows with no
// usable coordinate carry 0 in both columns (see points.validCoords), and
// a literal 0 in a Latitude column reads as a real fix on the equator —
// worse than a blank. Non-finite values (a handful of rows) are blanked
// for the same reason.
func coordCell(v float64) any {
	if v == 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return ""
	}
	return v
}

// flagWord renders one of the two membership flags for a spreadsheet cell.
func flagWord(ada bool) string {
	if ada {
		return "Ya"
	}
	return "Tidak"
}

var columns = []struct {
	header string
	width  float64
}{
	{"No", 6},
	{"Nama", 32},
	{"Alamat", 40},
	{"ID SUBSLS", 20},
	{"Latitude", 13},
	{"Longitude", 13},
	{"Nomor Bangunan", 14},
	{"Jenis Prelist", 18},
	{"Penggunaan Bangunan", 46},
	{"Keberadaan Keluarga", 34},
	{"Keberadaan Usaha", 34},
	{"Status", 34},
	{"Non Respon", 12},
	{"Assignment ID", 38},
	{"Ditemukan di Assignment Baru", 24},
	{"Assignment ID Baru", 38},
	{"Ditemukan di Regsosek", 21},
	{"Catatan", 40},
}

// Generate writes the report workbook to w. rows should already come in
// the order the report should read (see points.Service.ListAll); total is
// how many of them there are, for the "Jumlah Data" line, which is written
// before the first row is seen.
//
// Written through excelize's StreamWriter, not the cell-by-cell API: a
// whole kabupaten/kota is 552k rows × 18 columns, and the cell map the
// ordinary API keeps for that (every cell an object) runs to gigabytes,
// while the stream writer serialises each row as it comes and holds
// nothing. Same reason the Tabulasi workbook uses it. The constraints
// that brings are the same as there — column widths and panes before the
// first SetRow, the AutoFilter as an Excel table after the last, Flush
// once at the end — see tabulasi.go.
func Generate(w io.Writer, region report.Region, total int, rows iter.Seq[points.Point], truncated bool) error {
	f := excelize.NewFile()
	defer f.Close()

	sheet := sheetName
	if err := f.SetSheetName(f.GetSheetName(0), sheet); err != nil {
		return fmt.Errorf("xlsxreport: rename sheet: %w", err)
	}

	st, err := newStyles(f)
	if err != nil {
		return fmt.Errorf("xlsxreport: styles: %w", err)
	}

	sw, err := f.NewStreamWriter(sheet)
	if err != nil {
		return fmt.Errorf("xlsxreport: stream writer: %w", err)
	}
	for i, c := range columns {
		if err := sw.SetColWidth(i+1, i+1, c.width); err != nil {
			return fmt.Errorf("xlsxreport: column width: %w", err)
		}
	}

	// Keep everything from the title row down through the table header
	// visible while scrolling through data rows — the same sticky-header
	// idea the Daftar table itself uses (see #daftar-table thead th in
	// style.css), just Excel's version of it. The stream writer wants the
	// panes before the first row, so the info block's height is computed
	// first (infoRows) and written afterwards.
	headerRow := infoBlockHeight(region, truncated)
	if err := sw.SetPanes(&excelize.Panes{
		Freeze: true, Split: false, XSplit: 0, YSplit: headerRow,
		TopLeftCell: fmt.Sprintf("A%d", headerRow+1), ActivePane: "bottomLeft",
	}); err != nil {
		return fmt.Errorf("xlsxreport: freeze panes: %w", err)
	}

	if err := writeInfoBlock(sw, st, region, total, truncated, headerRow); err != nil {
		return fmt.Errorf("xlsxreport: info block: %w", err)
	}
	n, err := writeTable(sw, st, headerRow, rows)
	if err != nil {
		return fmt.Errorf("xlsxreport: table: %w", err)
	}

	// An Excel table is the stream writer's way of getting AutoFilter on
	// the header. Excel refuses a table with no data rows, so an empty
	// report just has a plain header.
	if n > 0 {
		lastCol, err := excelize.ColumnNumberToName(len(columns))
		if err != nil {
			return fmt.Errorf("xlsxreport: table range: %w", err)
		}
		noStripes := false
		if err := sw.AddTable(&excelize.Table{
			Range:          fmt.Sprintf("A%d:%s%d", headerRow, lastCol, headerRow+n),
			Name:           "daftar",
			StyleName:      "TableStyleLight1",
			ShowRowStripes: &noStripes,
		}); err != nil {
			return fmt.Errorf("xlsxreport: table: %w", err)
		}
	}
	if err := sw.Flush(); err != nil {
		return fmt.Errorf("xlsxreport: flush: %w", err)
	}

	if err := f.Write(w); err != nil {
		return fmt.Errorf("xlsxreport: write: %w", err)
	}
	return nil
}

// styles bundles the cell style IDs Generate needs, created once per
// workbook (excelize styles are workbook-scoped, not reusable across
// files).
type styles struct {
	title    int
	section  int
	label    int
	tblHead  int
	tblCell  int
	tblFill  int
	warn     int
	footnote int
}

func newStyles(f *excelize.File) (styles, error) {
	var s styles
	var err error

	newStyle := func(style *excelize.Style) int {
		if err != nil {
			return 0
		}
		var id int
		id, err = f.NewStyle(style)
		return id
	}

	s.title = newStyle(&excelize.Style{Font: &excelize.Font{Bold: true, Size: 16}})
	s.section = newStyle(&excelize.Style{Font: &excelize.Font{Bold: true, Size: 11}})
	s.label = newStyle(&excelize.Style{Font: &excelize.Font{Size: 10, Color: "555555"}})
	border := []excelize.Border{
		{Type: "left", Color: "cccccc", Style: 1},
		{Type: "top", Color: "cccccc", Style: 1},
		{Type: "right", Color: "cccccc", Style: 1},
		{Type: "bottom", Color: "cccccc", Style: 1},
	}
	s.tblHead = newStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Size: 10},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"e6e6e6"}, Pattern: 1},
		Border:    border,
		Alignment: &excelize.Alignment{Vertical: "center"},
	})
	s.tblCell = newStyle(&excelize.Style{Font: &excelize.Font{Size: 10}, Border: border, Alignment: &excelize.Alignment{Vertical: "top", WrapText: true}})
	s.tblFill = newStyle(&excelize.Style{
		Font:      &excelize.Font{Size: 10},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"f7f7f7"}, Pattern: 1},
		Border:    border,
		Alignment: &excelize.Alignment{Vertical: "top", WrapText: true},
	})
	s.warn = newStyle(&excelize.Style{Font: &excelize.Font{Italic: true, Size: 9, Color: "b43c1e"}})
	s.footnote = newStyle(&excelize.Style{Font: &excelize.Font{Italic: true, Size: 9, Color: "888888"}})

	return s, err
}

// infoBlockLines lists the info block's rows top to bottom — nil for a
// blank row, otherwise the cells of that row — so the block's height is
// known before anything is written (SetPanes needs it) and writing it is a
// plain loop over the same list.
func infoBlockLines(st styles, region report.Region, total int, truncated bool) [][]excelize.Cell {
	cell := func(style int, v any) excelize.Cell { return excelize.Cell{StyleID: style, Value: v} }
	kv := func(e [2]string) []excelize.Cell { return []excelize.Cell{cell(st.label, e[0]), cell(st.label, e[1])} }

	lines := [][]excelize.Cell{
		{cell(st.title, "Daftar Hasil Pendataan")},
		nil,
		{cell(st.section, "Keterangan Wilayah")},
	}
	for _, e := range region.WilayahRows(total) {
		lines = append(lines, kv(e))
	}
	if extra := region.ExtraFilters(); len(extra) > 0 {
		lines = append(lines, nil, []excelize.Cell{cell(st.section, "Filter Tambahan")})
		for _, e := range extra {
			lines = append(lines, kv(e))
		}
	}
	if truncated {
		lines = append(lines, nil, []excelize.Cell{cell(st.warn, fmt.Sprintf("Catatan: daftar ini dibatasi hingga %d baris pertama.", points.ReportMaxRows))})
	}
	lines = append(lines, nil,
		[]excelize.Cell{cell(st.footnote, fmt.Sprintf("Diunduh %s — Peta Titik SE2026", time.Now().Format("2 January 2006 15:04")))},
		nil)
	return lines
}

// infoBlockHeight is the row the table header lands on: one past the info
// block. Computed from the same list writeInfoBlock writes, so the two
// cannot disagree. Styles don't affect the count, hence the zero value.
func infoBlockHeight(region report.Region, truncated bool) int {
	return len(infoBlockLines(styles{}, region, 0, truncated)) + 1
}

// writeInfoBlock renders the title and "Keterangan Wilayah" / "Filter
// Tambahan" key-value rows — the spreadsheet's counterpart to pdfreport's
// writeHeader. headerRow is what infoBlockHeight returned for the same
// inputs; it is checked rather than trusted because the panes were already
// set from it.
func writeInfoBlock(sw *excelize.StreamWriter, st styles, region report.Region, total int, truncated bool, headerRow int) error {
	lines := infoBlockLines(st, region, total, truncated)
	if len(lines)+1 != headerRow {
		return fmt.Errorf("info block is %d rows but panes were set for %d", len(lines)+1, headerRow)
	}
	for i, cells := range lines {
		if cells == nil {
			continue
		}
		if err := sw.SetRow(fmt.Sprintf("A%d", i+1), cellsAny(cells)); err != nil {
			return err
		}
	}
	return nil
}

// cellsAny is the []any the stream writer wants for a row of styled cells.
func cellsAny(cells []excelize.Cell) []any {
	out := make([]any, len(cells))
	for i, c := range cells {
		out[i] = c
	}
	return out
}

// writeTable renders the column header row at headerRow and one data row
// per item below it, zebra-striped the same way the PDF table is. Returns
// how many data rows were written.
func writeTable(sw *excelize.StreamWriter, st styles, headerRow int, rows iter.Seq[points.Point]) (int, error) {
	head := make([]excelize.Cell, len(columns))
	for i, c := range columns {
		head[i] = excelize.Cell{StyleID: st.tblHead, Value: c.header}
	}
	if err := sw.SetRow(fmt.Sprintf("A%d", headerRow), cellsAny(head)); err != nil {
		return 0, err
	}

	n := 0
	cells := make([]any, len(columns))
	for p := range rows {
		style := st.tblCell
		if n%2 == 1 {
			style = st.tblFill
		}
		values := [...]any{
			n + 1,
			report.DashIfEmpty(p.Nama),
			report.DashIfEmpty(p.Alamat),
			report.DashIfEmpty(p.SubSLS),
			coordCell(p.Lat), coordCell(p.Lon),
			int(p.NomorBangunan),
			report.DashIfEmpty(p.JenisPrelist),
			report.DashIfEmpty(p.PenggunaanBangunan),
			report.DashIfEmpty(p.KeberadaanKeluarga),
			report.DashIfEmpty(p.KeberadaanBKU),
			report.DashIfEmpty(p.Status),
			flagWord(p.NonRespon),
			report.DashIfEmpty(p.AssignmentID),
			// Spelled out rather than a checkmark glyph: these two columns
			// are meant to be filtered and pivoted on in Excel, where a
			// word sorts and groups more usefully than a symbol.
			flagWord(p.AdaAssignmentBaru),
			report.DashIfEmpty(p.AssignmentIDBaru),
			flagWord(p.AdaRegsosek),
			report.DashIfEmpty(p.Catatan),
		}
		for ci, v := range values {
			cells[ci] = excelize.Cell{StyleID: style, Value: v}
		}
		if err := sw.SetRow(fmt.Sprintf("A%d", headerRow+1+n), cells); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
