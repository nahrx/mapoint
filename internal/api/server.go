// Package api wires up the HTTP handlers: static frontend, /api/points,
// /api/bounds, /api/kabkota, /api/kecamatan, /api/desa, /api/sls and /healthz.
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

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
	filter := points.Filter{KabKota: kabkota, Kecamatan: kecamatan, Desa: desa, SLS: sls}
	if err := filter.Validate(); err != nil {
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
