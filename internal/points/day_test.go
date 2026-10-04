package points

import (
	"strings"
	"testing"
)

// Every read of se2026_titik2 must be pinned to one created_at day, or a
// reload (which appends a whole new copy of the dataset rather than
// replacing the old one) would make every menu count its rows twice. The
// day itself is covered by RefreshLatestDay against the live table; what
// this file guards is the SQL those two helpers build.

func TestWhereForPinsTheDay(t *testing.T) {
	var s Service
	day := "2026-10-03"
	s.latestDay.Store(&day)

	t.Run("empty filter", func(t *testing.T) {
		where, args := s.whereFor(Filter{})
		if want := "1 AND toDate(created_at) = toDate('2026-10-03')"; where != want {
			t.Errorf("where = %q, want %q", where, want)
		}
		if len(args) != 0 {
			t.Errorf("args = %v, want none", args)
		}
	})

	t.Run("keeps the filter's own clauses and bind args", func(t *testing.T) {
		where, args := s.whereFor(Filter{KabKota: "6411", Search: "wozmi"})
		if !strings.HasPrefix(where, "startsWith(level_6_full_code, '6411')") {
			t.Errorf("wilayah prefix missing from %q", where)
		}
		if !strings.Contains(where, "positionCaseInsensitive(nama_assignment, ?)") {
			t.Errorf("search fragment missing from %q", where)
		}
		if !strings.HasSuffix(where, "AND toDate(created_at) = toDate('2026-10-03')") {
			t.Errorf("day pin missing from %q", where)
		}
		// The search text must still travel as a bind arg, never inlined.
		if len(args) != 1 || args[0] != "wozmi" {
			t.Errorf("args = %v, want [wozmi]", args)
		}
		if strings.Contains(where, "wozmi") {
			t.Errorf("search text was interpolated into %q", where)
		}
	})
}

// With no day known — the probe has not run, or the table has no usable
// created_at — the queries must fall back to filtering nothing. An empty
// dashboard is a far worse failure than one showing every batch.
func TestDayClauseFallsBackToNoFilter(t *testing.T) {
	var s Service
	if got := s.dayClause(); got != "1" {
		t.Errorf("dayClause() = %q, want %q", got, "1")
	}
	if got := s.LatestDay(); got != "" {
		t.Errorf("LatestDay() = %q, want empty", got)
	}
	where, _ := s.whereFor(Filter{})
	if where != "1 AND 1" {
		t.Errorf("where = %q, want %q", where, "1 AND 1")
	}
}
