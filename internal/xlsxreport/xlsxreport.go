// Package xlsxreport renders the "Daftar Hasil Pendataan" Excel workbook
// for the Daftar menu's download button — same data and scope as
// pdfreport (see report.Region), just as an .xlsx sheet instead of a
// printable PDF. Unlike the PDF, Assignment ID and ID SUBSLS are included
// as their own columns here: a spreadsheet is meant for further sorting,
// filtering and lookup, not just reading on paper, so the extra columns
// pull their weight here in a way they wouldn't on a printed page.
package xlsxreport

import (
	"fmt"
	"io"
	"time"

	"github.com/xuri/excelize/v2"

	"se2026-titik-maps/internal/points"
	"se2026-titik-maps/internal/report"
)

const sheetName = "Daftar Hasil Pendataan"

var columns = []struct {
	header string
	width  float64
}{
	{"No", 6},
	{"Nama", 32},
	{"Alamat", 40},
	{"ID SUBSLS", 20},
	{"Jenis Prelist", 18},
	{"Keberadaan Usaha", 16},
	{"Keberadaan Keluarga", 34},
	{"Status", 34},
	{"Assignment ID", 38},
}

// Generate writes the report workbook to w. items should already be
// sorted the way the report should read (see points.Service.ListAll).
func Generate(w io.Writer, region report.Region, items []points.Point, truncated bool) error {
	f := excelize.NewFile()
	defer f.Close()

	sheet := sheetName
	if err := f.SetSheetName(f.GetSheetName(0), sheet); err != nil {
		return fmt.Errorf("xlsxreport: rename sheet: %w", err)
	}

	styles, err := newStyles(f)
	if err != nil {
		return fmt.Errorf("xlsxreport: styles: %w", err)
	}

	headerRow, err := writeInfoBlock(f, sheet, styles, region, len(items), truncated)
	if err != nil {
		return fmt.Errorf("xlsxreport: info block: %w", err)
	}
	if err := writeTable(f, sheet, styles, headerRow, items); err != nil {
		return fmt.Errorf("xlsxreport: table: %w", err)
	}

	for i, c := range columns {
		col, err := excelize.ColumnNumberToName(i + 1)
		if err != nil {
			return fmt.Errorf("xlsxreport: column width: %w", err)
		}
		if err := f.SetColWidth(sheet, col, col, c.width); err != nil {
			return fmt.Errorf("xlsxreport: column width: %w", err)
		}
	}

	// Keep everything from the title row down through the table header
	// visible while scrolling through data rows — the same sticky-header
	// idea the Daftar table itself uses (see #daftar-table thead th in
	// style.css), just Excel's version of it.
	if err := f.SetPanes(sheet, &excelize.Panes{
		Freeze: true, Split: false, XSplit: 0, YSplit: headerRow,
		TopLeftCell: fmt.Sprintf("A%d", headerRow+1), ActivePane: "bottomLeft",
	}); err != nil {
		return fmt.Errorf("xlsxreport: freeze panes: %w", err)
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

// writeInfoBlock renders the title and "Keterangan Wilayah" / "Filter
// Tambahan" key-value rows — the spreadsheet's counterpart to
// pdfreport's writeHeader — and returns the row number the table header
// should start on.
func writeInfoBlock(f *excelize.File, sheet string, st styles, region report.Region, total int, truncated bool) (int, error) {
	row := 1
	set := func(cell string, style int, value any) {
		f.SetCellValue(sheet, cell, value)
		f.SetCellStyle(sheet, cell, cell, style)
	}
	kv := func(label, value string) {
		set(fmt.Sprintf("A%d", row), st.label, label)
		set(fmt.Sprintf("B%d", row), st.label, value)
		row++
	}

	set(fmt.Sprintf("A%d", row), st.title, "Daftar Hasil Pendataan")
	row += 2

	set(fmt.Sprintf("A%d", row), st.section, "Keterangan Wilayah")
	row++
	kv("Kabupaten/Kota", fmt.Sprintf("%s (%s)", region.KabKotaName, region.KabKotaCode))
	kv("Kecamatan", region.Kecamatan)
	kv("Desa/Kelurahan", region.Desa)
	kv("SLS", region.SLS)
	kv("SubSLS", region.SubSLS)
	kv("Kode Wilayah (ID SUBSLS)", region.FullCode())
	kv("Jumlah Data", fmt.Sprintf("%d", total))

	if extra := region.ExtraFilters(); len(extra) > 0 {
		row++
		set(fmt.Sprintf("A%d", row), st.section, "Filter Tambahan")
		row++
		for _, e := range extra {
			kv(e[0], e[1])
		}
	}

	if truncated {
		row++
		set(fmt.Sprintf("A%d", row), st.warn, fmt.Sprintf("Catatan: daftar ini dibatasi hingga %d baris pertama.", points.ReportMaxRows))
		row++
	}

	row++
	set(fmt.Sprintf("A%d", row), st.footnote, fmt.Sprintf("Diunduh %s — Peta Titik SE2026", time.Now().Format("2 January 2006 15:04")))
	row += 2

	return row, nil
}

// writeTable renders the column header row at headerRow and one data row
// per item below it, zebra-striped the same way the PDF table is.
func writeTable(f *excelize.File, sheet string, st styles, headerRow int, items []points.Point) error {
	for i, c := range columns {
		col, err := excelize.ColumnNumberToName(i + 1)
		if err != nil {
			return err
		}
		cell := fmt.Sprintf("%s%d", col, headerRow)
		f.SetCellValue(sheet, cell, c.header)
		f.SetCellStyle(sheet, cell, cell, st.tblHead)
	}

	for i, p := range items {
		r := headerRow + 1 + i
		style := st.tblCell
		if i%2 == 1 {
			style = st.tblFill
		}
		values := []any{
			i + 1,
			report.DashIfEmpty(p.Nama),
			report.DashIfEmpty(p.Alamat),
			report.DashIfEmpty(p.SubSLS),
			report.DashIfEmpty(p.JenisPrelist),
			int(p.KeberadaanUsaha),
			report.DashIfEmpty(p.KeberadaanKeluarga),
			report.DashIfEmpty(p.Status),
			report.DashIfEmpty(p.AssignmentID),
		}
		for ci, v := range values {
			col, err := excelize.ColumnNumberToName(ci + 1)
			if err != nil {
				return err
			}
			cell := fmt.Sprintf("%s%d", col, r)
			f.SetCellValue(sheet, cell, v)
			f.SetCellStyle(sheet, cell, cell, style)
		}
	}

	lastCol, err := excelize.ColumnNumberToName(len(columns))
	if err != nil {
		return err
	}
	lastRow := headerRow + len(items)
	return f.AutoFilter(sheet, fmt.Sprintf("A%d:%s%d", headerRow, lastCol, lastRow), nil)
}
