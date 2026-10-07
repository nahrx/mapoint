// Package bansos serves the Bansos menu: the rows of the `bansos` table,
// which lists social-assistance recipients matched against the SE2026
// prelist. It gets its own Service rather than joining internal/points
// because it is a separate table with its own columns and its own, unusual
// way of locating a row in the wilayah hierarchy — see Filter.clause.
package bansos

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"se2026-titik-maps/internal/points"
)

const (
	table = "bansos"

	queryTimeout = 15 * time.Second

	// ReportMaxRows caps what ListAll returns for the PDF/Excel downloads.
	// The whole table is 16,086 rows — smaller than a single desa of
	// se2026_titik2 — so unlike the other menus this one puts no minimum
	// wilayah on a download: even completely unfiltered it is a sane
	// report. The cap is a safety net for a table that grows, not a limit
	// anyone reaches today.
	ReportMaxRows = 40000
)

// Row is one line of the Bansos table.
//
// nik, no_kk and nik_kk exist in the table and are deliberately not here:
// they are personal identity numbers, nothing in this menu displays them,
// and — as in internal/regsosek — they are left out of the query entirely
// rather than fetched and then hidden, so they never reach the browser and
// no later display code can surface them by accident. The names stay;
// those are names, not numbers.
type Row struct {
	BansosNama    string `json:"bansos_nama"`
	SubSLS        string `json:"subsls"`
	AssignmentID  string `json:"assignment_id"`
	DtsenKodeDesa string `json:"dtsen_kode_desa"`
	DtsenAlamat   string `json:"dtsen_alamat"`

	// RtKTP, RwKTP and AlamatKTP come from the KTP address, which belongs
	// to a different desa for a quarter of the rows. They are blanked
	// unless kode_desa_ktp matches dtsen_kode_desa — see ktpColumns.
	RtKTP     string `json:"rt_ktp"`
	RwKTP     string `json:"rw_ktp"`
	AlamatKTP string `json:"alamat_ktp"`
}

// ktpMatches is the condition under which the three KTP columns are shown
// at all: the KTP address is only meaningful next to the dtsen address
// when both describe the same desa. Measured on the live table: 11,924 of
// 16,086 rows match, 4,162 do not (1,006 of those have no kode_desa_ktp),
// and the rest of a non-matching row still shows normally — only the three
// KTP fields come back blank.
//
// The empty-desa case is excluded explicitly: without it two rows that
// both have no desa code at all would count as "same desa" and show a KTP
// address that belongs nowhere.
const ktpMatches = "kode_desa_ktp = dtsen_kode_desa AND dtsen_kode_desa != ''"

// rowColumns is the SELECT list backing Row, in scan order. rt_ktp and
// rw_ktp are Int32 in the table and are read as strings so that "not
// shown" is an empty cell rather than a 0 that reads like a real RT.
var rowColumns = fmt.Sprintf(`bansos_nama, subsls, assignment_id, dtsen_kode_desa, dtsen_alamat,
		if(%[1]s, toString(rt_ktp), ''),
		if(%[1]s, toString(rw_ktp), ''),
		if(%[1]s, alamat_ktp, '')`, ktpMatches)

func scanRow(rows driver.Rows) (Row, error) {
	var r Row
	err := rows.Scan(
		&r.BansosNama, &r.SubSLS, &r.AssignmentID, &r.DtsenKodeDesa, &r.DtsenAlamat,
		&r.RtKTP, &r.RwKTP, &r.AlamatKTP,
	)
	return r, err
}

// Filter narrows the table by wilayah. WilayahPrefix is a digit-only
// prefix of the 16-digit wilayah code, built by the caller from an
// already-validated points.Filter (see its FullCode), so it never carries
// arbitrary text.
type Filter struct {
	WilayahPrefix string
	// Search is free text from the search box — the only field here that
	// is a bind parameter rather than interpolated, because it is the only
	// one that isn't already validated digits.
	Search string
}

// desaCodeLen is how many digits of the wilayah code identify a
// desa/kelurahan: 4 kabupaten/kota + 3 kecamatan + 3 desa. A prefix longer
// than this reaches into SLS (14) or SubSLS (16).
const desaCodeLen = 10

// clause builds the WHERE fragment for filter.
//
// Two columns can place a row in the hierarchy and they do not always
// agree on how deep they go. `subsls` is the full 16-digit code but is
// empty for 3,322 of 16,086 rows; `dtsen_kode_desa` is only 10 digits —
// desa level — and is empty for 4,617. So:
//
//   - a row with a subsls is filtered on it, at whatever level the user
//     picked;
//   - a row without one falls back to dtsen_kode_desa, which can only
//     answer down to desa. Once the filter reaches SLS or SubSLS those
//     rows are dropped rather than guessed at: nothing in the row says
//     which SLS it belongs to, and showing it under every SLS of its desa
//     would be worse than leaving it out.
//
// 1,584 rows have neither code. They can't be placed anywhere, so they
// appear only when no wilayah filter is applied at all — which is also
// how they become findable.
func (f Filter) clause() (string, []any) {
	parts := make([]string, 0, 2)
	var args []any

	if f.WilayahPrefix != "" {
		bySubSLS := fmt.Sprintf("startsWith(subsls, '%s')", f.WilayahPrefix)
		if len(f.WilayahPrefix) > desaCodeLen {
			parts = append(parts, bySubSLS)
		} else {
			parts = append(parts, fmt.Sprintf("(%s OR (subsls = '' AND startsWith(dtsen_kode_desa, '%s')))",
				bySubSLS, f.WilayahPrefix))
		}
	}
	if f.Search != "" {
		// Substring, not case-sensitive, over bansos_nama only — the name
		// this menu is read by. positionCaseInsensitive is a plain
		// substring search (no LIKE wildcards to escape) and the text
		// travels as a bind parameter, never inside the SQL string.
		parts = append(parts, "positionCaseInsensitive(bansos_nama, ?) > 0")
		args = append(args, f.Search)
	}

	if len(parts) == 0 {
		return "1", nil
	}
	return strings.Join(parts, " AND "), args
}

