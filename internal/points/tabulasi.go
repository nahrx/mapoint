package points

import (
	"context"
	"fmt"
	"sort"
)

// TabulasiVariable is one of the categorical variables the Tabulasi menu
// can cross-tabulate against SubSLS. Column is the SQL expression that
// yields the category — usually a bare column, but it can be any
// String-valued expression built from this package's own constants (the
// Regsosek flag is a dictionary lookup folded to "Ya"/"Tidak"). It is never
// anything a request supplied: Key is what the URL carries, and
// ParseTabulasiVariable is the only way from one to the other.
//
// Values is the display order of the categories, the same order the
// filter dropdowns use, so a tabulation column and a filter option line
// up. A category that never occurs still gets a column: a table whose
// shape changes from one wilayah to the next is much harder to compare
// across than one with a zero in it.
type TabulasiVariable struct {
	Key    string   `json:"key"`
	Label  string   `json:"label"`
	Column string   `json:"-"`
	Values []string `json:"values"`
}

var tabulasiVariables = []TabulasiVariable{
	{Key: "jenis_prelist", Label: "Jenis Prelist", Column: "jenis_prelist_root", Values: jenisPrelistValues},
	{Key: "penggunaan_bangunan", Label: "Penggunaan Bangunan", Column: "kode_penggunaan_bangunan_label", Values: penggunaanBangunanValues},
	{Key: "keberadaan_keluarga", Label: "Keberadaan Keluarga", Column: "keberadaan_keluarga", Values: keberadaanKeluargaValues},
	{Key: "keberadaan_bku", Label: "Keberadaan Usaha", Column: "keberadaan_BKU", Values: keberadaanBKUValues},
	{Key: "status", Label: "Status", Column: "assignment_status_alias", Values: statusValues},
	// Not a stored column: the same dictionary membership test the Daftar
	// and Peta menus use for their "Ditemukan di Regsosek" flag, turned
	// into a two-category variable. Folding to text here (rather than
	// tabulating the raw UInt8) keeps the scan path identical to every
	// other variable — groupArray(val) is always []string.
	{Key: "ada_regsosek", Label: "Ditemukan di Regsosek",
		Column: fmt.Sprintf("if(%s, %s, %s)", dictHasExpr(regsosekDict, "assignment_id"), "'Ya'", "'Tidak'"),
		Values: []string{"Ya", "Tidak"}},
}

// TabulasiVariables lists the variables in tab order, for the menu to
// build its tabs from rather than hardcoding a copy of this list.
func TabulasiVariables() []TabulasiVariable {
	return tabulasiVariables
}

// ParseTabulasiVariable resolves a URL key to its variable. Anything not in
// the list is an error, not a fallback — the key picks a SQL column, and a
// silent default would tabulate the wrong thing without saying so.
func ParseTabulasiVariable(key string) (TabulasiVariable, error) {
	for _, v := range tabulasiVariables {
		if v.Key == key {
			return v, nil
		}
	}
	return TabulasiVariable{}, fmt.Errorf("unknown tabulasi variable %q", key)
}

// TabulasiRow is one SubSLS: how many rows fall in each category of the
// variable, keyed by the category's raw value ("" for a blank column), plus
// the row total.
//
// The four name fields are not filled here — they come from the PostGIS
// layer, not ClickHouse, and this package only talks to ClickHouse. The API
// layer fills them after the fact (see fillTabulasiNames in internal/api);
// they stay "" when PostGIS is not configured.
type TabulasiRow struct {
	KabKotaName   string            `json:"kabkota_name"`
	KecamatanName string            `json:"kecamatan_name"`
	DesaName      string            `json:"desa_name"`
	SLSName       string            `json:"sls_name"`
	SubSLS        string            `json:"subsls"`
	Counts        map[string]uint64 `json:"counts"`
	Total         uint64            `json:"total"`
}

// TabulasiPage is one page of the cross-tabulation, the JSON shape of
// GET /api/tabulasi. Columns is the full category order the client should
// render, blank ("") last when any row in the whole filter has one; Grand
// holds the same categories summed over the whole filter, not just this
// page, so the footer stays correct while paging.
//
// Assembled by the API layer, not here: paging happens after sorting, and
// four of the sortable columns are wilayah names that come from PostGIS,
// which this package doesn't know about. This package returns the whole
// table (TabulasiAll); internal/api names, sorts and slices it.
type TabulasiPage struct {
	Variable   string            `json:"variable"`
	Columns    []string          `json:"columns"`
	Rows       []TabulasiRow     `json:"rows"`
	TotalRows  uint64            `json:"total_rows"`
	Grand      map[string]uint64 `json:"grand"`
	GrandTotal uint64            `json:"grand_total"`
	Page       int               `json:"page"`
	PageSize   int               `json:"page_size"`
}

// TabulasiTable is the whole cross-tabulation for one variable over one
// filter, with no paging — what the Excel export writes as one sheet.
// Truncated reports that TabulasiMaxRows cut it short.
type TabulasiTable struct {
	Variable   TabulasiVariable
	Columns    []string
	Rows       []TabulasiRow
	TotalRows  uint64
	Grand      map[string]uint64
	GrandTotal uint64
	Truncated  bool
}

// TabulasiMaxRows caps how many SubSLS TabulasiAll returns. The whole
// province is 17,120 SubSLS today, so this is headroom against the table
// growing, not a limit any real filter reaches — and a workbook that does
// hit it says so in its header rather than silently stopping short.
const TabulasiMaxRows = 50000

