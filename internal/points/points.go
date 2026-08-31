// Package points implements viewport-aware queries against the
// se2026_titik ClickHouse table: individual points when the visible area
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
	table = "se2026_titik"

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

// Filter narrows a query down to a wilayah. All four fields come from
// level_6_full_code: KabKota is characters 1-4, Kecamatan is characters
// 5-7, Desa is characters 8-10, SLS is characters 11-14. Each is
// meaningless without the one above it — see Validate.
type Filter struct {
	KabKota   string
	Kecamatan string
	Desa      string
	SLS       string
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
	return nil
}

func (f Filter) clause() string {
	parts := make([]string, 0, 4)
	if f.KabKota != "" {
		parts = append(parts, fmt.Sprintf("substring(level_6_full_code, 1, 4) = '%s'", f.KabKota))
	}
	if f.Kecamatan != "" {
		parts = append(parts, fmt.Sprintf("substring(level_6_full_code, 5, 3) = '%s'", f.Kecamatan))
	}
	if f.Desa != "" {
		parts = append(parts, fmt.Sprintf("substring(level_6_full_code, 8, 3) = '%s'", f.Desa))
	}
	if f.SLS != "" {
		parts = append(parts, fmt.Sprintf("substring(level_6_full_code, 11, 4) = '%s'", f.SLS))
	}
	if len(parts) == 0 {
		return "1"
	}
	return strings.Join(parts, " AND ")
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
	Lat                float64 `json:"lat"`
	Lon                float64 `json:"lon"`
	AssignmentID       string  `json:"assignment_id"`
	Nama               string  `json:"nama"`
	SubSLS             string  `json:"subsls"`
	JenisPrelist       string  `json:"jenis_prelist"`
	KeberadaanUsaha    uint8   `json:"keberadaan_usaha"`
	KeberadaanKeluarga string  `json:"keberadaan_keluarga"`
	Status             string  `json:"status"`
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

	total, err := s.count(ctx, b, filter)
	if err != nil {
		return Response{}, fmt.Errorf("points: count: %w", err)
	}

	if total <= IndividualLimit {
		pts, err := s.queryPoints(ctx, b, filter)
		if err != nil {
			return Response{}, fmt.Errorf("points: query points: %w", err)
		}
		return Response{Type: "points", Total: total, Points: pts}, nil
	}

	clusters, err := s.queryClusters(ctx, b, zoom, filter)
	if err != nil {
		return Response{}, fmt.Errorf("points: query clusters: %w", err)
	}
	return Response{Type: "clusters", Total: total, Clusters: clusters}, nil
}

func (s *Service) count(ctx context.Context, b BBox, filter Filter) (uint64, error) {
	q := fmt.Sprintf(
		"SELECT count() FROM %s WHERE %s AND %s AND %s",
		table, validCoords, bboxClause(b), filter.clause(),
	)
	row := s.conn.QueryRow(ctx, q)
	var n uint64
	if err := row.Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

func (s *Service) queryPoints(ctx context.Context, b BBox, filter Filter) ([]Point, error) {
	q := fmt.Sprintf(`SELECT
		assignment_id, nama_assignment, level_6_full_code, jenis_prelist_root,
		keberadaan_usaha, keberadaan_keluarga, assignment_status_alias,
		latitude_ppl, longitude_ppl
	FROM %s
	WHERE %s AND %s AND %s
	LIMIT %d`, table, validCoords, bboxClause(b), filter.clause(), IndividualLimit)

	rows, err := s.conn.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	pts := make([]Point, 0, 512)
	for rows.Next() {
		var p Point
		if err := rows.Scan(
			&p.AssignmentID, &p.Nama, &p.SubSLS, &p.JenisPrelist,
			&p.KeberadaanUsaha, &p.KeberadaanKeluarga, &p.Status,
			&p.Lat, &p.Lon,
		); err != nil {
			return nil, err
		}
		pts = append(pts, p)
	}
	return pts, rows.Err()
}

// cellSizeForZoom returns a grid cell size, in degrees of longitude, tuned
// so each cluster covers roughly 60 screen pixels at that Leaflet zoom
// level (256px tiles, doubling every zoom level).
func cellSizeForZoom(zoom int) float64 {
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
	cell := cellSizeForZoom(zoom)
	cellStr := fFloat(cell)

	q := fmt.Sprintf(`SELECT
		floor(latitude_ppl / %s) * %s + %s / 2 AS glat,
		floor(longitude_ppl / %s) * %s + %s / 2 AS glon,
		count() AS cnt
	FROM %s
	WHERE %s AND %s AND %s
	GROUP BY glat, glon
	ORDER BY cnt DESC
	LIMIT %d`, cellStr, cellStr, cellStr, cellStr, cellStr, cellStr, table, validCoords, bboxClause(b), filter.clause(), ClusterLimit)

	rows, err := s.conn.Query(ctx, q)
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
// most populous first, each with a bounding box for the map to fit to when
// selected. The bbox uses the 1st/99th percentile of coordinates rather
// than true min/max so the occasional bad-GPS outlier doesn't blow up the
// zoom level.
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
	ORDER BY count() DESC`, table, validCoords)

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
		k.Name = kabkotaName(k.Code)
		list = append(list, k)
	}
	return list, rows.Err()
}

// KecamatanInfo describes one kecamatan filter option within a
// kabupaten/kota: its 3-digit code, row count, and a bounding box (same
// 1st/99th-percentile approach as KabKotaInfo). There's no name reference
// table for kecamatan in this database, so the code itself is the label —
// see the frontend, which renders it as "Kec. <code>".
type KecamatanInfo struct {
	Code                           string `json:"code"`
	Total                          uint64 `json:"total"`
	MinLat, MaxLat, MinLon, MaxLon float64
}

type KecamatanInfoJSON struct {
	Code   string  `json:"code"`
	Total  uint64  `json:"total"`
	MinLat float64 `json:"min_lat"`
	MaxLat float64 `json:"max_lat"`
	MinLon float64 `json:"min_lon"`
	MaxLon float64 `json:"max_lon"`
}

// KecamatanList returns every kecamatan present within one kabupaten/kota,
// most populous first. kabkota must be a validated 4-digit code (see
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
	ORDER BY count() DESC`, table, validCoords, kabkota)

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
// approach as KabKotaInfo). No name reference table exists for desa either,
// so the code is the label — frontend renders it as "Desa/Kel. <code>".
type DesaInfo struct {
	Code                           string `json:"code"`
	Total                          uint64 `json:"total"`
	MinLat, MaxLat, MinLon, MaxLon float64
}

type DesaInfoJSON struct {
	Code   string  `json:"code"`
	Total  uint64  `json:"total"`
	MinLat float64 `json:"min_lat"`
	MaxLat float64 `json:"max_lat"`
	MinLon float64 `json:"min_lon"`
	MaxLon float64 `json:"max_lon"`
}

// DesaList returns every desa/kelurahan present within one kabupaten/kota +
// kecamatan combination, most populous first. Both codes must already be
// validated (ParseKabKota / ParseKecamatan) — a desa code repeats across
// different kecamatan, so both parents are required to disambiguate it.
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
	ORDER BY count() DESC`, table, validCoords, kabkota, kecamatan)

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
// kecamatan + desa/kelurahan combination, most populous first. All three
// codes must already be validated — an SLS code repeats across different
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
	ORDER BY count() DESC`, table, validCoords, kabkota, kecamatan, desa)

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
