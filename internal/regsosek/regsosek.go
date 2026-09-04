// Package regsosek serves the two "match" menus: Daftar Match Regsosek
// (the table) and Peta Match Reg2022 (the same rows on a map). Both read
// se2026_match_regsosek, which holds SE2026 prelist families recorded as
// not found in the field but matched to a Regsosek 2022 record. It's a
// separate table with its own columns, so it gets its own Service rather
// than being folded into internal/points — but it shares that package's
// wilayah vocabulary (level_6_full_code in the same 16-digit form), its
// pagination/sort types, and its clustering scale, so the views behave
// identically where they overlap.
package regsosek

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"se2026-titik-maps/internal/points"
)

const (
	table = "se2026_match_regsosek"

	queryTimeout = 15 * time.Second

	// ReportMaxRows caps how many rows ListAll returns. Reports are scoped
	// to a kecamatan or narrower (see the handler in internal/api), and the
	// largest kecamatan in this table holds ~7.4k rows, so this is a safety
	// net rather than a limit anyone should reach.
	ReportMaxRows = 40000
)

// Row is one line of the Daftar Match Regsosek table. Several JSON names
// differ from the column they come from because the UI labels them
// differently: level_6_full_code is shown as "ID SubSLS" and nama_prelist
// as "Nama".
type Row struct {
	AssignmentID   string `json:"assignment_id"`
	SubSLS         string `json:"subsls"` // level_6_full_code
	Nama           string `json:"nama"`   // nama_prelist
	NamaKK         string `json:"nama_kk"`
	MatchStatus    string `json:"match_status"`
	AlamatRegsosek string `json:"alamat_regsosek"` // alamat_gabung_regsosek
	NamaMatched    string `json:"nama_matched"`    // nama_matched_regsosek
}

// rowColumns is the SELECT list backing Row, in scan order.
//
// no_kk, nik_kk and nik_matched_regsosek are deliberately absent. They are
// personal identity numbers and are not shown anywhere — not in the table,
// the map tooltip, the PDF, the Excel export, or the search — so they are
// left out of the query entirely rather than fetched and then hidden. That
// way they never reach the browser at all, and no future display code can
// surface them by accident. nama_kk stays: it is a name, not a number.
const rowColumns = `assignment_id, level_6_full_code, nama_prelist, nama_kk,
		match_status, alamat_gabung_regsosek, nama_matched_regsosek`

func scanRow(rows driver.Rows) (Row, error) {
	var r Row
	err := rows.Scan(
		&r.AssignmentID, &r.SubSLS, &r.Nama, &r.NamaKK,
		&r.MatchStatus, &r.AlamatRegsosek, &r.NamaMatched,
	)
	return r, err
}

// matchStatusValues is the closed set match_status actually holds, taken
// from the live data. Same role as the enum whitelists in points: it is
// both the dropdown's contents (via GetFilterOptions) and the check a raw
// filter value must pass before it can reach the SQL text.
var matchStatusValues = []string{
	"MATCH_EXACT_NIK_HEAD",
	"MATCH_EXACT_SLS_NAME_HEAD",
	"MATCH_NIK_1_DIGIT_TYPO",
	"MATCH_EXACT_NIK_MEMBER",
	"MATCH_NIK_2_DIGIT_TYPO",
	"MATCH_NIK_3_DIGIT_TYPO",
	"MATCH_EXACT_SLS_NAME_MEMBER",
}

// FilterOptions is the payload for GET /api/reg2022/filter-options.
type FilterOptions struct {
	MatchStatus []string `json:"match_status"`
}

// GetFilterOptions returns the match_status dropdown choices.
func GetFilterOptions() FilterOptions {
	return FilterOptions{MatchStatus: matchStatusValues}
}

// ParseMatchStatus validates a match_status filter value. Empty means "no
// filter"; points.EmptyValue means "rows where the column is blank" — the
// same two-state convention the Daftar menu's attribute filters use.
func ParseMatchStatus(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	if raw == points.EmptyValue {
		return points.EmptyValue, nil
	}
	for _, v := range matchStatusValues {
		if raw == v {
			return raw, nil
		}
	}
	return "", fmt.Errorf("invalid match_status value %q", raw)
}

// Filter narrows both the table and the map. WilayahPrefix is a digit-only
// prefix of level_6_full_code, built by the caller from an
// already-validated points.Filter (see its FullCode), so it never carries
// arbitrary text. MatchStatus has been through ParseMatchStatus. Search is
// genuine free-text and is the only field bound as a query parameter
// instead of being interpolated.
type Filter struct {
	WilayahPrefix string
	MatchStatus   string
	Search        string
}

// searchColumns are the columns the free-text search box looks in. Names
// only: no_kk and nik_kk were dropped along with their columns, so an
// identity number can't be used as a lookup key either — not merely hidden
// from display. See rowColumns.
var searchColumns = []string{"nama_prelist", "nama_kk"}

