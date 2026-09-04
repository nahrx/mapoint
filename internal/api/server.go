// Package api wires up the HTTP handlers: static frontend, /api/points,
// /api/bounds, /api/kabkota, /api/kecamatan, /api/desa, /api/sls,
// /api/subsls, /api/list, /api/list/pdf, /api/list/xlsx, /api/reg2022,
// /api/subsls-polygon and /healthz.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/jackc/pgx/v5/pgxpool"

	"se2026-titik-maps/internal/mapdb"
	"se2026-titik-maps/internal/pdfreport"
	"se2026-titik-maps/internal/points"
	"se2026-titik-maps/internal/regsosek"
	"se2026-titik-maps/internal/report"
	"se2026-titik-maps/internal/xlsxreport"
)

type Server struct {
	svc     *points.Service
	regsvc  *regsosek.Service
	conn    driver.Conn
	bounds  atomic.Pointer[points.Bounds]
	kabkota atomic.Pointer[[]points.KabKotaInfo]
	log     *slog.Logger

	// mapPool is nil when config.Config.MapEnabled() is false — the SubSLS
	// polygon feature is optional, and handleSubSLSPolygon degrades to a
	// clear "not configured" response rather than a nil-pointer panic.
	mapPool *pgxpool.Pool

	// auth is the single account gating every route except the login page
	// and /healthz — see auth.go. Never nil: config.Load refuses to start
	// without credentials.
	auth *auth
}

func NewServer(svc *points.Service, regsvc *regsosek.Service, conn driver.Conn, bounds points.Bounds, kabkota []points.KabKotaInfo, mapPool *pgxpool.Pool, authUsername, authPassword string, log *slog.Logger) *Server {
	s := &Server{svc: svc, regsvc: regsvc, conn: conn, mapPool: mapPool, auth: newAuth(authUsername, authPassword), log: log}
	s.bounds.Store(&bounds)
	s.kabkota.Store(&kabkota)
	return s
}

// SetBounds atomically replaces the cached dataset extent/total, used by a
// background refresher so /api/bounds reflects new data without a restart.
func (s *Server) SetBounds(b points.Bounds) {
	s.bounds.Store(&b)
}

// SetKabKota atomically replaces the cached kabupaten/kota filter list.
func (s *Server) SetKabKota(list []points.KabKotaInfo) {
	s.kabkota.Store(&list)
}

// Routes returns the full handler, including static file serving.
func (s *Server) Routes(staticFS http.FileSystem) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/points", s.handlePoints)
	mux.HandleFunc("GET /api/bounds", s.handleBounds)
	mux.HandleFunc("GET /api/kabkota", s.handleKabKota)
	mux.HandleFunc("GET /api/kecamatan", s.handleKecamatan)
	mux.HandleFunc("GET /api/desa", s.handleDesa)
	mux.HandleFunc("GET /api/sls", s.handleSLS)
	mux.HandleFunc("GET /api/subsls", s.handleSubSLS)
	mux.HandleFunc("GET /api/list", s.handleList)
	mux.HandleFunc("GET /api/list/pdf", s.handleListPDF)
	mux.HandleFunc("GET /api/list/xlsx", s.handleListXLSX)
	mux.HandleFunc("GET /api/filter-options", s.handleFilterOptions)
	mux.HandleFunc("GET /api/reg2022", s.handleReg2022)
	mux.HandleFunc("GET /api/reg2022/filter-options", s.handleReg2022FilterOptions)
	mux.HandleFunc("GET /api/reg2022/pdf", s.handleRegsosekPDF)
	mux.HandleFunc("GET /api/reg2022/xlsx", s.handleRegsosekXLSX)
	mux.HandleFunc("GET /api/match-points", s.handleMatchPoints)
	mux.HandleFunc("GET /api/match-bounds", s.handleMatchBounds)
	mux.HandleFunc("GET /api/subsls-polygon", s.handleSubSLSPolygon)
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /login", s.handleLoginPage(staticFS))
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)
	// The Daftar menu has its own URL (see nav.js's use of history.pushState)
	// so it survives a refresh or a direct link — but it's still the same
	// single-page app, so just serve the same index.html the SPA's router
	// (nav.js) uses to pick the right view on load.
	mux.HandleFunc("GET /daftar", s.serveIndex(staticFS))
	mux.HandleFunc("GET /reg2022", s.serveIndex(staticFS))
	mux.HandleFunc("GET /peta-match", s.serveIndex(staticFS))
	mux.Handle("/", http.FileServer(staticFS))

	// requireAuth sits outside the mux so it covers every route including
	// static assets — otherwise the whole frontend would be readable
	// without signing in, and only the data behind it protected. gzip sits
	// inside logging so the logged status/duration still describe the real
	// response, and outside the mux so static assets (leaflet.js,
	// style.css) get compressed too, not just the API.
	return withLogging(s.log, withGzip(s.requireAuth(mux)))
}

