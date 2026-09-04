// Package points implements viewport-aware queries against the
// se2026_titik2 ClickHouse table: individual points when the visible area
// is sparse enough, grid clusters otherwise. This is what lets a
// multi-million-row table be shown "seamlessly" without ever shipping more
// than a few thousand markers to the browser at once.
package points

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

const (
	table = "se2026_titik2"

	// matchTable and regsosekTable back the two "ditemukan di …" flags.
	// Neither is joined for its columns — only for whether a matching row
	// exists at all — so they are used as IN-subqueries rather than joins;
	// see flagExpr.
	matchTable    = "se2026_match"
	regsosekTable = "se2026_match_regsosek"

	// IndividualLimit is the max rows returned as raw points for one
	// request. Below this the viewport is "sparse" and every point is
	// shown individually with full tooltip data.
	IndividualLimit = 3000

	// ClusterLimit caps how many grid cells one clustering response can
	// return, as a safety net against pathological grid sizes.
	ClusterLimit = 20000

	queryTimeout = 15 * time.Second
)

// BBox is a geographic viewport, WGS84 degrees.
type BBox struct {
	MinLat, MaxLat float64
	MinLon, MaxLon float64
}

// Parse validates and clamps a bbox from raw query-string values.
func ParseBBox(minLat, maxLat, minLon, maxLon string) (BBox, error) {
	vals := make([]float64, 4)
	for i, s := range []string{minLat, maxLat, minLon, maxLon} {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return BBox{}, fmt.Errorf("invalid coordinate %q: %w", s, err)
		}
		vals[i] = v
	}
	b := BBox{MinLat: vals[0], MaxLat: vals[1], MinLon: vals[2], MaxLon: vals[3]}
	if b.MinLat > b.MaxLat || b.MinLon > b.MaxLon {
		return BBox{}, fmt.Errorf("bbox min must not exceed max")
	}
	b.MinLat = clamp(b.MinLat, -90, 90)
	b.MaxLat = clamp(b.MaxLat, -90, 90)
	b.MinLon = clamp(b.MinLon, -180, 180)
	b.MaxLon = clamp(b.MaxLon, -180, 180)
	return b, nil
}

var (
	fourDigitCodeRe  = regexp.MustCompile(`^[0-9]{4}$`) // shared by kabkota and SLS codes
	threeDigitCodeRe = regexp.MustCompile(`^[0-9]{3}$`) // shared by kecamatan and desa/kelurahan codes
	twoDigitCodeRe   = regexp.MustCompile(`^[0-9]{2}$`) // SubSLS codes
)

// ParseKabKota validates a kabupaten/kota filter value: either empty (no
// filter) or exactly 4 digits — the provinsi+kabupaten/kota prefix of
// level_6_full_code.
func ParseKabKota(code string) (string, error) {
	if code == "" {
		return "", nil
	}
	if !fourDigitCodeRe.MatchString(code) {
		return "", fmt.Errorf("invalid kabkota code %q: expected 4 digits", code)
	}
	return code, nil
}

// ParseKecamatan validates a kecamatan filter value: either empty (no
// filter) or exactly 3 digits — the 3 characters right after the
// kabupaten/kota prefix in level_6_full_code. A kecamatan code is only
// unique within its kabupaten/kota, so it's meaningless without one — the
// caller (see Filter.Validate) must ensure KabKota is set whenever
// Kecamatan is.
func ParseKecamatan(code string) (string, error) {
	if code == "" {
		return "", nil
	}
	if !threeDigitCodeRe.MatchString(code) {
		return "", fmt.Errorf("invalid kecamatan code %q: expected 3 digits", code)
	}
	return code, nil
}

// ParseDesa validates a desa/kelurahan filter value: either empty (no
// filter) or exactly 3 digits — the 3 characters right after the
// kecamatan code in level_6_full_code. Like Kecamatan, it's only unique
// within its kecamatan, so it's meaningless without one — see Validate.
func ParseDesa(code string) (string, error) {
	if code == "" {
		return "", nil
	}
	if !threeDigitCodeRe.MatchString(code) {
		return "", fmt.Errorf("invalid desa/kelurahan code %q: expected 3 digits", code)
	}
	return code, nil
}

// ParseSLS validates a Kode SLS filter value: either empty (no filter) or
// exactly 4 digits — the 4 characters right after the desa/kelurahan code
// in level_6_full_code. Like Desa, it's only unique within its
// desa/kelurahan, so it's meaningless without one — see Validate.
func ParseSLS(code string) (string, error) {
	if code == "" {
		return "", nil
	}
	if !fourDigitCodeRe.MatchString(code) {
		return "", fmt.Errorf("invalid SLS code %q: expected 4 digits", code)
	}
	return code, nil
}

// ParseSubSLS validates a Kode SubSLS filter value: either empty (no
// filter) or exactly 2 digits — the 2 characters right after the SLS code
// in level_6_full_code (the last 2 of its 16 digits). Like SLS, it's only
// unique within its SLS, so it's meaningless without one — see Validate.
func ParseSubSLS(code string) (string, error) {
	if code == "" {
		return "", nil
	}
	if !twoDigitCodeRe.MatchString(code) {
		return "", fmt.Errorf("invalid SubSLS code %q: expected 2 digits", code)
	}
	return code, nil
}

// jenisPrelistValues, keberadaanKeluargaValues and statusValues are the
// closed sets of values jenis_prelist_root, keberadaan_keluarga and
// assignment_status_alias actually hold. They double as the dropdown
// choices served by GET /api/filter-options and the server-side whitelist
// a raw filter value is checked against in parseEnumFilter — so an
// attribute filter can only ever become a value that column can genuinely
// hold, never arbitrary text dropped into SQL.
var (
	jenisPrelistValues = []string{
		"keluarga", "UMKM", "OSS Perorangan", "OSS Badan Usaha",
		"bangunan_lain", "UB", "dummy", "KEK-KI",
	}
	keberadaanKeluargaValues = []string{
		"0. Tidak Ditemukan (STOP)", "1. Ditemukan", "2. Baru", "3. Meninggal",
		"4. Tidak Eligible", "5. Tidak dapat ditemui sampai akhir pendataan",
		"6. Keluarga Khusus",
	}
	statusValues = []string{
		"OPEN", "DRAFT", "SUBMITTED BY Pencacah", "APPROVED BY Pengawas",
		"REJECTED BY Pengawas", "REVOKED BY Pengawas", "SUBMITTED RESPONDENT",
		"EDITED BY Pengawas", "REJECTED BY Admin Kabupaten",
		"EDITED BY Admin Kabupaten", "COMPLETED BY Admin Kabupaten",
		"REVOKED BY Admin Kabupaten",
	}
)

