package xlsxreport

import (
	"fmt"
	"io"
	"testing"
	"time"

	"se2026-titik-maps/internal/points"
)

// Synthetic province-sized workbook: six sheets of 17,120 SubSLS with the
// real column counts, no database. Isolates the excelize cost from the
// ClickHouse/PostGIS cost so a slow export can be blamed correctly.
func TestGenerateTabulasiProvinceTiming(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test")
	}
	vars := points.TabulasiVariables()
	const n = 17120
	tables := make([]points.TabulasiTable, 0, len(vars))
	for _, v := range vars {
		cols := append(append([]string{}, v.Values...), "")
		rows := make([]points.TabulasiRow, n)
		grand := map[string]uint64{}
		for i := range rows {
			counts := make(map[string]uint64, len(cols))
			for j, c := range cols {
				counts[c] = uint64((i + j) % 37)
				grand[c] += counts[c]
			}
			rows[i] = points.TabulasiRow{
				KabKotaName: "Kutai Kartanegara", KecamatanName: "TENGGARONG SEBERANG",
				DesaName: "BUKIT PARIAMAN", SLSName: fmt.Sprintf("RT %03d DUSUN MEKAR SARI", i%200),
				SubSLS: fmt.Sprintf("64%014d", i), Counts: counts, Total: 500,
			}
		}
		tables = append(tables, points.TabulasiTable{Variable: v, Columns: cols, Rows: rows, TotalRows: n, Grand: grand, GrandTotal: 500 * n})
	}
	start := time.Now()
	if err := GenerateTabulasi(io.Discard, TabulasiScope{}, tables); err != nil {
		t.Fatal(err)
	}
	t.Logf("GenerateTabulasi: %d sheets x %d rows in %s", len(tables), n, time.Since(start).Round(time.Millisecond))
}
