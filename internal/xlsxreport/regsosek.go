package xlsxreport

import (
	"fmt"
	"io"

	"github.com/xuri/excelize/v2"

	"se2026-titik-maps/internal/regsosek"
	"se2026-titik-maps/internal/report"
)

const regsosekSheetName = "Daftar Match Regsosek"

// regsosekSubtitle matches the line the PDF prints under its title, so the
// two exports describe the same thing in the same words.
const regsosekSubtitle = "Daftar keluarga SE2026 dengan status tidak ditemukan, tapi ditemukan di Reg2022."

// regsosekColumns keeps Assignment ID — unlike the PDF, which drops it.
// Same reasoning as the other Excel export: a spreadsheet is for further
// lookup and cross-referencing, where an id earns its column.
var regsosekColumns = []struct {
	header string
	width  float64
}{
	{"No", 6},
	{"Nama", 32},
	{"Nama KK", 28},
	{"ID SubSLS", 20},
	{"Match Status", 30},
	{"Alamat Regsosek", 40},
	{"Nama Matched Regsosek", 30},
	{"Assignment ID", 38},
}

// GenerateRegsosek writes the "Daftar Match Regsosek" workbook to w,
// reusing this package's styles and info block so it matches the other
// Excel export everywhere but the columns.
func GenerateRegsosek(w io.Writer, region report.Region, rows []regsosek.Row, truncated bool) error {
	f := excelize.NewFile()
	defer f.Close()

	sheet := regsosekSheetName
	if err := f.SetSheetName(f.GetSheetName(0), sheet); err != nil {
		return fmt.Errorf("xlsxreport: rename sheet: %w", err)
	}

	styles, err := newStyles(f)
	if err != nil {
		return fmt.Errorf("xlsxreport: styles: %w", err)
	}

	headerRow, err := writeRegsosekInfoBlock(f, sheet, styles, region, len(rows), truncated)
	if err != nil {
		return fmt.Errorf("xlsxreport: info block: %w", err)
	}

	for i, c := range regsosekColumns {
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
			report.DashIfEmpty(r.Nama),
			report.DashIfEmpty(r.NamaKK),
			report.DashIfEmpty(r.SubSLS),
			report.DashIfEmpty(r.MatchStatus),
			report.DashIfEmpty(r.AlamatRegsosek),
			report.DashIfEmpty(r.NamaMatched),
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

	lastCol, err := excelize.ColumnNumberToName(len(regsosekColumns))
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

// writeRegsosekInfoBlock is writeInfoBlock plus the subtitle, and using
// this table's own row cap in the truncation note.
func writeRegsosekInfoBlock(f *excelize.File, sheet string, st styles, region report.Region, total int, truncated bool) (int, error) {
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

	set(fmt.Sprintf("A%d", row), st.title, regsosekSheetName)
	row++
	set(fmt.Sprintf("A%d", row), st.footnote, regsosekSubtitle)
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
		set(fmt.Sprintf("A%d", row), st.warn, fmt.Sprintf("Catatan: daftar ini dibatasi hingga %d baris pertama.", regsosek.ReportMaxRows))
		row++
	}

	row += 2
	return row, nil
}