func (f Filter) clause() (string, []any) {
	parts := make([]string, 0, 3)
	var args []any

	if f.WilayahPrefix != "" {
		// startsWith on a level_6_full_code prefix, matching how
		// points.Filter.clause does it. This table is sorted by
		// assignment_id rather than the wilayah code, so unlike there it
		// can't skip granules — but at 166k rows / 32MiB a full scan costs
		// only a few ms, and writing it this way keeps the views consistent
		// (and stays correct if the table is ever re-sorted).
		parts = append(parts, fmt.Sprintf("startsWith(level_6_full_code, '%s')", f.WilayahPrefix))
	}
	if f.MatchStatus != "" {
		if f.MatchStatus == points.EmptyValue {
			parts = append(parts, "match_status = ''")
		} else {
			parts = append(parts, fmt.Sprintf("match_status = '%s'", f.MatchStatus))
		}
	}
	if f.Search != "" {
		// One bind parameter per searched column (same value each time), so
		// what's typed matches either the prelist name or the
		// head-of-household name. positionCaseInsensitive is a
		// plain substring search — no LIKE wildcards to escape — and the
		// text never reaches the SQL string itself.
		ors := make([]string, len(searchColumns))
		for i, col := range searchColumns {
			ors[i] = fmt.Sprintf("positionCaseInsensitive(%s, ?) > 0", col)
			args = append(args, f.Search)
		}
		parts = append(parts, "("+strings.Join(ors, " OR ")+")")
	}

	if len(parts) == 0 {
		return "1", nil
	}
	return strings.Join(parts, " AND "), args
}

// listSortColumns whitelists the sortable columns, keyed by the JSON names
// Row exposes so the frontend can send back whatever column it rendered.
// Same reasoning as points' equivalent: sortBy is user input and only ever
// reaches SQL as this map's value, never its key.
var listSortColumns = map[string]string{
	"nama":            "nama_prelist",
	"nama_kk":         "nama_kk",
	"subsls":          "level_6_full_code",
	"match_status":    "match_status",
	"alamat_regsosek": "alamat_gabung_regsosek",
	"nama_matched":    "nama_matched_regsosek",
	"assignment_id":   "assignment_id",
}

// DefaultSortColumn is used when sortBy is empty or unrecognized.
const DefaultSortColumn = "nama"

// ParseSortColumn validates a sort key, falling back to DefaultSortColumn
// rather than erroring — the table always has to render in some order.
func ParseSortColumn(v string) string {
	if _, ok := listSortColumns[v]; ok {
		return v
	}
	return DefaultSortColumn
}

// ListPage is one page of the Daftar Match Regsosek table.
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
// pageSize is clamped to the same [1, points.MaxPageSize] range the other
// list menu uses, so both paginate identically.
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
		return ListPage{}, fmt.Errorf("regsosek: count: %w", err)
	}

	items, err := s.listRows(ctx, where, args, sortColumn, dir, pageSize, (page-1)*pageSize)
	if err != nil {
		return ListPage{}, err
	}
	return ListPage{Total: total, Page: page, PageSize: pageSize, Items: items}, nil
}

// ListAll returns every row matching filter (up to ReportMaxRows) with no
// pagination — for the PDF/Excel reports, which need the whole wilayah in
// one document. Callers should pin the filter to at least a kecamatan
// first; see the handler in internal/api.
func (s *Service) ListAll(ctx context.Context, filter Filter, sortColumn string, dir points.SortDir) ([]Row, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	where, args := filter.clause()
	return s.listRows(ctx, where, args, sortColumn, dir, ReportMaxRows, 0)
}

func (s *Service) listRows(ctx context.Context, where string, args []any, sortColumn string, dir points.SortDir, limit, offset int) ([]Row, error) {
	sortCol, ok := listSortColumns[sortColumn]
	if !ok {
		sortCol = listSortColumns[DefaultSortColumn]
	}
	q := fmt.Sprintf(`SELECT
		%s
	FROM %s
	WHERE %s
	ORDER BY %s %s
	LIMIT %d OFFSET %d`, rowColumns, table, where, sortCol, dir.SQL(), limit, offset)

	rows, err := s.conn.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("regsosek: query: %w", err)
	}
	defer rows.Close()

	items := make([]Row, 0, 512)
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			return nil, fmt.Errorf("regsosek: scan: %w", err)
		}
		items = append(items, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("regsosek: rows: %w", err)
	}
	return items, nil
}

// =====================================================================
// Peta Match Reg2022 — the same rows, viewport-bound
// =====================================================================

// MapPoint is one marker on the match map, carrying exactly the four
// fields the tooltip shows.
type MapPoint struct {
	Lat    float64 `json:"lat"`
	Lon    float64 `json:"lon"`
	Nama   string  `json:"nama"` // nama_prelist
	NamaKK string  `json:"nama_kk"`
	SubSLS string  `json:"subsls"` // level_6_full_code
	Alamat string  `json:"alamat"` // alamat_gabung_regsosek
}

// MapResponse mirrors points.Response: individual markers when the
// viewport is sparse, grid clusters otherwise.
type MapResponse struct {
	Type     string           `json:"type"` // "points" or "clusters"
	Total    uint64           `json:"total"`
	Points   []MapPoint       `json:"points"`
	Clusters []points.Cluster `json:"clusters"`
}