// FilterOptions is the payload for GET /api/filter-options: the fixed
// dropdown choices for the Daftar menu's attribute filters, so the
// frontend never has to hardcode (and risk drifting from) these lists.
type FilterOptions struct {
	JenisPrelist       []string `json:"jenis_prelist"`
	KeberadaanKeluarga []string `json:"keberadaan_keluarga"`
	Status             []string `json:"status"`
}

// GetFilterOptions returns the attribute filter dropdown choices.
func GetFilterOptions() FilterOptions {
	return FilterOptions{
		JenisPrelist:       jenisPrelistValues,
		KeberadaanKeluarga: keberadaanKeluargaValues,
		Status:             statusValues,
	}
}

// matchKeySub and regsosekKeySub are the key sets behind the two flags.
// se2026_match is keyed by assignment_id_tdk, se2026_match_regsosek by
// assignment_id; both are matched against se2026_titik2.assignment_id.
//
// Measured before choosing this shape: se2026_titik2.assignment_id covers
// 88,633 of se2026_match's 88,636 distinct keys (99.997%) and all 166,505
// of se2026_match_regsosek's, so both flags carry real signal here. (The
// same join against se2026_match_regsosek.assignment_id instead — a
// different id space — overlaps se2026_match by exactly 0.)
//
// Neither key is unique on its own side (se2026_match has 89,437 rows for
// 88,636 keys), which would duplicate left rows under a plain JOIN and
// corrupt the row count and pagination. IN sidesteps that entirely: it is
// a set membership test, so a key appearing twice still yields one row.
const (
	matchKeySub    = "SELECT assignment_id_tdk FROM " + matchTable
	regsosekKeySub = "SELECT assignment_id FROM " + regsosekTable
)

// flagExpr builds the membership test for one flag. col is the qualified
// assignment_id to test and sub is one of the two constants above — both
// are compile-time constants in this package, never caller input.
func flagExpr(col, sub string) string {
	return fmt.Sprintf("(%s IN (%s))", col, sub)
}

// matchJoin attaches se2026_match's assignment_id_baru to each row. Only
// the Daftar list uses it (see listItems); the map needs no value column
// from that table, so queryPoints stays join-free.
//
// The subquery is grouped before the join, and that is load-bearing:
// assignment_id_tdk is not unique (89,437 rows for 88,636 distinct keys),
// so joining the raw table would turn 792 left rows into 1,593 and corrupt
// both the row count and pagination. Grouping first keeps it one row per
// key — verified: the count is 2,168,304 with and without the join.
//
// 787 of those 792 keys carry genuinely *different* assignment_id_baru
// values, so picking one with min()/argMin() would silently show a value
// that is wrong about half the time for them. They are concatenated
// instead, so a row with two new assignments reads as both — rare (0.9% of
// matched rows) and honest rather than quietly lossy.
//
// "1 AS ada" is what feeds Point.AdaAssignmentBaru here, replacing the IN
// test flagExpr would otherwise build: identical results on all 2,168,304
// rows (measured, 0 differences) while reading se2026_match once instead of
// twice. It is a literal rather than a test on aid_baru being non-empty, so
// it stays correct even if that column ever holds blanks.
//
// Aliases are deliberately not named after their source columns: an alias
// like "AS assignment_id_baru" would shadow the column of that name inside
// the aggregate beside it, which ClickHouse rejects as an aggregate nested
// in an aggregate.
const matchJoin = `LEFT JOIN (
		SELECT
			assignment_id_tdk,
			1 AS ada,
			arrayStringConcat(arraySort(groupUniqArray(assignment_id_baru)), ', ') AS aid_baru
		FROM ` + matchTable + `
		GROUP BY assignment_id_tdk
	) AS m ON t.assignment_id = m.assignment_id_tdk`

// FlagYes and FlagNo are the two values a flag filter can take over the
// wire; "" means the filter is unset. Kept as strings rather than a *bool
// so the filter reads the same as every other optional filter here.
const (
	FlagYes = "1"
	FlagNo  = "0"
)

// ParseFlag validates one of the two "ditemukan di …" filter values.
// Anything other than "", "1" or "0" is rejected rather than coerced, so a
// typo surfaces as a 400 instead of silently filtering the wrong way.
func ParseFlag(raw string) (string, error) {
	switch raw {
	case "", FlagYes, FlagNo:
		return raw, nil
	}
	return "", fmt.Errorf("invalid flag filter value %q: expected %q or %q", raw, FlagYes, FlagNo)
}

// flagClause turns a validated flag value into a WHERE fragment.
func flagClause(sub, val string) string {
	expr := flagExpr("assignment_id", sub)
	if val == FlagNo {
		return "NOT " + expr
	}
	return expr
}

// EmptyValue is the sentinel a caller passes to explicitly filter for rows
// where the column itself is blank, as distinct from leaving the filter
// unset entirely. Parse*'s zero value ("") already means "no filter", so
// plain "" can't also stand for "the column is blank" — this sentinel
// disambiguates the two states over the wire (e.g. ?jenisPrelist=__EMPTY__).
const EmptyValue = "__EMPTY__"

// parseEnumFilter validates raw against allowed, the closed set of values
// its target column can hold. Empty input means "no filter"; EmptyValue
// means "filter for a blank column"; anything else must match allowed
// exactly, since only whitelisted strings ever reach the SQL WHERE clause
// this feeds — see (Filter).clause.
func parseEnumFilter(raw string, allowed []string) (string, error) {
	if raw == "" {
		return "", nil
	}
	if raw == EmptyValue {
		return EmptyValue, nil
	}
	for _, v := range allowed {
		if raw == v {
			return raw, nil
		}
	}
	return "", fmt.Errorf("invalid filter value %q", raw)
}