// TabulasiAll cross-tabulates v against level_6_full_code for the rows
// matching filter: one row per SubSLS, one column per category, every
// SubSLS up to TabulasiMaxRows, ordered by code. Both the on-screen table
// and the Excel export go through this — there is no server-side LIMIT
// path any more, because the table sorts on columns ClickHouse can't sort
// by (wilayah names), and measured against the live table the whole
// province's 17k rows cost 0.14s to fetch: the GROUP BY over 2.19M rows is
// the work, and a LIMIT saved only the transfer of the result.
//
// Three queries. The rows query groups twice — first (subsls, value) to
// count, then subsls to fold each SubSLS's counts into a pair of parallel
// arrays. Parallel arrays rather than groupArray of a tuple because the
// driver scans []string and []uint64 without ceremony, and a tuple would
// come back as untyped []any. The other two give the grand row and the
// SubSLS count.
func (s *Service) TabulasiAll(ctx context.Context, filter Filter, v TabulasiVariable) (TabulasiTable, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	grand, grandTotal, totalRows, err := s.tabulasiTotals(ctx, filter, v)
	if err != nil {
		return TabulasiTable{}, err
	}
	rows, err := s.tabulasiRows(ctx, filter, v, TabulasiMaxRows)
	if err != nil {
		return TabulasiTable{}, err
	}
	return TabulasiTable{
		Variable:   v,
		Columns:    tabulasiColumns(v, grand),
		Rows:       rows,
		TotalRows:  totalRows,
		Grand:      grand,
		GrandTotal: grandTotal,
		Truncated:  len(rows) >= TabulasiMaxRows,
	}, nil
}

// tabulasiTotals returns the per-category counts over the whole filter,
// their sum, and how many distinct SubSLS the filter covers.
func (s *Service) tabulasiTotals(ctx context.Context, filter Filter, v TabulasiVariable) (grand map[string]uint64, grandTotal, totalRows uint64, err error) {
	where, args := filter.clause()
	grand = map[string]uint64{}

	q := fmt.Sprintf(`SELECT %s AS val, count() AS n FROM %s WHERE %s GROUP BY val`, v.Column, table, where)
	rows, err := s.conn.Query(ctx, q, args...)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("points: tabulasi totals: %w", err)
	}
	for rows.Next() {
		var val string
		var n uint64
		if err := rows.Scan(&val, &n); err != nil {
			rows.Close()
			return nil, 0, 0, fmt.Errorf("points: tabulasi totals scan: %w", err)
		}
		grand[val] = n
		grandTotal += n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, 0, fmt.Errorf("points: tabulasi totals rows: %w", err)
	}

	// The SubSLS count is its own aggregate: a SubSLS holds several
	// categories, so per-category uniqExact values can't be summed.
	row := s.conn.QueryRow(ctx, fmt.Sprintf(
		`SELECT uniqExact(level_6_full_code) FROM %s WHERE %s`, table, where), args...)
	if err := row.Scan(&totalRows); err != nil {
		return nil, 0, 0, fmt.Errorf("points: tabulasi subsls count: %w", err)
	}
	return grand, grandTotal, totalRows, nil
}

// tabulasiColumns is the column order: the variable's known values first
// (all of them, present or not — see TabulasiVariable), anything
// unexpected after them in sorted order so map iteration can't reshuffle
// it between requests, and blank last, only when it occurs.
func tabulasiColumns(v TabulasiVariable, grand map[string]uint64) []string {
	columns := make([]string, 0, len(v.Values)+2)
	seen := map[string]bool{}
	for _, val := range v.Values {
		columns = append(columns, val)
		seen[val] = true
	}
	var extra []string
	for val := range grand {
		if val != "" && !seen[val] {
			extra = append(extra, val)
		}
	}
	sort.Strings(extra)
	columns = append(columns, extra...)
	if _, ok := grand[""]; ok {
		columns = append(columns, "")
	}
	return columns
}

// tabulasiRows returns up to limit SubSLS, each with its per-category
// counts, ordered by code.
func (s *Service) tabulasiRows(ctx context.Context, filter Filter, v TabulasiVariable, limit int) ([]TabulasiRow, error) {
	where, args := filter.clause()
	q := fmt.Sprintf(`SELECT subsls, groupArray(val) AS vals, groupArray(n) AS ns, sum(n) AS total
		FROM (
			SELECT level_6_full_code AS subsls, %s AS val, count() AS n
			FROM %s WHERE %s
			GROUP BY subsls, val
		)
		GROUP BY subsls
		ORDER BY subsls
		LIMIT %d`, v.Column, table, where, limit)
	rows, err := s.conn.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("points: tabulasi page: %w", err)
	}
	defer rows.Close()

	out := make([]TabulasiRow, 0, min(limit, 1024))
	for rows.Next() {
		var r TabulasiRow
		var vals []string
		var ns []uint64
		if err := rows.Scan(&r.SubSLS, &vals, &ns, &r.Total); err != nil {
			return nil, fmt.Errorf("points: tabulasi page scan: %w", err)
		}
		r.Counts = make(map[string]uint64, len(vals))
		for i := range vals {
			if i < len(ns) {
				r.Counts[vals[i]] = ns[i]
			}
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("points: tabulasi page rows: %w", err)
	}
	return out, nil
}