// Every row in this table has usable coordinates (checked against the live
// data: 166,507 of 166,507 non-zero and finite, all within Kalimantan), so
// unlike the points table there's no validity predicate to apply here.
func bboxClause(b points.BBox) string {
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
	return fmt.Sprintf(
		"latitude_regsosek BETWEEN %s AND %s AND longitude_regsosek BETWEEN %s AND %s",
		f(b.MinLat), f(b.MaxLat), f(b.MinLon), f(b.MaxLon),
	)
}

// Query returns individual markers if the viewport is sparse, or grid
// clusters otherwise. Same cluster-first shape as points.Service.Query:
// the grid aggregation already carries a count per cell, so summing those
// gives the viewport total without a second count() query.
func (s *Service) Query(ctx context.Context, b points.BBox, zoom int, filter Filter) (MapResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	where, args := filter.clause()

	clusters, err := s.queryClusters(ctx, b, zoom, where, args)
	if err != nil {
		return MapResponse{}, fmt.Errorf("regsosek: query clusters: %w", err)
	}

	var total uint64
	for _, c := range clusters {
		total += c.Count
	}

	// A truncated cluster list would understate the total — see the same
	// guard in points.Service.Query.
	if len(clusters) >= points.ClusterLimit {
		q := fmt.Sprintf("SELECT count() FROM %s WHERE %s AND %s", table, bboxClause(b), where)
		if err := s.conn.QueryRow(ctx, q, args...).Scan(&total); err != nil {
			return MapResponse{}, fmt.Errorf("regsosek: count: %w", err)
		}
	}

	if total > points.IndividualLimit {
		return MapResponse{Type: "clusters", Total: total, Clusters: clusters}, nil
	}

	pts, err := s.queryPoints(ctx, b, where, args)
	if err != nil {
		return MapResponse{}, fmt.Errorf("regsosek: query points: %w", err)
	}
	return MapResponse{Type: "points", Total: total, Points: pts}, nil
}

func (s *Service) queryPoints(ctx context.Context, b points.BBox, where string, args []any) ([]MapPoint, error) {
	q := fmt.Sprintf(`SELECT
		latitude_regsosek, longitude_regsosek,
		nama_prelist, nama_kk, level_6_full_code, alamat_gabung_regsosek
	FROM %s
	WHERE %s AND %s
	LIMIT %d`, table, bboxClause(b), where, points.IndividualLimit)

	rows, err := s.conn.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	pts := make([]MapPoint, 0, 512)
	for rows.Next() {
		var p MapPoint
		if err := rows.Scan(&p.Lat, &p.Lon, &p.Nama, &p.NamaKK, &p.SubSLS, &p.Alamat); err != nil {
			return nil, err
		}
		pts = append(pts, p)
	}
	return pts, rows.Err()
}

func (s *Service) queryClusters(ctx context.Context, b points.BBox, zoom int, where string, args []any) ([]points.Cluster, error) {
	cell := strconv.FormatFloat(points.CellSizeForZoom(zoom), 'f', -1, 64)
	q := fmt.Sprintf(`SELECT
		floor(latitude_regsosek / %s) * %s + %s / 2 AS glat,
		floor(longitude_regsosek / %s) * %s + %s / 2 AS glon,
		count() AS cnt
	FROM %s
	WHERE %s AND %s
	GROUP BY glat, glon
	ORDER BY cnt DESC
	LIMIT %d`, cell, cell, cell, cell, cell, cell, table, bboxClause(b), where, points.ClusterLimit)

	rows, err := s.conn.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	clusters := make([]points.Cluster, 0, 512)
	for rows.Next() {
		var c points.Cluster
		if err := rows.Scan(&c.Lat, &c.Lon, &c.Count); err != nil {
			return nil, err
		}
		clusters = append(clusters, c)
	}
	return clusters, rows.Err()
}

// Bounds is the geographic extent of every row matching filter, used to
// fit the map when a filter is applied.
type Bounds struct {
	MinLat float64 `json:"min_lat"`
	MaxLat float64 `json:"max_lat"`
	MinLon float64 `json:"min_lon"`
	MaxLon float64 `json:"max_lon"`
	Total  uint64  `json:"total"`
}

// BoundsFor computes the extent of the filtered rows. Percentile 1-99 like
// the points table's per-wilayah boxes, so a single mis-keyed coordinate
// can't stretch the view across the province.
func (s *Service) BoundsFor(ctx context.Context, filter Filter) (Bounds, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	where, args := filter.clause()
	q := fmt.Sprintf(`SELECT
		quantile(0.01)(latitude_regsosek), quantile(0.99)(latitude_regsosek),
		quantile(0.01)(longitude_regsosek), quantile(0.99)(longitude_regsosek),
		count()
	FROM %s WHERE %s`, table, where)

	var b Bounds
	if err := s.conn.QueryRow(ctx, q, args...).Scan(&b.MinLat, &b.MaxLat, &b.MinLon, &b.MaxLon, &b.Total); err != nil {
		return Bounds{}, fmt.Errorf("regsosek: bounds: %w", err)
	}
	return b, nil
}
