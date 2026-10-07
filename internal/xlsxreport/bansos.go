package xlsxreport

import (
	"fmt"
	"io"

	"github.com/xuri/excelize/v2"

	"se2026-titik-maps/internal/bansos"
	"se2026-titik-maps/internal/report"
)

const bansosSheetName = "Daftar Bansos"

// bansosSubtitle matches the line the PDF prints under its title, so the
// two formats describe the same thing in the same words.
const bansosSubtitle = "Penerima bantuan sosial yang dicocokkan dengan prelist SE2026. " +
	"Kolom RT/RW/Alamat KTP hanya terisi kalau desa KTP sama dengan desa DTSEN."

// Assignment ID is here but not in the PDF — see the note on
// pdfreport.bansosColumns. A spreadsheet is where a join key is useful.
var bansosColumns = []struct {
	header string
	width  float64
}{
	{"No", 6},
	{"Nama", 32},
	{"ID SubSLS", 20},
	{"Kode Desa DTSEN", 18},
	{"Alamat DTSEN", 40},
	{"RT KTP", 10},
	{"RW KTP", 10},
	{"Alamat KTP", 40},
	{"Assignment ID", 38},
}

// GenerateBansos writes the "Daftar Bansos" workbook to w, with the same
// info block, freeze pane and AutoFilter as the other reports.
func GenerateBansos(w io.Writer, region report.Region, rows []bansos.Row, truncated bool) error {
	f := excelize.NewFile()
	defer f.Close()

	sheet := bansosSheetName
	if err := f.SetSheetName(f.GetSheetName(0), sheet); err != nil {
		return fmt.Errorf("xlsxreport: rename sheet: %w", err)
	}

	styles, err := newStyles(f)
	if err != nil {
		return fmt.Errorf("xlsxreport: styles: %w", err)
	}

	headerRow, err := writeBansosInfoBlock(f, sheet, styles, region, len(rows), truncated)
	if err != nil {
		return fmt.Errorf("xlsxreport: info block: %w", err)
	}

	for i, c := range bansosColumns {
		col, err := excelize.ColumnNumberToName(i + 1)
		if err != nil {
			return fmt.Errorf("xlsxreport: column width: %w", err)
		}
		if err := f.SetColWidth(sheet, col, col, c.width); err != nil {
			return fmt.Errorf("xlsxreport: column width: %w", err)
		}
		cell := fmt.Sprintf("%s%d", col, headerRow)
		f.SetCellValue(sheet, cell, c.header)
		f.SetCellStyle(sheet, cell, cell, styles.tblHead)
	}

	for i, r := range rows {
		rowNo := headerRow + 1 + i
		style := styles.tblCell
		if i%2 == 1 {
			style = styles.tblFill
		}
		values := []any{
			i + 1,
			report.DashIfEmpty(r.BansosNama),
			report.DashIfEmpty(r.SubSLS),
			report.DashIfEmpty(r.DtsenKodeDesa),
			report.DashIfEmpty(r.DtsenAlamat),
			// RT/RW stay strings: they are blank when the KTP desa differs,
			// and a blank cell is not a number.
			report.DashIfEmpty(r.RtKTP),
			report.DashIfEmpty(r.RwKTP),
			report.DashIfEmpty(r.AlamatKTP),
			report.DashIfEmpty(r.AssignmentID),
		}
		for ci, v := range values {
			col, err := excelize.ColumnNumberToName(ci + 1)
			if err != nil {
				return err
			}
			cell := fmt.Sprintf("%s%d", col, rowNo)
			f.SetCellValue(sheet, cell, v)
			f.SetCellStyle(sheet, cell, cell, style)
		}
	}

	lastCol, err := excelize.ColumnNumberToName(len(bansosColumns))
	if err != nil {
		return err
	}
	if err := f.AutoFilter(sheet, fmt.Sprintf("A%d:%s%d", headerRow, lastCol, headerRow+len(rows)), nil); err != nil {
		return fmt.Errorf("xlsxreport: autofilter: %w", err)
	}
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

// writeBansosInfoBlock is the same block the other reports print, with
// this table's own subtitle and row cap.
func writeBansosInfoBlock(f *excelize.File, sheet string, st styles, region report.Region, total int, truncated bool) (int, error) {
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

	set(fmt.Sprintf("A%d", row), st.title, bansosSheetName)
	row++
	set(fmt.Sprintf("A%d", row), st.footnote, bansosSubtitle)
	row += 2

	set(fmt.Sprintf("A%d", row), st.section, "Keterangan Wilayah")
	row++
	for _, e := range region.WilayahRows(total) {
		kv(e[0], e[1])
	}

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
		set(fmt.Sprintf("A%d", row), st.warn, fmt.Sprintf("Catatan: daftar ini dibatasi hingga %d baris pertama.", bansos.ReportMaxRows))
		row++
	}

	row += 2
	return row, nil
}