func (s *Server) serveIndex(staticFS http.FileSystem) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f, err := staticFS.Open("index.html")
		if err != nil {
			s.log.Error("open index.html failed", "err", err)
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		defer f.Close()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if _, err := io.Copy(w, f); err != nil {
			s.log.Error("write index.html failed", "err", err)
		}
	}
}

func (s *Server) handlePoints(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	bbox, err := points.ParseBBox(q.Get("minLat"), q.Get("maxLat"), q.Get("minLon"), q.Get("maxLon"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	zoom := 0
	if z := q.Get("zoom"); z != "" {
		parsed, err := strconv.Atoi(z)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid zoom")
			return
		}
		zoom = parsed
	}

	filter, err := parseFilter(q)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	resp, err := s.svc.Query(r.Context(), bbox, zoom, filter)
	if err != nil {
		if r.Context().Err() != nil {
			// Client navigated away / aborted the request — not a server
			// problem, don't log it as one.
			return
		}
		s.log.Error("points query failed", "err", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleBounds(w http.ResponseWriter, r *http.Request) {
	b := s.bounds.Load()
	writeJSON(w, http.StatusOK, points.BoundsJSON{
		MinLat: b.MinLat,
		MaxLat: b.MaxLat,
		MinLon: b.MinLon,
		MaxLon: b.MaxLon,
		Total:  b.Total,
	})
}

func (s *Server) handleKabKota(w http.ResponseWriter, r *http.Request) {
	list := *s.kabkota.Load()
	out := make([]points.KabKotaInfoJSON, len(list))
	for i, k := range list {
		out[i] = points.KabKotaInfoJSON{
			Code: k.Code, Name: k.Name, Total: k.Total,
			MinLat: k.MinLat, MaxLat: k.MaxLat, MinLon: k.MinLon, MaxLon: k.MaxLon,
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleFilterOptions serves the fixed dropdown choices for the Daftar
// menu's attribute filters (jenis prelist, keberadaan keluarga, status) —
// a static, closed set (see points.GetFilterOptions), not a database
// query, so there's nothing to cache or scope by wilayah here.
func (s *Server) handleFilterOptions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, points.GetFilterOptions())
}

func (s *Server) handleKecamatan(w http.ResponseWriter, r *http.Request) {
	kabkota, err := points.ParseKabKota(r.URL.Query().Get("kabkota"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if kabkota == "" {
		writeError(w, http.StatusBadRequest, "kabkota is required")
		return
	}

	list, err := s.svc.KecamatanList(r.Context(), kabkota)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		s.log.Error("kecamatan query failed", "err", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	names := s.kecamatanNames(r.Context(), kabkota)
	out := make([]points.KecamatanInfoJSON, len(list))
	for i, k := range list {
		out[i] = points.KecamatanInfoJSON{
			Code: k.Code, Name: names[k.Code], Total: k.Total,
			MinLat: k.MinLat, MaxLat: k.MaxLat, MinLon: k.MinLon, MaxLon: k.MaxLon,
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// kecamatanNames looks up code->name from the optional PostGIS database,
// returning a nil map (safe to index — Go just yields "") when it isn't
// configured or the query fails, so callers never need a nil check.
func (s *Server) kecamatanNames(ctx context.Context, kabkota string) map[string]string {
	if s.mapPool == nil {
		return nil
	}
	names, err := mapdb.KecamatanNames(ctx, s.mapPool, kabkota)
	if err != nil {
		s.log.Warn("kecamatan name lookup failed", "err", err)
		return nil
	}
	return names
}

func (s *Server) handleDesa(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	kabkota, err := points.ParseKabKota(q.Get("kabkota"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	kecamatan, err := points.ParseKecamatan(q.Get("kecamatan"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if kabkota == "" || kecamatan == "" {
		writeError(w, http.StatusBadRequest, "kabkota and kecamatan are required")
		return
	}

	list, err := s.svc.DesaList(r.Context(), kabkota, kecamatan)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		s.log.Error("desa query failed", "err", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	names := s.desaNames(r.Context(), kabkota, kecamatan)
	out := make([]points.DesaInfoJSON, len(list))
	for i, d := range list {
		out[i] = points.DesaInfoJSON{
			Code: d.Code, Name: names[d.Code], Total: d.Total,
			MinLat: d.MinLat, MaxLat: d.MaxLat, MinLon: d.MinLon, MaxLon: d.MaxLon,
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// desaNames is kecamatanNames's counterpart for desa/kelurahan — see
// kecamatanNames for the nil-map-is-fine contract.
func (s *Server) desaNames(ctx context.Context, kabkota, kecamatan string) map[string]string {
	if s.mapPool == nil {
		return nil
	}
	names, err := mapdb.DesaNames(ctx, s.mapPool, kabkota, kecamatan)
	if err != nil {
		s.log.Warn("desa name lookup failed", "err", err)
		return nil
	}
	return names
}

func (s *Server) handleSLS(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	kabkota, err := points.ParseKabKota(q.Get("kabkota"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	kecamatan, err := points.ParseKecamatan(q.Get("kecamatan"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	desa, err := points.ParseDesa(q.Get("desa"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if kabkota == "" || kecamatan == "" || desa == "" {
		writeError(w, http.StatusBadRequest, "kabkota, kecamatan and desa are required")
		return
	}

	list, err := s.svc.SLSList(r.Context(), kabkota, kecamatan, desa)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		s.log.Error("sls query failed", "err", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	names := s.slsNames(r.Context(), kabkota, kecamatan, desa)
	out := make([]points.SLSInfoJSON, len(list))
	for i, sl := range list {
		out[i] = points.SLSInfoJSON{
			Code: sl.Code, Name: names[sl.Code], Total: sl.Total,
			MinLat: sl.MinLat, MaxLat: sl.MaxLat, MinLon: sl.MinLon, MaxLon: sl.MaxLon,
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// slsNames looks up each Kode SLS's name in PostGIS. Same shape as
// kecamatanNames/desaNames: a nil map when PostGIS isn't configured or the
// lookup fails, so the dropdown quietly falls back to bare codes rather
// than the whole request failing over an optional label.
func (s *Server) slsNames(ctx context.Context, kabkota, kecamatan, desa string) map[string]string {
	if s.mapPool == nil {
		return nil
	}
	names, err := mapdb.SLSNames(ctx, s.mapPool, kabkota, kecamatan, desa)
	if err != nil {
		s.log.Warn("SLS name lookup failed", "err", err)
		return nil
	}
	return names
}

// maxSearchLen bounds the free-text ?search= param — generous for any
// real name, just enough to reject pathological input before it reaches
// ClickHouse.
const maxSearchLen = 200

// parseWilayah reads just the kabkota/kecamatan/desa/sls/subsls levels and
// checks that each is only set when its parent is too. Shared by every
// menu: /api/points and /api/list build on it via parseFilter, and
// /api/reg2022 uses it directly (that table has the same
// level_6_full_code, but none of the other filters).
func parseWilayah(q url.Values) (points.Filter, error) {
	kabkota, err := points.ParseKabKota(q.Get("kabkota"))
	if err != nil {
		return points.Filter{}, err
	}
	kecamatan, err := points.ParseKecamatan(q.Get("kecamatan"))
	if err != nil {
		return points.Filter{}, err
	}
	desa, err := points.ParseDesa(q.Get("desa"))
	if err != nil {
		return points.Filter{}, err
	}
	sls, err := points.ParseSLS(q.Get("sls"))
	if err != nil {
		return points.Filter{}, err
	}
	subsls, err := points.ParseSubSLS(q.Get("subsls"))
	if err != nil {
		return points.Filter{}, err
	}
	w := points.Filter{KabKota: kabkota, Kecamatan: kecamatan, Desa: desa, SLS: sls, SubSLS: subsls}
	if err := w.Validate(); err != nil {
		return points.Filter{}, err
	}
	return w, nil
}

// parseSearch reads and length-checks the free-text ?search= param.
func parseSearch(q url.Values) (string, error) {
	s := strings.TrimSpace(q.Get("search"))
	if len(s) > maxSearchLen {
		return "", fmt.Errorf("search term is too long (max %d characters)", maxSearchLen)
	}
	return s, nil
}

// parseFilter is parseWilayah plus the attribute filters and name search
// that only the Peta/Daftar menus have.
func parseFilter(q url.Values) (points.Filter, error) {
	filter, err := parseWilayah(q)
	if err != nil {
		return points.Filter{}, err
	}
	jenisPrelist, err := points.ParseJenisPrelist(q.Get("jenisPrelist"))
	if err != nil {
		return points.Filter{}, err
	}
	keberadaanKeluarga, err := points.ParseKeberadaanKeluarga(q.Get("keberadaanKeluarga"))
	if err != nil {
		return points.Filter{}, err
	}
	status, err := points.ParseStatus(q.Get("status"))
	if err != nil {
		return points.Filter{}, err
	}
	flagBaru, err := points.ParseFlag(q.Get("flagBaru"))
	if err != nil {
		return points.Filter{}, err
	}
	flagRegsosek, err := points.ParseFlag(q.Get("flagRegsosek"))
	if err != nil {
		return points.Filter{}, err
	}
	search, err := parseSearch(q)
	if err != nil {
		return points.Filter{}, err
	}
	filter.JenisPrelist = jenisPrelist
	filter.KeberadaanKeluarga = keberadaanKeluarga
	filter.Status = status
	filter.FlagBaru = flagBaru
	filter.FlagRegsosek = flagRegsosek
	filter.Search = search
	return filter, nil
}

func (s *Server) handleSubSLS(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	kabkota, err := points.ParseKabKota(q.Get("kabkota"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	kecamatan, err := points.ParseKecamatan(q.Get("kecamatan"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	desa, err := points.ParseDesa(q.Get("desa"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	sls, err := points.ParseSLS(q.Get("sls"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if kabkota == "" || kecamatan == "" || desa == "" || sls == "" {
		writeError(w, http.StatusBadRequest, "kabkota, kecamatan, desa and sls are required")
		return
	}

	list, err := s.svc.SubSLSList(r.Context(), kabkota, kecamatan, desa, sls)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		s.log.Error("subsls query failed", "err", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	out := make([]points.SubSLSInfoJSON, len(list))
	for i, sub := range list {
		out[i] = points.SubSLSInfoJSON{
			Code: sub.Code, Total: sub.Total,
			MinLat: sub.MinLat, MaxLat: sub.MaxLat, MinLon: sub.MinLon, MaxLon: sub.MaxLon,
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter, err := parseFilter(q)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	page := 1
	if v := q.Get("page"); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil || parsed < 1 {
			writeError(w, http.StatusBadRequest, "invalid page")
			return
		}
		page = parsed
	}

	pageSize := points.DefaultPageSize
	if v := q.Get("pageSize"); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil || parsed < 1 {
			writeError(w, http.StatusBadRequest, "invalid pageSize")
			return
		}
		pageSize = parsed
	}

	sortBy := points.ParseSortColumn(q.Get("sortBy"))
	dir := points.ParseSortDir(q.Get("dir"))

	resp, err := s.svc.List(r.Context(), filter, page, pageSize, sortBy, dir)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		s.log.Error("list query failed", "err", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

// reportData is what handleListPDF and handleListXLSX both need to render
// their respective format — gathered once by prepareReport so the two
// handlers can't drift out of sync on how a report's scope or row set is
// determined.
type reportData struct {
	region    report.Region
	items     []points.Point
	truncated bool
}

// prepareReport validates and parses the wilayah/attribute/sort filter
// from q, requires it to be pinned at least down to one desa/kelurahan
// (mirroring the download buttons' disabled state on the frontend: a
// report scoped any wider isn't what either button is for, and a whole
// kecamatan would run to hundreds of thousands of rows), and fetches every
// matching row (capped at points.ReportMaxRows). SLS and SubSLS stay
// optional — narrowing further just makes the report smaller. ok is false
// when it has already written an error response and the caller should
// return immediately.
func (s *Server) prepareReport(w http.ResponseWriter, r *http.Request, q url.Values, downloadKind string) (data reportData, ok bool) {
	filter, err := parseFilter(q)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return reportData{}, false
	}
	if filter.KabKota == "" || filter.Kecamatan == "" || filter.Desa == "" {
		writeError(w, http.StatusBadRequest, downloadKind+" download requires filtering at least down to Desa/Kelurahan")
		return reportData{}, false
	}

	sortBy := points.ParseSortColumn(q.Get("sortBy"))
	dir := points.ParseSortDir(q.Get("dir"))

	items, err := s.svc.ListAll(r.Context(), filter, sortBy, dir)
	if err != nil {
		if r.Context().Err() != nil {
			return reportData{}, false
		}
		s.log.Error("list-report query failed", "err", err, "format", downloadKind)
		writeError(w, http.StatusInternalServerError, "query failed")
		return reportData{}, false
	}

	region := report.Region{
		KabKotaCode: filter.KabKota,
		KabKotaName: points.KabKotaName(filter.KabKota),
		Kecamatan:   filter.Kecamatan,
		Desa:        filter.Desa,
		SLS:         filter.SLS,
		SubSLS:      filter.SubSLS,

		JenisPrelist:       filter.JenisPrelist,
		KeberadaanKeluarga: filter.KeberadaanKeluarga,
		Status:             filter.Status,
		Search:             filter.Search,
		FlagBaru:           filter.FlagBaru,
		FlagRegsosek:       filter.FlagRegsosek,
	}

	return reportData{region: region, items: items, truncated: len(items) >= points.ReportMaxRows}, true
}

// handleListPDF renders the "Daftar Hasil Pendataan" PDF for one
// fully-drilled-down SubSLS — see prepareReport for the shared scope
// rules.
func (s *Server) handleListPDF(w http.ResponseWriter, r *http.Request) {
	data, ok := s.prepareReport(w, r, r.URL.Query(), "PDF")
	if !ok {
		return
	}

	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="daftar-hasil-pendataan-%s.pdf"`, data.region.FullCode()))
	// The report reflects live data and every filter combination shares
	// this same URL shape, so never let a browser (or an intermediary
	// proxy) serve a stale cached copy back for a re-download.
	w.Header().Set("Cache-Control", "no-store")
	if err := pdfreport.Generate(w, data.region, data.items, data.truncated); err != nil {
		s.log.Error("pdf generation failed", "err", err)
		// Headers are already sent at this point, so we can't fall back to
		// writeError's JSON body — the client just gets a truncated/broken
		// download, which is at least visible rather than silently wrong.
	}
}

// handleListXLSX renders the same "Daftar Hasil Pendataan" report as
// handleListPDF, as an Excel workbook instead — same scope rules, same
// data, see prepareReport.
func (s *Server) handleListXLSX(w http.ResponseWriter, r *http.Request) {
	data, ok := s.prepareReport(w, r, r.URL.Query(), "Excel")
	if !ok {
		return
	}

	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="daftar-hasil-pendataan-%s.xlsx"`, data.region.FullCode()))
	w.Header().Set("Cache-Control", "no-store")
	if err := xlsxreport.Generate(w, data.region, data.items, data.truncated); err != nil {
		s.log.Error("xlsx generation failed", "err", err)
		// Same caveat as handleListPDF above: headers are already sent.
	}
}

// handleSubSLSPolygon serves the SubSLS boundary as GeoJSON for the Peta
// map to draw once a filter is pinned all the way down to one SubSLS —
// same all-five-levels-required rule as the PDF download, since a
// boundary polygon is only meaningful for one specific SubSLS.
func (s *Server) handleSubSLSPolygon(w http.ResponseWriter, r *http.Request) {
	if s.mapPool == nil {
		writeError(w, http.StatusServiceUnavailable, "SubSLS polygon layer is not configured on this server")
		return
	}

	q := r.URL.Query()
	filter, err := parseFilter(q)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if filter.KabKota == "" || filter.Kecamatan == "" || filter.Desa == "" || filter.SLS == "" || filter.SubSLS == "" {
		writeError(w, http.StatusBadRequest, "polygon lookup requires filtering all the way down to Kode SubSLS")
		return
	}

	geojson, err := mapdb.SubSLSPolygonGeoJSON(r.Context(), s.mapPool, filter.FullCode())
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		s.log.Error("subsls polygon query failed", "err", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	w.Header().Set("Content-Type", "application/geo+json")
	w.Write(geojson)
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := s.conn.Ping(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database unreachable")
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// handleReg2022FilterOptions serves the match_status dropdown choices for
// the Daftar Reg2022 menu — a fixed, closed set (see
// regsosek.GetFilterOptions), not a database query.
func (s *Server) handleReg2022FilterOptions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, regsosek.GetFilterOptions())
}

// handleReg2022 serves one page of the Daftar Reg2022 table. The wilayah
// filter is the same one the other menus use (se2026_match_regsosek shares
// level_6_full_code), so it reuses parseWilayah — and the frontend reuses
// the existing /api/kecamatan etc. cascade endpoints to populate those
// dropdowns, since every wilayah in this table also exists in the points
// table.
func (s *Server) handleReg2022(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	filter, _, err := regsosekFilter(q)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	page := 1
	if v := q.Get("page"); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil || parsed < 1 {
			writeError(w, http.StatusBadRequest, "invalid page")
			return
		}
		page = parsed
	}

	pageSize := points.DefaultPageSize
	if v := q.Get("pageSize"); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil || parsed < 1 {
			writeError(w, http.StatusBadRequest, "invalid pageSize")
			return
		}
		pageSize = parsed
	}

	resp, err := s.regsvc.List(
		r.Context(), filter, page, pageSize,
		regsosek.ParseSortColumn(q.Get("sortBy")), points.ParseSortDir(q.Get("dir")),
	)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		s.log.Error("reg2022 query failed", "err", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

// regsosekFilter parses everything the two match menus filter on: the
// shared wilayah levels, match_status, and the free-text search.
func regsosekFilter(q url.Values) (regsosek.Filter, points.Filter, error) {
	wilayah, err := parseWilayah(q)
	if err != nil {
		return regsosek.Filter{}, points.Filter{}, err
	}
	matchStatus, err := regsosek.ParseMatchStatus(q.Get("matchStatus"))
	if err != nil {
		return regsosek.Filter{}, points.Filter{}, err
	}
	search, err := parseSearch(q)
	if err != nil {
		return regsosek.Filter{}, points.Filter{}, err
	}
	return regsosek.Filter{
		WilayahPrefix: wilayah.FullCode(),
		MatchStatus:   matchStatus,
		Search:        search,
	}, wilayah, nil
}

// prepareRegsosekReport gathers the rows and scope description for the
// Daftar Match Regsosek downloads. Unlike the other report it only
// requires a kecamatan, not a desa/kelurahan: this table is far smaller
// (the largest kecamatan in it holds ~7.4k rows against ~27k for the
// largest desa in the points table), so a desa-level floor would be
// needlessly restrictive here. ok is false when an error response has
// already been written.
func (s *Server) prepareRegsosekReport(w http.ResponseWriter, r *http.Request, kind string) (report.Region, []regsosek.Row, bool, bool) {
	q := r.URL.Query()

	filter, wilayah, err := regsosekFilter(q)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return report.Region{}, nil, false, false
	}
	if wilayah.KabKota == "" || wilayah.Kecamatan == "" {
		writeError(w, http.StatusBadRequest, kind+" download requires filtering at least down to Kecamatan")
		return report.Region{}, nil, false, false
	}

	rows, err := s.regsvc.ListAll(
		r.Context(), filter,
		regsosek.ParseSortColumn(q.Get("sortBy")), points.ParseSortDir(q.Get("dir")),
	)
	if err != nil {
		if r.Context().Err() != nil {
			return report.Region{}, nil, false, false
		}
		s.log.Error("regsosek report query failed", "err", err, "format", kind)
		writeError(w, http.StatusInternalServerError, "query failed")
		return report.Region{}, nil, false, false
	}

	region := report.Region{
		KabKotaCode: wilayah.KabKota,
		KabKotaName: points.KabKotaName(wilayah.KabKota),
		Kecamatan:   wilayah.Kecamatan,
		Desa:        wilayah.Desa,
		SLS:         wilayah.SLS,
		SubSLS:      wilayah.SubSLS,

		// Reusing the shared "Filter Tambahan" block: match_status is this
		// menu's one attribute filter, so it goes in the Status slot rather
		// than growing Region a field only one report would use.
		Status: filter.MatchStatus,
		Search: filter.Search,
	}
	return region, rows, len(rows) >= regsosek.ReportMaxRows, true
}

func (s *Server) handleRegsosekPDF(w http.ResponseWriter, r *http.Request) {
	region, rows, truncated, ok := s.prepareRegsosekReport(w, r, "PDF")
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="daftar-match-regsosek-%s.pdf"`, region.FullCode()))
	w.Header().Set("Cache-Control", "no-store")
	if err := pdfreport.GenerateRegsosek(w, region, rows, truncated); err != nil {
		s.log.Error("regsosek pdf generation failed", "err", err)
	}
}

func (s *Server) handleRegsosekXLSX(w http.ResponseWriter, r *http.Request) {
	region, rows, truncated, ok := s.prepareRegsosekReport(w, r, "Excel")
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="daftar-match-regsosek-%s.xlsx"`, region.FullCode()))
	w.Header().Set("Cache-Control", "no-store")
	if err := xlsxreport.GenerateRegsosek(w, region, rows, truncated); err != nil {
		s.log.Error("regsosek xlsx generation failed", "err", err)
	}
}

// handleMatchPoints serves the Peta Match Reg2022 viewport: the same rows
// as the Daftar Match Regsosek table, plotted on their Regsosek
// coordinates, clustered by the same rules /api/points uses.
func (s *Server) handleMatchPoints(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	bbox, err := points.ParseBBox(q.Get("minLat"), q.Get("maxLat"), q.Get("minLon"), q.Get("maxLon"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	zoom := 0
	if z := q.Get("zoom"); z != "" {
		parsed, err := strconv.Atoi(z)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid zoom")
			return
		}
		zoom = parsed
	}
	filter, _, err := regsosekFilter(q)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	resp, err := s.regsvc.Query(r.Context(), bbox, zoom, filter)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		s.log.Error("match points query failed", "err", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleMatchBounds gives the match map an extent to fit to for whatever
// filter is applied — the equivalent of /api/bounds, but computed per
// request since it depends on the filter rather than being a fixed
// dataset-wide value.
func (s *Server) handleMatchBounds(w http.ResponseWriter, r *http.Request) {
	filter, _, err := regsosekFilter(r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	b, err := s.regsvc.BoundsFor(r.Context(), filter)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		s.log.Error("match bounds query failed", "err", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	writeJSON(w, http.StatusOK, b)
}
