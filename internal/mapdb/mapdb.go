// Package mapdb wraps an optional PostgreSQL/PostGIS connection used to
// serve SubSLS boundary polygons on the Peta map. Unlike the ClickHouse
// connection the rest of this app depends on, this one is optional — the
// map works fine without it, just without polygon overlays. See
// config.Config.MapEnabled.
package mapdb

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"se2026-titik-maps/internal/config"
)

// table/columns for the SubSLS polygon source. codeColumn holds the same
// 16-digit code as ClickHouse's level_6_full_code — 2-digit provinsi +
// 2-digit kabupaten/kota + 3-digit kecamatan + 3-digit desa/kelurahan +
// 4-digit SLS + 2-digit SubSLS — confirmed by direct lookup against known
// data. geomColumn is a PostGIS geometry column in SRID 4326 (WGS84,
// lat/lon), the same coordinate system Leaflet expects, so no
// reprojection is needed.
const (
	table      = "peta_sls_6400_rev"
	codeColumn = "idsubsls"
	geomColumn = "wkb_geometry"
)

// New opens a pooled connection to the PostGIS database and verifies it
// with a ping. Only call this when cfg.MapEnabled() is true.
func New(ctx context.Context, cfg *config.Config) (*pgxpool.Pool, error) {
	connStr := fmt.Sprintf(
		"postgres://%s:%s@%s:%d/%s?sslmode=disable&connect_timeout=5",
		cfg.MapUsername, cfg.MapPassword, cfg.MapHost, cfg.MapPort, cfg.MapDatabase,
	)
	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		return nil, fmt.Errorf("mapdb: open: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("mapdb: ping %s:%d: %w", cfg.MapHost, cfg.MapPort, err)
	}
	return pool, nil
}

// SubSLSPolygonGeoJSON returns the SubSLS boundary as a GeoJSON
// FeatureCollection (raw bytes, ready to write straight into an HTTP
// response) for the given 16-digit code. A code with no matching row
// yields an empty FeatureCollection (features: []), not an error — the
// caller decides whether that's worth a 404. code is passed as a query
// parameter, never interpolated into SQL, but callers should still run it
// through points.Parse* first so a malformed filter fails with a clear
// 400 instead of a silently-empty polygon.
func SubSLSPolygonGeoJSON(ctx context.Context, pool *pgxpool.Pool, code string) ([]byte, error) {
	rows, err := pool.Query(ctx, fmt.Sprintf(
		`SELECT coalesce(ST_AsGeoJSON(%s), '{}') FROM %s WHERE %s = $1`,
		geomColumn, table, codeColumn,
	), code)
	if err != nil {
		return nil, fmt.Errorf("mapdb: query: %w", err)
	}
	defer rows.Close()

	var features []string
	for rows.Next() {
		var geomJSON string
		if err := rows.Scan(&geomJSON); err != nil {
			return nil, fmt.Errorf("mapdb: scan: %w", err)
		}
		features = append(features, fmt.Sprintf(`{"type":"Feature","geometry":%s,"properties":{}}`, geomJSON))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mapdb: rows: %w", err)
	}

	return []byte(fmt.Sprintf(`{"type":"FeatureCollection","features":[%s]}`, strings.Join(features, ","))), nil
}

// splitKabKota breaks a 4-digit ClickHouse kabkota code (2-digit kdprov +
// 2-digit kdkab) into the two parts peta_sls_6400_rev keys by.
func splitKabKota(kabkotaCode string) (kdprov, kdkab string, err error) {
	if len(kabkotaCode) != 4 {
		return "", "", fmt.Errorf("mapdb: kabkota code must be 4 digits, got %q", kabkotaCode)
	}
	return kabkotaCode[:2], kabkotaCode[2:], nil
}

// KecamatanNames returns kdkec -> nmkec for every kecamatan within the
// given kabkota. A code with more than one distinct name on file (a data
// entry inconsistency, not expected but not impossible) ends up with
// whichever name its last matching row happened to have — good enough for
// a dropdown label, not worth a tie-breaking query.
func KecamatanNames(ctx context.Context, pool *pgxpool.Pool, kabkotaCode string) (map[string]string, error) {
	kdprov, kdkab, err := splitKabKota(kabkotaCode)
	if err != nil {
		return nil, err
	}
	rows, err := pool.Query(ctx,
		`SELECT DISTINCT kdkec, nmkec FROM `+table+` WHERE kdprov = $1 AND kdkab = $2`,
		kdprov, kdkab,
	)
	if err != nil {
		return nil, fmt.Errorf("mapdb: query kecamatan names: %w", err)
	}
	defer rows.Close()

	names := make(map[string]string)
	for rows.Next() {
		var code, name string
		if err := rows.Scan(&code, &name); err != nil {
			return nil, fmt.Errorf("mapdb: scan kecamatan name: %w", err)
		}
		if name != "" {
			names[code] = name
		}
	}
	return names, rows.Err()
}

// DesaNames returns kddesa -> nmdesa for every desa/kelurahan within the
// given kabkota + kecamatan.
func DesaNames(ctx context.Context, pool *pgxpool.Pool, kabkotaCode, kecamatanCode string) (map[string]string, error) {
	kdprov, kdkab, err := splitKabKota(kabkotaCode)
	if err != nil {
		return nil, err
	}
	rows, err := pool.Query(ctx,
		`SELECT DISTINCT kddesa, nmdesa FROM `+table+` WHERE kdprov = $1 AND kdkab = $2 AND kdkec = $3`,
		kdprov, kdkab, kecamatanCode,
	)
	if err != nil {
		return nil, fmt.Errorf("mapdb: query desa names: %w", err)
	}
	defer rows.Close()

	names := make(map[string]string)
	for rows.Next() {
		var code, name string
		if err := rows.Scan(&code, &name); err != nil {
			return nil, fmt.Errorf("mapdb: scan desa name: %w", err)
		}
		if name != "" {
			names[code] = name
		}
	}
	return names, rows.Err()
}