// ParseJenisPrelist validates a Jenis Prelist filter value.
func ParseJenisPrelist(raw string) (string, error) {
	return parseEnumFilter(raw, jenisPrelistValues)
}

// ParseKeberadaanKeluarga validates a Keberadaan Keluarga filter value.
func ParseKeberadaanKeluarga(raw string) (string, error) {
	return parseEnumFilter(raw, keberadaanKeluargaValues)
}

// ParseStatus validates a Status filter value.
func ParseStatus(raw string) (string, error) {
	return parseEnumFilter(raw, statusValues)
}

// Filter narrows a query down to a wilayah and, optionally, a handful of
// row attributes. The five wilayah fields come from level_6_full_code:
// KabKota is characters 1-4, Kecamatan is characters 5-7, Desa is
// characters 8-10, SLS is characters 11-14, SubSLS is characters 15-16
// (its last two). Each is meaningless without the one above it — see
// Validate. JenisPrelist, KeberadaanKeluarga and Status are independent of
// wilayah and of each other: "" (unset), EmptyValue (blank column), or one
// of that column's known values (see the Parse* functions above).
type Filter struct {
	KabKota   string
	Kecamatan string
	Desa      string
	SLS       string
	SubSLS    string

	JenisPrelist       string
	KeberadaanKeluarga string
	Status             string

	// FlagBaru and FlagRegsosek filter on the two membership flags:
	// FlagYes keeps only rows found in that table, FlagNo only rows not
	// found in it, "" leaves the filter off. Validated by ParseFlag.
	FlagBaru     string
	FlagRegsosek string

	// Search is free-text user input matched against nama_assignment
	// (see clause). Unlike every other field above, it is never validated
	// against a whitelist — it's bound as a query parameter instead of
	// being interpolated into the SQL text, so it's safe as-is.
	Search string
}

// FullCode reconstructs the 16-digit level_6_full_code the five wilayah
// fields pin exactly (4+3+3+4+2 digits) when all of them are set — meant
// for callers that need a single SubSLS pinned down, like the PDF report
// or the SubSLS boundary polygon lookup. Meaningless (and not guaranteed
// to be 16 characters) unless every level down to SubSLS is set.
func (f Filter) FullCode() string {
	return f.KabKota + f.Kecamatan + f.Desa + f.SLS + f.SubSLS
}

// Validate reports an error if a narrower field is set without its parent,
// since each code is only unique within its parent.
func (f Filter) Validate() error {
	if f.Kecamatan != "" && f.KabKota == "" {
		return fmt.Errorf("kecamatan filter requires a kabkota filter")
	}
	if f.Desa != "" && f.Kecamatan == "" {
		return fmt.Errorf("desa/kelurahan filter requires a kecamatan filter")
	}
	if f.SLS != "" && f.Desa == "" {
		return fmt.Errorf("SLS filter requires a desa/kelurahan filter")
	}
	if f.SubSLS != "" && f.SLS == "" {
		return fmt.Errorf("SubSLS filter requires an SLS filter")
	}
	return nil
}

// clause builds the WHERE fragment for filter, plus any bind arguments it
// needs. Every field except Search is either a regex-validated digit
// string or matched against a closed whitelist (see Parse* above), so it's
// safe to interpolate directly into the SQL text. Search is genuine
// free-text user input, so it's never interpolated — it's passed back as a
// bind arg for the ? placeholder in the search fragment, and the caller
// must forward args to conn.Query/QueryRow alongside the query text.
func (f Filter) clause() (string, []any) {
	parts := make([]string, 0, 6)
	var args []any
	// The five wilayah levels are hierarchical and Validate rejects a level
	// whose parent is unset, so whatever is set always forms a contiguous
	// prefix of level_6_full_code — which FullCode already concatenates.
	// That lets one startsWith replace what used to be five separate
	// substring(...) = ... predicates, and it matters a lot: ClickHouse
	// rewrites startsWith on the sort key into a primary-key range
	// (level_6_full_code is the first sort column), while substring() is
	// opaque to the index and forces a full scan. Measured on 2.17M rows,
	// one kecamatan: 2,184,688 rows read before vs 131,072 after.
	//
	// Only valid for a filter that has been through Validate — callers get
	// that via parseFilter. An unvalidated Filter with a gap (say Desa set
	// but KabKota empty) would build a prefix that means something else.
	if prefix := f.FullCode(); prefix != "" {
		parts = append(parts, fmt.Sprintf("startsWith(level_6_full_code, '%s')", prefix))
	}
	if f.JenisPrelist != "" {
		parts = append(parts, attrClause("jenis_prelist_root", f.JenisPrelist))
	}
	if f.KeberadaanKeluarga != "" {
		parts = append(parts, attrClause("keberadaan_keluarga", f.KeberadaanKeluarga))
	}
	if f.Status != "" {
		parts = append(parts, attrClause("assignment_status_alias", f.Status))
	}
	// Both flags cost a set build (~90k and ~167k keys), so the fragment is
	// only added when the filter is actually on — an unfiltered viewport
	// query pays nothing for these.
	if f.FlagBaru != "" {
		parts = append(parts, flagClause(matchKeySub, f.FlagBaru))
	}
	if f.FlagRegsosek != "" {
		parts = append(parts, flagClause(regsosekKeySub, f.FlagRegsosek))
	}
	if f.Search != "" {
		// positionCaseInsensitive is a plain substring search, not a LIKE
		// pattern, so there's no %/_ wildcard to escape on top of the bind
		// parameter already keeping the raw text out of the SQL text.
		parts = append(parts, "positionCaseInsensitive(nama_assignment, ?) > 0")
		args = append(args, f.Search)
	}
	if len(parts) == 0 {
		return "1", nil
	}
	return strings.Join(parts, " AND "), args
}

