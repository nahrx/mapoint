// Package api wires up the HTTP handlers: static frontend, /api/points,
// /api/bounds, /api/kabkota, /api/kecamatan, /api/desa, /api/sls,
// /api/subsls, /api/list, /api/list/pdf and /healthz.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"se2026-titik-maps/internal/pdfreport"
	"se2026-titik-maps/internal/points"
)

type Server struct {
	svc     *points.Service
	conn    driver.Conn
	bounds  atomic.Pointer[points.Bounds]
	kabkota atomic.Pointer[[]points.KabKotaInfo]
	log     *slog.Logger
}

func NewServer(svc *points.Service, conn driver.Conn, bounds points.Bounds, kabkota []points.KabKotaInfo, log *slog.Logger) *Server {
	s := &Server{svc: svc, conn: conn, log: log}
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
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.Handle("/", http.FileServer(staticFS))

	return withLogging(s.log, mux)
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

	out := make([]points.KecamatanInfoJSON, len(list))
	for i, k := range list {
		out[i] = points.KecamatanInfoJSON{
			Code: k.Code, Total: k.Total,
			MinLat: k.MinLat, MaxLat: k.MaxLat, MinLon: k.MinLon, MaxLon: k.MaxLon,
		}
	}
	writeJSON(w, http.StatusOK, out)
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

	out := make([]points.DesaInfoJSON, len(list))
	for i, d := range list {
		out[i] = points.DesaInfoJSON{
			Code: d.Code, Total: d.Total,
			MinLat: d.MinLat, MaxLat: d.MaxLat, MinLon: d.MinLon, MaxLon: d.MaxLon,
		}
	}
	writeJSON(w, http.StatusOK, out)
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

	out := make([]points.SLSInfoJSON, len(list))
	for i, sl := range list {
		out[i] = points.SLSInfoJSON{
			Code: sl.Code, Total: sl.Total,
			MinLat: sl.MinLat, MaxLat: sl.MaxLat, MinLon: sl.MinLon, MaxLon: sl.MaxLon,
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// parseFilter reads the kabkota/kecamatan/desa/sls/subsls wilayah filter
// shared by /api/points and /api/list, and checks that each level is only
// set when its parent is too.
func parseFilter(q url.Values) (points.Filter, error) {
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
	filter := points.Filter{KabKota: kabkota, Kecamatan: kecamatan, Desa: desa, SLS: sls, SubSLS: subsls}
	if err := filter.Validate(); err != nil {
		return points.Filter{}, err
	}
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

	dir := points.ParseSortDir(q.Get("dir"))

	resp, err := s.svc.List(r.Context(), filter, page, pageSize, dir)
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

// handleListPDF renders the "Daftar Hasil Pendataan" PDF for one
// fully-drilled-down SubSLS. It deliberately requires every wilayah level
// (kabkota through subsls) — mirroring the button's disabled state on the
// frontend — since a report scoped any wider than a single SubSLS isn't
// what this button is for, and could otherwise return an unbounded
// number of rows.
func (s *Server) handleListPDF(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter, err := parseFilter(q)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if filter.KabKota == "" || filter.Kecamatan == "" || filter.Desa == "" || filter.SLS == "" || filter.SubSLS == "" {
		writeError(w, http.StatusBadRequest, "PDF download requires filtering all the way down to Kode SubSLS")
		return
	}

	dir := points.ParseSortDir(q.Get("dir"))

	items, err := s.svc.ListAll(r.Context(), filter, dir)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		s.log.Error("list-pdf query failed", "err", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	region := pdfreport.Region{
		KabKotaCode: filter.KabKota,
		KabKotaName: points.KabKotaName(filter.KabKota),
		Kecamatan:   filter.Kecamatan,
		Desa:        filter.Desa,
		SLS:         filter.SLS,
		SubSLS:      filter.SubSLS,
	}
	truncated := len(items) >= points.ReportMaxRows

	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="daftar-hasil-pendataan-%s.pdf"`, region.FullCode()))
	// The report reflects live data and every filter combination shares
	// this same URL shape, so never let a browser (or an intermediary
	// proxy) serve a stale cached copy back for a re-download.
	w.Header().Set("Cache-Control", "no-store")
	if err := pdfreport.Generate(w, region, items, truncated); err != nil {
		s.log.Error("pdf generation failed", "err", err)
		// Headers are already sent at this point, so we can't fall back to
		// writeError's JSON body — the client just gets a truncated/broken
		// download, which is at least visible rather than silently wrong.
	}
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
