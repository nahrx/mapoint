package points

import (
	"context"
	"fmt"
	"sort"
)

// TabulasiVariable is one of the categorical columns the Tabulasi menu can
// cross-tabulate against SubSLS. Column is a constant from this package,
// never anything a request supplied — Key is what the URL carries, and
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
type TabulasiRow struct {
	SubSLS string            `json:"subsls"`
	Counts map[string]uint64 `json:"counts"`
	Total  uint64            `json:"total"`
}

// TabulasiPage is one page of the cross-tabulation. Columns is the full
// category order the client should render, blank ("") last when any row
// in the whole filter has one; Grand holds the same categories summed over
// the whole filter, not just this page, so the footer stays correct while
// paging.
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

// Tabulasi cross-tabulates v against level_6_full_code for the rows
// matching filter: one row per SubSLS, one column per category. page is
// 1-indexed and counts SubSLS, not underlying rows; pageSize is clamped to
// [1, MaxPageSize] like List.
//
// Three queries. The page query groups twice — first (subsls, value) to
// count, then subsls to fold each SubSLS's counts into a pair of parallel
// arrays — and pages on the outer GROUP BY, so LIMIT/OFFSET apply to
// SubSLS. Parallel arrays rather than groupArray of a tuple because the
// driver scans []string and []uint64 without ceremony, and a tuple would
// come back as untyped []any. The other two give the grand row and the
// SubSLS count.
//
// Measured against the live table: the whole province (15.7k SubSLS,
// 2.19M rows) answers in well under a second — see README, Menu Tabulasi.
func (s *Service) Tabulasi(ctx context.Context, filter Filter, v TabulasiVariable, page, pageSize int) (TabulasiPage, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = DefaultPageSize
	}
	if pageSize > MaxPageSize {
		pageSize = MaxPageSize
	}

	where, args := filter.clause()

	// --- grand totals + how many SubSLS there are ----------------------
	grand := map[string]uint64{}
	var grandTotal, totalRows uint64
	{
		q := fmt.Sprintf(`SELECT %s AS val, count() AS n FROM %s WHERE %s GROUP BY val`, v.Column, table, where)
		rows, err := s.conn.Query(ctx, q, args...)
		if err != nil {
			return TabulasiPage{}, fmt.Errorf("points: tabulasi totals: %w", err)
		}
		for rows.Next() {
			var val string
			var n uint64
			if err := rows.Scan(&val, &n); err != nil {
				rows.Close()
				return TabulasiPage{}, fmt.Errorf("points: tabulasi totals scan: %w", err)
			}
			grand[val] = n
			grandTotal += n
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return TabulasiPage{}, fmt.Errorf("points: tabulasi totals rows: %w", err)
		}

		// The SubSLS count is its own aggregate: a SubSLS holds several
		// categories, so per-category uniqExact values can't be summed.
		row := s.conn.QueryRow(ctx, fmt.Sprintf(
			`SELECT uniqExact(level_6_full_code) FROM %s WHERE %s`, table, where), args...)
		if err := row.Scan(&totalRows); err != nil {
			return TabulasiPage{}, fmt.Errorf("points: tabulasi subsls count: %w", err)
		}
	}

	// --- column order: known values first, anything unexpected after, ---
	//     blank last and only when it occurs.
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
	// Deterministic: map iteration order would otherwise reshuffle the
	// unexpected columns between requests.
	sort.Strings(extra)
	columns = append(columns, extra...)
	if _, ok := grand[""]; ok {
		columns = append(columns, "")
	}

	// --- one page of SubSLS -----------------------------------------------
	offset := (page - 1) * pageSize
	q := fmt.Sprintf(`SELECT subsls, groupArray(val) AS vals, groupArray(n) AS ns, sum(n) AS total
		FROM (
			SELECT level_6_full_code AS subsls, %s AS val, count() AS n
			FROM %s WHERE %s
			GROUP BY subsls, val
		)
		GROUP BY subsls
		ORDER BY subsls
		LIMIT %d OFFSET %d`, v.Column, table, where, pageSize, offset)
	rows, err := s.conn.Query(ctx, q, args...)
	if err != nil {
		return TabulasiPage{}, fmt.Errorf("points: tabulasi page: %w", err)
	}
	defer rows.Close()

	out := make([]TabulasiRow, 0, pageSize)
	for rows.Next() {
		var r TabulasiRow
		var vals []string
		var ns []uint64
		if err := rows.Scan(&r.SubSLS, &vals, &ns, &r.Total); err != nil {
			return TabulasiPage{}, fmt.Errorf("points: tabulasi page scan: %w", err)
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
		return TabulasiPage{}, fmt.Errorf("points: tabulasi page rows: %w", err)
	}

	return TabulasiPage{
		Variable:   v.Key,
		Columns:    columns,
		Rows:       out,
		TotalRows:  totalRows,
		Grand:      grand,
		GrandTotal: grandTotal,
		Page:       page,
		PageSize:   pageSize,
	}, nil
}