// listSortColumns whitelists the sortable columns, keyed by the JSON names
// Row exposes so the frontend can send back whatever column it rendered.
// Same reasoning as the other menus': sortBy is user input and only ever
// reaches SQL as this map's value, never its key.
//
// The three KTP columns sort by what is actually displayed, not by the raw
// column — sorting a blank cell by a value the user can't see would be a
// lie. For RT/RW that means sorting numerically with the blanks collapsed
// to -1, so they group at one end instead of scattering: as text, "10"
// would sort before "2".
var listSortColumns = map[string]string{
	"bansos_nama":     "bansos_nama",
	"subsls":          "subsls",
	"assignment_id":   "assignment_id",
	"dtsen_kode_desa": "dtsen_kode_desa",
	"dtsen_alamat":    "dtsen_alamat",
	"rt_ktp":          fmt.Sprintf("if(%s, rt_ktp, -1)", ktpMatches),
	"rw_ktp":          fmt.Sprintf("if(%s, rw_ktp, -1)", ktpMatches),
	"alamat_ktp":      fmt.Sprintf("if(%s, alamat_ktp, '')", ktpMatches),
}

// DefaultSortColumn is used when sortBy is empty or unrecognized.
const DefaultSortColumn = "bansos_nama"

// ParseSortColumn validates a sort key, falling back to DefaultSortColumn
// rather than erroring — the table always has to render in some order.
func ParseSortColumn(v string) string {
	if _, ok := listSortColumns[v]; ok {
		return v
	}
	return DefaultSortColumn
}

// orderByFor is the ORDER BY for a validated sort key. The wilayah code
// and assignment_id are appended as tie-breakers so that rows sharing a
// value (plenty share a blank one) keep a stable, meaningful order —
// ClickHouse does not promise a stable sort on its own, and without this a
// row could move between pages while nothing changed.
func orderByFor(sortColumn string, dir points.SortDir) string {
	expr, ok := listSortColumns[sortColumn]
	if !ok {
		expr = listSortColumns[DefaultSortColumn]
	}
	return fmt.Sprintf("%s %s, subsls ASC, assignment_id ASC", expr, dir.SQL())
}

// ListPage is one page of the Bansos table.
type ListPage struct {
	Total    uint64 `json:"total"`
	Page     int    `json:"page"`
	PageSize int    `json:"page_size"`
	Items    []Row  `json:"items"`
}

// Service runs the queries against ClickHouse.
type Service struct {
	conn driver.Conn
}

func NewService(conn driver.Conn) *Service {
	return &Service{conn: conn}
}

// List returns one page of rows matching filter. page is 1-indexed;
// pageSize is clamped to the same range as the other list menus, so all of
// them paginate identically.
func (s *Service) List(ctx context.Context, filter Filter, page, pageSize int, sortColumn string, dir points.SortDir) (ListPage, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = points.DefaultPageSize
	}
	if pageSize > points.MaxPageSize {
		pageSize = points.MaxPageSize
	}

	where, args := filter.clause()

	var total uint64
	countQ := fmt.Sprintf("SELECT count() FROM %s WHERE %s", table, where)
	if err := s.conn.QueryRow(ctx, countQ, args...).Scan(&total); err != nil {
		return ListPage{}, fmt.Errorf("bansos: count: %w", err)
	}

	q := fmt.Sprintf(`SELECT %s FROM %s WHERE %s ORDER BY %s LIMIT %d OFFSET %d`,
		rowColumns, table, where, orderByFor(sortColumn, dir), pageSize, (page-1)*pageSize)
	rows, err := s.conn.Query(ctx, q, args...)
	if err != nil {
		return ListPage{}, fmt.Errorf("bansos: list: %w", err)
	}
	defer rows.Close()

	items := make([]Row, 0, pageSize)
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			return ListPage{}, fmt.Errorf("bansos: scan: %w", err)
		}
		items = append(items, r)
	}
	if err := rows.Err(); err != nil {
		return ListPage{}, fmt.Errorf("bansos: rows: %w", err)
	}
	return ListPage{Total: total, Page: page, PageSize: pageSize, Items: items}, nil
}

// ListAll returns every row matching filter (up to ReportMaxRows) in the
// same order the table shows, with no pagination — for the PDF/Excel
// downloads, which describe the whole filtered set rather than one page.
func (s *Service) ListAll(ctx context.Context, filter Filter, sortColumn string, dir points.SortDir) ([]Row, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	where, args := filter.clause()
	q := fmt.Sprintf(`SELECT %s FROM %s WHERE %s ORDER BY %s LIMIT %d`,
		rowColumns, table, where, orderByFor(sortColumn, dir), ReportMaxRows)
	rows, err := s.conn.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("bansos: list all: %w", err)
	}
	defer rows.Close()

	items := make([]Row, 0, 1024)
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			return nil, fmt.Errorf("bansos: scan: %w", err)
		}
		items = append(items, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("bansos: rows: %w", err)
	}
	return items, nil
}
