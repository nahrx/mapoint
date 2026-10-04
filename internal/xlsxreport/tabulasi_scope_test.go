package xlsxreport

import (
	"io"
	"testing"

	"se2026-titik-maps/internal/points"
)

// GenerateTabulasi has to freeze the panes before it writes the first row,
// so the header row is computed from scope.rows() rather than discovered.
// That coupling is easy to miss: adding the "Data per tanggal" row for the
// day picker made a previously hardcoded 14 wrong, and every workbook came
// out zero bytes. The runtime consistency check caught it, but only once a
// download was attempted — this catches it at build time, for a scope with
// the day row and one without, truncated and not.
func TestGenerateTabulasiHeaderRowMatchesScope(t *testing.T) {
	for _, tc := range []struct {
		name      string
		scope     TabulasiScope
		truncated bool
	}{
		{"tanpa hari", TabulasiScope{KabKotaCode: "6411", KabKotaName: "Mahakam Ulu"}, false},
		{"dengan hari", TabulasiScope{KabKotaCode: "6411", KabKotaName: "Mahakam Ulu", Day: "2026-10-03"}, false},
		{"dengan hari, terpotong", TabulasiScope{Day: "2026-10-03"}, true},
		{"scope kosong", TabulasiScope{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := GenerateTabulasi(io.Discard, tc.scope, tabulasiFixture(tc.truncated)); err != nil {
				t.Fatalf("GenerateTabulasi: %v", err)
			}
		})
	}
}

// tabulasiFixture is the smallest workbook that still exercises the header
// row, the data rows and the totals row.
func tabulasiFixture(truncated bool) []points.TabulasiTable {
	vars := points.TabulasiVariables()
	tables := make([]points.TabulasiTable, 0, len(vars))
	for _, v := range vars {
		cols := append(append([]string{}, v.Values...), "")
		counts := make(map[string]uint64, len(cols))
		grand := make(map[string]uint64, len(cols))
		for i, c := range cols {
			counts[c] = uint64(i)
			grand[c] = uint64(i)
		}
		tables = append(tables, points.TabulasiTable{
			Variable: v, Columns: cols,
			Rows: []points.TabulasiRow{{
				KabKotaName: "Mahakam Ulu", KecamatanName: "LAHAM",
				DesaName: "NYARIBUNGAN", SLSName: "RT 01",
				SubSLS: "6411010001000100", Counts: counts, Total: 7,
			}},
			TotalRows: 1, Grand: grand, GrandTotal: 7, Truncated: truncated,
		})
	}
	return tables
}