// attrClause builds an equality WHERE fragment for one of the attribute
// filters. val has already passed parseEnumFilter against a fixed
// whitelist (see ParseJenisPrelist etc.), so it's always either EmptyValue
// or a known-safe string — never arbitrary text — by the time it lands
// here.
func attrClause(col, val string) string {
	if val == EmptyValue {
		return fmt.Sprintf("%s = ''", col)
	}
	return fmt.Sprintf("%s = '%s'", col, val)
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// Point is a single map marker with the fields needed for the hover tooltip.
type Point struct {
	Lat          float64 `json:"lat"`
	Lon          float64 `json:"lon"`
	AssignmentID string  `json:"assignment_id"`
	Nama         string  `json:"nama"`
	Alamat       string  `json:"alamat"`
	SubSLS       string  `json:"subsls"`
	JenisPrelist string  `json:"jenis_prelist"`
	// KeberadaanUsaha is scanned from the table's jumlah_usaha column
	// (renamed there from keberadaan_usaha; it always held a count of usaha
	// at the point, not a 0/1 presence flag). int32 to match that column's
	// type — a uint8 would fail to scan the ~1.8k rows whose value exceeds
	// 255. Still fetched on every query (unused fields cost nothing extra
	// here) even though it's temporarily hidden from every display —
	// tooltip, Daftar table, PDF and Excel all skip rendering it for now;
	// see NomorBangunan below for what replaced it in those views.
	KeberadaanUsaha int32 `json:"keberadaan_usaha"`
	// NomorBangunan is shown wherever KeberadaanUsaha used to be: tooltip,
	// Daftar table, PDF and Excel. int32 to match the column (its one
	// sentinel/overflow-looking value is math.MaxInt32, which still scans
	// fine at this width).
	NomorBangunan      int32  `json:"nomor_bangunan"`
	KeberadaanKeluarga string `json:"keberadaan_keluarga"`
	Status             string `json:"status"`
	// AdaAssignmentBaru and AdaRegsosek report whether this row's
	// assignment_id appears in se2026_match (as assignment_id_tdk) and in
	// se2026_match_regsosek respectively. They are presence flags only —
	// neither source table contributes any other column here.
	AdaAssignmentBaru bool `json:"ada_assignment_baru"`
	AdaRegsosek       bool `json:"ada_regsosek"`
	// AssignmentIDBaru is se2026_match.assignment_id_baru for this row, or
	// "" when there is none. Only populated by the Daftar list path (see
	// matchJoin) — /api/points doesn't join that table, so it's omitted
	// from the map payload rather than shipped empty on every marker. For
	// the 0.9% of matched rows whose key maps to more than one new
	// assignment, this holds all of them, comma-separated.
	AssignmentIDBaru string `json:"assignment_id_baru,omitempty"`
	// Catatan is free-text field notes — only populated by ListAll (the
	// PDF/Excel report path, via listItems' includeCatatan). Left "" for
	// /api/points and /api/list, which don't select this column at all:
	// it can run to over a thousand characters and isn't meant for
	// on-screen browsing, only for the downloaded report.
	Catatan string `json:"catatan,omitempty"`
}

// Cluster is an aggregated grid cell representing many points.
type Cluster struct {
	Lat   float64 `json:"lat"`
	Lon   float64 `json:"lon"`
	Count uint64  `json:"count"`
}

// Response is what /api/points returns.
type Response struct {
	Type     string    `json:"type"` // "points" or "clusters"
	Total    uint64    `json:"total"`
	Points   []Point   `json:"points"`
	Clusters []Cluster `json:"clusters"`
}

// Bounds summarizes the full dataset's geographic extent and row count, used
// to set the map's initial view.
type Bounds struct {
	MinLat, MaxLat float64 `json:"-"`
	MinLon, MaxLon float64 `json:"-"`
	Total          uint64  `json:"total"`
}

type BoundsJSON struct {
	MinLat float64 `json:"min_lat"`
	MaxLat float64 `json:"max_lat"`
	MinLon float64 `json:"min_lon"`
	MaxLon float64 `json:"max_lon"`
	Total  uint64  `json:"total"`
}

// Service executes the queries against ClickHouse.
type Service struct {
	conn driver.Conn
}

func NewService(conn driver.Conn) *Service {
	return &Service{conn: conn}
}

const validCoords = "latitude_ppl != 0 AND longitude_ppl != 0 AND isFinite(latitude_ppl) AND isFinite(longitude_ppl)"

func bboxClause(b BBox) string {
	return fmt.Sprintf(
		"latitude_ppl BETWEEN %s AND %s AND longitude_ppl BETWEEN %s AND %s",
		fFloat(b.MinLat), fFloat(b.MaxLat), fFloat(b.MinLon), fFloat(b.MaxLon),
	)
}

func fFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// Query returns individual points if the viewport is sparse, or grid
// clusters otherwise. zoom is the Leaflet zoom level (0-20+) and only
// affects cluster cell size, never correctness. filter, if set, restricts
// results to a kabupaten/kota and/or kecamatan.
func (s *Service) Query(ctx context.Context, b BBox, zoom int, filter Filter) (Response, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	// Cluster first, then decide. The grid aggregation already carries a
	// count per cell, so summing those gives the viewport total for free —
	// which is what the separate count() this used to run first was for.
	// Both queries read exactly the same rows under the same WHERE, so
	// dropping one halves the database work for any viewport dense enough
	// to be shown as clusters (measured on an unfiltered province-wide
	// view: ~600k rows scanned per query, ~30ms, twice).
	clusters, err := s.queryClusters(ctx, b, zoom, filter)
	if err != nil {
		return Response{}, fmt.Errorf("points: query clusters: %w", err)
	}

	var total uint64
	for _, c := range clusters {
		total += c.Count
	}

	// Summing only works while the cluster list is complete. It's capped at
	// ClusterLimit — far more cells than a screenful of ~60px cells ever
	// needs, so this is a safety net rather than a normal path — and a
	// truncated list would understate the total, so count properly instead.
	if len(clusters) >= ClusterLimit {
		total, err = s.count(ctx, b, filter)
		if err != nil {
			return Response{}, fmt.Errorf("points: count: %w", err)
		}
	}

	if total > IndividualLimit {
		return Response{Type: "clusters", Total: total, Clusters: clusters}, nil
	}

	// Sparse enough to draw every point individually. Still two queries in
	// this branch, same as before — but at this zoom the grid has roughly
	// one cell per point, so the aggregation it replaced was cheap.
	pts, err := s.queryPoints(ctx, b, filter)
	if err != nil {
		return Response{}, fmt.Errorf("points: query points: %w", err)
	}
	return Response{Type: "points", Total: total, Points: pts}, nil
}

func (s *Service) count(ctx context.Context, b BBox, filter Filter) (uint64, error) {
	where, args := filter.clause()
	q := fmt.Sprintf(
		"SELECT count() FROM %s WHERE %s AND %s AND %s",
		table, validCoords, bboxClause(b), where,
	)
	row := s.conn.QueryRow(ctx, q, args...)
	var n uint64
	if err := row.Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

func (s *Service) queryPoints(ctx context.Context, b BBox, filter Filter) ([]Point, error) {
	where, args := filter.clause()
	q := fmt.Sprintf(`SELECT
		assignment_id, nama_assignment, alamat, level_6_full_code, jenis_prelist_root,
		jumlah_usaha, nomor_bangunan, keberadaan_keluarga, assignment_status_alias,
		latitude_ppl, longitude_ppl,
		%s, %s
	FROM %s
	WHERE %s AND %s AND %s
	LIMIT %d`,
		flagExpr("assignment_id", matchKeySub), flagExpr("assignment_id", regsosekKeySub),
		table, validCoords, bboxClause(b), where, IndividualLimit)

	rows, err := s.conn.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	pts := make([]Point, 0, 512)
	for rows.Next() {
		var p Point
		// ClickHouse types an IN expression as UInt8, which the driver
		// won't scan straight into a bool — hence the two temporaries.
		var adaBaru, adaRegsosek uint8
		if err := rows.Scan(
			&p.AssignmentID, &p.Nama, &p.Alamat, &p.SubSLS, &p.JenisPrelist,
			&p.KeberadaanUsaha, &p.NomorBangunan, &p.KeberadaanKeluarga, &p.Status,
			&p.Lat, &p.Lon, &adaBaru, &adaRegsosek,
		); err != nil {
			return nil, err
		}
		p.AdaAssignmentBaru = adaBaru == 1
		p.AdaRegsosek = adaRegsosek == 1
		pts = append(pts, p)
	}
	return pts, rows.Err()
}

// CellSizeForZoom returns a grid cell size, in degrees of longitude, tuned
// so each cluster covers roughly 60 screen pixels at that Leaflet zoom
// level (256px tiles, doubling every zoom level). Exported so the match
// map in internal/regsosek clusters at exactly the same scale as this one.
func CellSizeForZoom(zoom int) float64 {
	if zoom < 0 {
		zoom = 0
	}
	if zoom > 20 {
		zoom = 20
	}
	const targetPixels = 60.0
	return targetPixels * 360.0 / (256.0 * math.Pow(2, float64(zoom)))
}

func (s *Service) queryClusters(ctx context.Context, b BBox, zoom int, filter Filter) ([]Cluster, error) {
	cell := CellSizeForZoom(zoom)
	cellStr := fFloat(cell)

	where, args := filter.clause()
	q := fmt.Sprintf(`SELECT
		floor(latitude_ppl / %s) * %s + %s / 2 AS glat,
		floor(longitude_ppl / %s) * %s + %s / 2 AS glon,
		count() AS cnt
	FROM %s
	WHERE %s AND %s AND %s
	GROUP BY glat, glon
	ORDER BY cnt DESC
	LIMIT %d`, cellStr, cellStr, cellStr, cellStr, cellStr, cellStr, table, validCoords, bboxClause(b), where, ClusterLimit)

	rows, err := s.conn.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	clusters := make([]Cluster, 0, 512)
	for rows.Next() {
		var c Cluster
		if err := rows.Scan(&c.Lat, &c.Lon, &c.Count); err != nil {
			return nil, err
		}
		clusters = append(clusters, c)
	}
	return clusters, rows.Err()
}

// DatasetBounds computes the geographic extent and total row count of all
// valid-coordinate rows. Intended to be called once at startup (and cached)
// since it scans the whole table.
func (s *Service) DatasetBounds(ctx context.Context) (Bounds, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	q := fmt.Sprintf(`SELECT
		min(latitude_ppl), max(latitude_ppl),
		min(longitude_ppl), max(longitude_ppl),
		count()
	FROM %s WHERE %s`, table, validCoords)

	row := s.conn.QueryRow(ctx, q)
	var b Bounds
	if err := row.Scan(&b.MinLat, &b.MaxLat, &b.MinLon, &b.MaxLon, &b.Total); err != nil {
		return Bounds{}, err
	}
	return b, nil
}

// KabKotaInfo describes one kabupaten/kota filter option: its code, a
// human name where known (see kabkotaNames), how many rows it has, and a
// bounding box to fit the map to when it's selected.
type KabKotaInfo struct {
	Code                           string `json:"code"`
	Name                           string `json:"name"`
	Total                          uint64 `json:"total"`
	MinLat, MaxLat, MinLon, MaxLon float64
}

type KabKotaInfoJSON struct {
	Code   string  `json:"code"`
	Name   string  `json:"name"`
	Total  uint64  `json:"total"`
	MinLat float64 `json:"min_lat"`
	MaxLat float64 `json:"max_lat"`
	MinLon float64 `json:"min_lon"`
	MaxLon float64 `json:"max_lon"`
}

// KabKotaList returns every kabupaten/kota actually present in the data,
// ordered by code ascending, each with a bounding box for the map to fit
// to when selected. The bbox uses the 1st/99th percentile of coordinates
// rather than true min/max so the occasional bad-GPS outlier doesn't blow
// up the zoom level.
func (s *Service) KabKotaList(ctx context.Context) ([]KabKotaInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	q := fmt.Sprintf(`SELECT
		substring(level_6_full_code, 1, 4) AS kabkota,
		count(),
		quantile(0.01)(latitude_ppl), quantile(0.99)(latitude_ppl),
		quantile(0.01)(longitude_ppl), quantile(0.99)(longitude_ppl)
	FROM %s
	WHERE %s AND length(level_6_full_code) >= 4
	GROUP BY kabkota
	ORDER BY kabkota ASC`, table, validCoords)

	rows, err := s.conn.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := make([]KabKotaInfo, 0, 32)
	for rows.Next() {
		var k KabKotaInfo
		if err := rows.Scan(&k.Code, &k.Total, &k.MinLat, &k.MaxLat, &k.MinLon, &k.MaxLon); err != nil {
			return nil, err
		}
		k.Name = KabKotaName(k.Code)
		list = append(list, k)
	}
	return list, rows.Err()
}

// KecamatanInfo describes one kecamatan filter option within a
// kabupaten/kota: its 3-digit code, row count, and a bounding box (same
// 1st/99th-percentile approach as KabKotaInfo). ClickHouse itself has no
// name reference table for kecamatan — the API layer enriches the
// serialized KecamatanInfoJSON with a name from the optional PostGIS
// database when it's configured (see internal/mapdb.KecamatanNames); the
// frontend falls back to "Kec. <code>" when no name is available.
type KecamatanInfo struct {
	Code                           string `json:"code"`
	Total                          uint64 `json:"total"`
	MinLat, MaxLat, MinLon, MaxLon float64
}

// KecamatanInfoJSON is the API shape for a kecamatan option. Name is ""
// unless the server enriched it from the optional PostGIS database (see
// internal/mapdb.KecamatanNames) — the frontend falls back to showing
// Code alone when Name is blank.
type KecamatanInfoJSON struct {
	Code   string  `json:"code"`
	Name   string  `json:"name"`
	Total  uint64  `json:"total"`
	MinLat float64 `json:"min_lat"`
	MaxLat float64 `json:"max_lat"`
	MinLon float64 `json:"min_lon"`
	MaxLon float64 `json:"max_lon"`
}

// KecamatanList returns every kecamatan present within one kabupaten/kota,
// ordered by code ascending. kabkota must be a validated 4-digit code (see
// ParseKabKota) — this is always scoped to one kabupaten/kota since
// kecamatan codes repeat across them.
func (s *Service) KecamatanList(ctx context.Context, kabkota string) ([]KecamatanInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	q := fmt.Sprintf(`SELECT
		substring(level_6_full_code, 5, 3) AS kec,
		count(),
		quantile(0.01)(latitude_ppl), quantile(0.99)(latitude_ppl),
		quantile(0.01)(longitude_ppl), quantile(0.99)(longitude_ppl)
	FROM %s
	WHERE %s AND length(level_6_full_code) >= 7 AND substring(level_6_full_code, 1, 4) = '%s'
	GROUP BY kec
	ORDER BY kec ASC`, table, validCoords, kabkota)

	rows, err := s.conn.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := make([]KecamatanInfo, 0, 32)
	for rows.Next() {
		var k KecamatanInfo
		if err := rows.Scan(&k.Code, &k.Total, &k.MinLat, &k.MaxLat, &k.MinLon, &k.MaxLon); err != nil {
			return nil, err
		}
		list = append(list, k)
	}
	return list, rows.Err()
}

// DesaInfo describes one desa/kelurahan filter option within a kecamatan:
// its 3-digit code, row count, and a bounding box (same 1st/99th-percentile
// approach as KabKotaInfo). Same name-enrichment story as KecamatanInfo —
// see DesaInfoJSON and internal/mapdb.DesaNames.
type DesaInfo struct {
	Code                           string `json:"code"`
	Total                          uint64 `json:"total"`
	MinLat, MaxLat, MinLon, MaxLon float64
}

// DesaInfoJSON is the API shape for a desa/kelurahan option. Name is ""
// unless the server enriched it from the optional PostGIS database.
type DesaInfoJSON struct {
	Code   string  `json:"code"`
	Name   string  `json:"name"`
	Total  uint64  `json:"total"`
	MinLat float64 `json:"min_lat"`
	MaxLat float64 `json:"max_lat"`
	MinLon float64 `json:"min_lon"`
	MaxLon float64 `json:"max_lon"`
}

// DesaList returns every desa/kelurahan present within one kabupaten/kota +
// kecamatan combination, ordered by code ascending. Both codes must
// already be validated (ParseKabKota / ParseKecamatan) — a desa code
// repeats across different kecamatan, so both parents are required to
// disambiguate it.
func (s *Service) DesaList(ctx context.Context, kabkota, kecamatan string) ([]DesaInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	q := fmt.Sprintf(`SELECT
		substring(level_6_full_code, 8, 3) AS desa,
		count(),
		quantile(0.01)(latitude_ppl), quantile(0.99)(latitude_ppl),
		quantile(0.01)(longitude_ppl), quantile(0.99)(longitude_ppl)
	FROM %s
	WHERE %s AND length(level_6_full_code) >= 10
		AND substring(level_6_full_code, 1, 4) = '%s'
		AND substring(level_6_full_code, 5, 3) = '%s'
	GROUP BY desa
	ORDER BY desa ASC`, table, validCoords, kabkota, kecamatan)

	rows, err := s.conn.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := make([]DesaInfo, 0, 32)
	for rows.Next() {
		var d DesaInfo
		if err := rows.Scan(&d.Code, &d.Total, &d.MinLat, &d.MaxLat, &d.MinLon, &d.MaxLon); err != nil {
			return nil, err
		}
		list = append(list, d)
	}
	return list, rows.Err()
}

// SLSInfo describes one Kode SLS filter option within a desa/kelurahan:
// its 4-digit code, row count, and a bounding box (same 1st/99th-percentile
// approach as KabKotaInfo). No name reference table exists for SLS either
// (it's an enumeration-area code, not a named place), so the code is the
// label — frontend renders it as "SLS <code>".
type SLSInfo struct {
	Code                           string `json:"code"`
	Total                          uint64 `json:"total"`
	MinLat, MaxLat, MinLon, MaxLon float64
}

type SLSInfoJSON struct {
	Code   string  `json:"code"`
	Total  uint64  `json:"total"`
	MinLat float64 `json:"min_lat"`
	MaxLat float64 `json:"max_lat"`
	MinLon float64 `json:"min_lon"`
	MaxLon float64 `json:"max_lon"`
}

// SLSList returns every Kode SLS present within one kabupaten/kota +
// kecamatan + desa/kelurahan combination, ordered by code ascending (the
// filter dropdown reads better sorted by code at this level than by
// count, unlike the coarser levels above it). All three codes must
// already be validated — an SLS code repeats across different
// desa/kelurahan, so all three parents are required to disambiguate it.
func (s *Service) SLSList(ctx context.Context, kabkota, kecamatan, desa string) ([]SLSInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	q := fmt.Sprintf(`SELECT
		substring(level_6_full_code, 11, 4) AS sls,
		count(),
		quantile(0.01)(latitude_ppl), quantile(0.99)(latitude_ppl),
		quantile(0.01)(longitude_ppl), quantile(0.99)(longitude_ppl)
	FROM %s
	WHERE %s AND length(level_6_full_code) >= 14
		AND substring(level_6_full_code, 1, 4) = '%s'
		AND substring(level_6_full_code, 5, 3) = '%s'
		AND substring(level_6_full_code, 8, 3) = '%s'
	GROUP BY sls
	ORDER BY sls ASC`, table, validCoords, kabkota, kecamatan, desa)

	rows, err := s.conn.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := make([]SLSInfo, 0, 64)
	for rows.Next() {
		var sl SLSInfo
		if err := rows.Scan(&sl.Code, &sl.Total, &sl.MinLat, &sl.MaxLat, &sl.MinLon, &sl.MaxLon); err != nil {
			return nil, err
		}
		list = append(list, sl)
	}
	return list, rows.Err()
}

// SubSLSInfo describes one Kode SubSLS filter option within an SLS: its
// 2-digit code, row count, and a bounding box (same 1st/99th-percentile
// approach as KabKotaInfo). No name reference table exists for SubSLS
// either, so the code is the label — frontend renders it as "SubSLS <code>".
type SubSLSInfo struct {
	Code                           string `json:"code"`
	Total                          uint64 `json:"total"`
	MinLat, MaxLat, MinLon, MaxLon float64
}

type SubSLSInfoJSON struct {
	Code   string  `json:"code"`
	Total  uint64  `json:"total"`
	MinLat float64 `json:"min_lat"`
	MaxLat float64 `json:"max_lat"`
	MinLon float64 `json:"min_lon"`
	MaxLon float64 `json:"max_lon"`
}

// SubSLSList returns every Kode SubSLS present within one kabupaten/kota +
// kecamatan + desa/kelurahan + SLS combination, ordered by code ascending
// (same reasoning as SLSList). All four codes must already be validated —
// a SubSLS code repeats across different SLS, so all four parents are
// required to disambiguate it.
func (s *Service) SubSLSList(ctx context.Context, kabkota, kecamatan, desa, sls string) ([]SubSLSInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	q := fmt.Sprintf(`SELECT
		substring(level_6_full_code, 15, 2) AS subsls,
		count(),
		quantile(0.01)(latitude_ppl), quantile(0.99)(latitude_ppl),
		quantile(0.01)(longitude_ppl), quantile(0.99)(longitude_ppl)
	FROM %s
	WHERE %s AND length(level_6_full_code) >= 16
		AND substring(level_6_full_code, 1, 4) = '%s'
		AND substring(level_6_full_code, 5, 3) = '%s'
		AND substring(level_6_full_code, 8, 3) = '%s'
		AND substring(level_6_full_code, 11, 4) = '%s'
	GROUP BY subsls
	ORDER BY subsls ASC`, table, validCoords, kabkota, kecamatan, desa, sls)

	rows, err := s.conn.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := make([]SubSLSInfo, 0, 16)
	for rows.Next() {
		var sub SubSLSInfo
		if err := rows.Scan(&sub.Code, &sub.Total, &sub.MinLat, &sub.MaxLat, &sub.MinLon, &sub.MaxLon); err != nil {
			return nil, err
		}
		list = append(list, sub)
	}
	return list, rows.Err()
}

// --- paginated table listing (the "Daftar" menu) ----------------------------

const (
	DefaultPageSize = 50
	MaxPageSize     = 200
)

// SortDir is a column sort direction for List.
type SortDir string

const (
	SortAsc  SortDir = "asc"
	SortDesc SortDir = "desc"
)

// ParseSortDir validates a sort direction, defaulting to ascending for
// anything empty or unrecognized rather than erroring — the table always
// has to render some order, and "asc" is a harmless fallback.
func ParseSortDir(v string) SortDir {
	if strings.EqualFold(v, string(SortDesc)) {
		return SortDesc
	}
	return SortAsc
}

// SQL renders the direction as the ASC/DESC keyword for an ORDER BY.
// Exported because internal/regsosek builds its own list query with the
// same sort semantics.
func (d SortDir) SQL() string {
	if d == SortDesc {
		return "DESC"
	}
	return "ASC"
}

// listSortColumns maps the sortable column keys the Daftar table exposes
// (matching Point's JSON field names, so the frontend can pass back
// whatever column it rendered) to the ClickHouse column actually sorted
// on. A whitelist, not a passthrough — sortBy is user-controlled and goes
// straight into the SQL text via listSortColumns' value, never its key.
var listSortColumns = map[string]string{
	"nama":                "nama_assignment",
	"alamat":              "alamat",
	"subsls":              "level_6_full_code",
	"jenis_prelist":       "jenis_prelist_root",
	"keberadaan_usaha":    "jumlah_usaha",
	"nomor_bangunan":      "nomor_bangunan",
	"keberadaan_keluarga": "keberadaan_keluarga",
	"status":              "assignment_status_alias",
	"assignment_id":       "assignment_id",

	// Columns that only exist in the list query's SELECT: the two
	// membership flags and the joined assignment_id_baru. listItems must
	// resolve them in the same SELECT the ORDER BY applies to — see the
	// comment there. ada_assignment_baru reads from matchJoin rather than
	// an IN test, since that query already joins se2026_match.
	"ada_assignment_baru": "m.ada",
	"ada_regsosek":        flagSortExpr(regsosekKeySub),
	"assignment_id_baru":  "m.aid_baru",
}

// flagSortExpr exists so listSortColumns can hold the flag expressions as
// values: they are built from this package's own constants, exactly like
// the plain column names beside them, and never from caller input. Only a
// value of this map ever reaches the SQL text — ParseSortColumn already
// reduced the caller's sortBy to one of its keys.
func flagSortExpr(sub string) string {
	return flagExpr("assignment_id", sub)
}

// DefaultSortColumn is used by ParseSortColumn when sortBy is empty or not
// a recognized key.
const DefaultSortColumn = "nama"

// ParseSortColumn validates a sort column key against listSortColumns,
// defaulting to DefaultSortColumn for anything empty or unrecognized —
// same forgiving-default approach as ParseSortDir, since the Daftar table
// always has to render some order.
func ParseSortColumn(v string) string {
	if _, ok := listSortColumns[v]; ok {
		return v
	}
	return DefaultSortColumn
}

// ListPage is one page of the "Daftar" table: unlike Query, it is not
// viewport-bound — it lists every row matching filter (the whole table, if
// filter is empty), sorted and paginated for browsing rather than mapping.
type ListPage struct {
	Total    uint64  `json:"total"`
	Page     int     `json:"page"`
	PageSize int     `json:"page_size"`
	Items    []Point `json:"items"`
}

// List returns one page of rows matching filter, sorted by sortColumn (a
// key from listSortColumns — pass it through ParseSortColumn first). page
// is 1-indexed; pageSize is clamped to [1, MaxPageSize].
func (s *Service) List(ctx context.Context, filter Filter, page, pageSize int, sortColumn string, dir SortDir) (ListPage, error) {
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

	total, err := s.listCount(ctx, filter)
	if err != nil {
		return ListPage{}, fmt.Errorf("points: list count: %w", err)
	}

	items, err := s.listItems(ctx, filter, page, pageSize, sortColumn, dir, false)
	if err != nil {
		return ListPage{}, fmt.Errorf("points: list items: %w", err)
	}

	return ListPage{Total: total, Page: page, PageSize: pageSize, Items: items}, nil
}

// ReportMaxRows caps how many rows ListAll will ever return. Reports are
// scoped to a desa/kelurahan or narrower (see prepareReport in
// internal/api); measured against live data, a desa runs ~700 rows at the
// median, ~18k at the 99th percentile and ~27k at the largest, so this
// clears every real desa with headroom while still bounding the damage if
// a much larger wilayah ever reaches ListAll. A report that does hit the
// cap says so in its header rather than truncating silently — see the
// truncated flag threaded through to pdfreport/xlsxreport.
const ReportMaxRows = 40000

// ListAll returns every row matching filter (up to ReportMaxRows), sorted
// by sortColumn, with no pagination. Intended for the PDF/Excel reports,
// which need the whole wilayah in one document rather than one page's
// worth — call sites should ensure filter is pinned at least down to a
// desa/kelurahan first, since anything wider is too big to be a report.
func (s *Service) ListAll(ctx context.Context, filter Filter, sortColumn string, dir SortDir) ([]Point, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	return s.listItems(ctx, filter, 1, ReportMaxRows, sortColumn, dir, true)
}

func (s *Service) listCount(ctx context.Context, filter Filter) (uint64, error) {
	where, args := filter.clause()
	q := fmt.Sprintf("SELECT count() FROM %s WHERE %s", table, where)
	row := s.conn.QueryRow(ctx, q, args...)
	var n uint64
	if err := row.Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// listItems fetches one page of rows. includeCatatan adds the catatan
// column to both the SELECT and the scan target — only ListAll (the
// report path) passes true; List (the Daftar table) leaves Point.Catatan
// unset, since that column isn't meant for on-screen browsing (see the
// field's doc comment on Point).
func (s *Service) listItems(ctx context.Context, filter Filter, page, pageSize int, sortColumn string, dir SortDir, includeCatatan bool) ([]Point, error) {
	offset := (page - 1) * pageSize
	sortCol, ok := listSortColumns[sortColumn]
	if !ok {
		sortCol = listSortColumns[DefaultSortColumn]
	}
	cols := "t.assignment_id, t.nama_assignment, t.alamat, t.level_6_full_code, t.jenis_prelist_root, t.jumlah_usaha, t.nomor_bangunan, t.keberadaan_keluarga, t.assignment_status_alias, t.latitude_ppl, t.longitude_ppl"
	if includeCatatan {
		cols += ", t.catatan"
	}
	// The joined/flag columns are resolved here rather than over the
	// already-paged rows, which would be ~25% faster: sorting by one of
	// them needs them available before ORDER BY/LIMIT, and one query shape
	// means they sort exactly like every other column. Measured on the
	// largest desa (27k rows), the whole page query goes 200ms -> 274ms.
	// The join keeps the left side's primary-key range intact — read_rows
	// goes 57,344 -> 146,781, and the difference is exactly se2026_match's
	// own 89,437 rows.
	cols += fmt.Sprintf(", m.ada, %s, m.aid_baru", flagExpr("t.assignment_id", regsosekKeySub))
	where, args := filter.clause()
	q := fmt.Sprintf(`SELECT
		%s
	FROM %s AS t
	%s
	WHERE %s
	ORDER BY %s %s
	LIMIT %d OFFSET %d`, cols, table, matchJoin, where, sortCol, dir.SQL(), pageSize, offset)

	rows, err := s.conn.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]Point, 0, pageSize)
	for rows.Next() {
		var p Point
		dest := []any{
			&p.AssignmentID, &p.Nama, &p.Alamat, &p.SubSLS, &p.JenisPrelist,
			&p.KeberadaanUsaha, &p.NomorBangunan, &p.KeberadaanKeluarga, &p.Status,
			&p.Lat, &p.Lon,
		}
		if includeCatatan {
			dest = append(dest, &p.Catatan)
		}
		// UInt8 in ClickHouse, bool in Point — same as queryPoints.
		// adaBaru comes from matchJoin's literal, so a LEFT JOIN miss
		// scans as UInt8's zero value.
		var adaBaru, adaRegsosek uint8
		dest = append(dest, &adaBaru, &adaRegsosek, &p.AssignmentIDBaru)
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		p.AdaAssignmentBaru = adaBaru == 1
		p.AdaRegsosek = adaRegsosek == 1
		items = append(items, p)
	}
	return items, rows.Err()
}
