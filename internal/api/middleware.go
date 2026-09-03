package api

import (
	"compress/gzip"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// withLogging logs every request's method, path, status and duration.
func withLogging(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"query", r.URL.RawQuery,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}

// compressibleTypes are the response content types worth gzipping. The
// downloads are deliberately absent: an .xlsx is already a zip and a PDF's
// streams are already deflated, so compressing them again burns CPU for
// roughly nothing.
var compressibleTypes = []string{
	"application/json",
	"application/geo+json",
	"application/javascript",
	"text/",
}

func compressible(contentType string) bool {
	for _, t := range compressibleTypes {
		if strings.HasPrefix(contentType, t) {
			return true
		}
	}
	return false
}

// gzipPool reuses gzip.Writers across requests — each one carries a ~64KB
// internal window, which is worth not reallocating on every map pan.
var gzipPool = sync.Pool{
	New: func() any { return gzip.NewWriter(io.Discard) },
}

// gzipResponseWriter defers the compress-or-not decision until the handler
// has set its Content-Type (i.e. until WriteHeader, explicit or implied by
// the first Write), because that's what the decision is based on.
type gzipResponseWriter struct {
	http.ResponseWriter
	gz          *gzip.Writer
	wroteHeader bool
}

func (w *gzipResponseWriter) WriteHeader(code int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true

	// 204/304 carry no body, and a Content-Encoding on them is meaningless.
	if code != http.StatusNoContent && code != http.StatusNotModified &&
		compressible(w.Header().Get("Content-Type")) {
		w.gz = gzipPool.Get().(*gzip.Writer)
		w.gz.Reset(w.ResponseWriter)
		w.Header().Set("Content-Encoding", "gzip")
		// Whatever length the handler computed describes the uncompressed
		// body, so it would be wrong on the wire; drop it and let the
		// response be chunked.
		w.Header().Del("Content-Length")
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *gzipResponseWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		// net/http would sniff the type here if the handler didn't set one;
		// do it first ourselves so the decision above sees the same value.
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", http.DetectContentType(b))
		}
		w.WriteHeader(http.StatusOK)
	}
	if w.gz != nil {
		return w.gz.Write(b)
	}
	return w.ResponseWriter.Write(b)
}

// close flushes and returns the gzip.Writer to the pool. Safe to call when
// nothing was compressed.
func (w *gzipResponseWriter) close() {
	if w.gz == nil {
		return
	}
	w.gz.Close()
	gzipPool.Put(w.gz)
	w.gz = nil
}

// withGzip compresses responses for clients that advertise gzip support.
// Measured on a real /api/points response (942 individual points): 285KB
// down to 51KB, ~5.6x — the biggest single win for field staff loading the
// map over mobile data.
func withGzip(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}
		// Caches must key on the encoding, or a gzipped body can be handed
		// to a client that never asked for one.
		w.Header().Add("Vary", "Accept-Encoding")

		gw := &gzipResponseWriter{ResponseWriter: w}
		defer gw.close()
		next.ServeHTTP(gw, r)
	})
}
