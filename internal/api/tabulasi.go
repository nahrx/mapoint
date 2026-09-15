package api

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"se2026-titik-maps/internal/points"
)

// tabulasiSort is the sort order of the Tabulasi table. Every column is
// sortable: the four wilayah names, the SubSLS code, each category, and
// the total. Sorting happens here rather than in ClickHouse because the
// name columns come from PostGIS — ClickHouse has only the codes — and a
// sort that is server-side for some columns and impossible for others
// would be a strange table. The whole table is fetched (cheap, see
// points.TabulasiAll), named, sorted, then sliced to a page.
type tabulasiSort struct {
	// key is one of the tabulasiSortKeys; "category" sorts by the count
	// in one category, named by category.
	key      string
	category string
	desc     bool
}

// tabulasiSortKeys are the accepted sortBy values. "category" additionally
// needs ?category=, validated against the table's actual columns once the
// table is in hand (see handleTabulasi) — so a preset saved on one tab
// can't smuggle a category the current variable doesn't have.
var tabulasiSortKeys = map[string]bool{
	"kabkota": true, "kecamatan": true, "desa": true, "sls": true,
	"subsls": true, "total": true, "category": true,
}

// parseTabulasiSort reads sortBy / category / dir. An unknown sortBy is an
// error rather than a fallback: silently sorting by something else would
// make the arrows on the table lie.
func parseTabulasiSort(q url.Values) (tabulasiSort, error) {
	s := tabulasiSort{key: "subsls"}
	if v := q.Get("sortBy"); v != "" {
		if !tabulasiSortKeys[v] {
			return tabulasiSort{}, fmt.Errorf("unknown sortBy %q", v)
		}
		s.key = v
	}
	if s.key == "category" {
		raw := q.Get("category")
		if raw == "" {
			return tabulasiSort{}, fmt.Errorf("sortBy=category requires category=")
		}
		// The same sentinel the filters use for "the blank value", so the
		// blank column is addressable without an empty query parameter.
		if raw == points.EmptyValue {
			raw = ""
		}
		s.category = raw
	}
	s.desc = points.ParseSortDir(q.Get("dir")) == points.SortDesc
	return s, nil
}

// sortTabulasiRows orders rows in place. Text columns compare
// case-insensitively with blanks last in either direction — a blank name
// is "no polygon in the layer", not a name that sorts before "A" — and
// every order is made total by falling back to the SubSLS code, so paging
// through equal values is stable.
func sortTabulasiRows(rows []points.TabulasiRow, s tabulasiSort) {
	text := func(get func(r *points.TabulasiRow) string) func(a, b *points.TabulasiRow) int {
		return func(a, b *points.TabulasiRow) int {
			x, y := get(a), get(b)
			switch {
			case x == "" && y == "":
				return 0
			case x == "":
				return 1 // blank last regardless of direction
			case y == "":
				return -1
			}
			c := strings.Compare(strings.ToLower(x), strings.ToLower(y))
			if s.desc {
				c = -c
			}
			return c
		}
	}
	num := func(get func(r *points.TabulasiRow) uint64) func(a, b *points.TabulasiRow) int {
		return func(a, b *points.TabulasiRow) int {
			x, y := get(a), get(b)
			c := 0
			if x < y {
				c = -1
			} else if x > y {
				c = 1
			}
			if s.desc {
				c = -c
			}
			return c
		}
	}

	var cmp func(a, b *points.TabulasiRow) int
	switch s.key {
	case "kabkota":
		cmp = text(func(r *points.TabulasiRow) string { return r.KabKotaName })
	case "kecamatan":
		cmp = text(func(r *points.TabulasiRow) string { return r.KecamatanName })
	case "desa":
		cmp = text(func(r *points.TabulasiRow) string { return r.DesaName })
	case "sls":
		cmp = text(func(r *points.TabulasiRow) string { return r.SLSName })
	case "total":
		cmp = num(func(r *points.TabulasiRow) uint64 { return r.Total })
	case "category":
		cat := s.category
		cmp = num(func(r *points.TabulasiRow) uint64 { return r.Counts[cat] })
	default: // subsls
		cmp = func(a, b *points.TabulasiRow) int {
			c := strings.Compare(a.SubSLS, b.SubSLS)
			if s.desc {
				c = -c
			}
			return c
		}
	}

	sort.SliceStable(rows, func(i, j int) bool {
		if c := cmp(&rows[i], &rows[j]); c != 0 {
			return c < 0
		}
		return rows[i].SubSLS < rows[j].SubSLS
	})
}

// pageOf slices rows to one page. page is 1-indexed; a page past the end
// is empty rather than an error, the same as /api/list.
func pageOf(rows []points.TabulasiRow, page, pageSize int) []points.TabulasiRow {
	start := (page - 1) * pageSize
	if start >= len(rows) {
		return []points.TabulasiRow{}
	}
	end := start + pageSize
	if end > len(rows) {
		end = len(rows)
	}
	return rows[start:end]
}
