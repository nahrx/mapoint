package api

import (
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"se2026-titik-maps/internal/pdfreport"
	"se2026-titik-maps/internal/points"
	"se2026-titik-maps/internal/report"
	"se2026-titik-maps/internal/xlsxreport"
)

// The "Pisahkan per SubSLS" download mode of the Daftar menu: instead of
// one PDF/Excel for the whole filter, one file per SubSLS, delivered as a
// single ZIP so a click yields a single download whatever the count
// (browsers block a page that fires hundreds of downloads at once, and
// the largest kecamatan has 592 SubSLS).
//
// It is switched on with split=subsls on the same two endpoints, so every
// other parameter — filters, sort — keeps its meaning; only the scope rule
// and the response shape change. The scope may be as wide as one kecamatan
// here (the ordinary report stops at desa/kelurahan): each file is still
// one SubSLS, so the size of any single document is unchanged — what grows
// is the number of files, and that is what the ZIP is for.

// splitParam is the query parameter that selects split mode, and
// splitSubSLS its only accepted value. Anything else is a 400 rather than
// silently ignored, so a typo doesn't quietly hand back the single report.
const (
	splitParam  = "split"
	splitSubSLS = "subsls"
)

// parseSplit reads the split parameter: off, per-SubSLS, or an error.
func parseSplit(q url.Values) (bool, error) {
	switch v := q.Get(splitParam); v {
	case "":
		return false, nil
	case splitSubSLS:
		return true, nil
	default:
		return false, fmt.Errorf("unknown %s value %q (only %q is supported)", splitParam, v, splitSubSLS)
	}
}

// splitFormat is the per-format part of a split download: the two
// handlers differ only in what they call to render one SubSLS.
type splitFormat struct {
	kind     string // "PDF" / "Excel", for messages and logs
	ext      string
	generate func(w io.Writer, region report.Region, items []points.Point, truncated bool) error
}

var (
	splitPDF  = splitFormat{kind: "PDF", ext: "pdf", generate: pdfreport.Generate}
	splitXLSX = splitFormat{kind: "Excel", ext: "xlsx", generate: xlsxreport.Generate}
)

// subslsGroup is the rows of one SubSLS, in the caller's sort order.
type subslsGroup struct {
	code  string // level_6_full_code, 16 digits
	items []points.Point
}

// groupBySubSLS partitions items by their SubSLS code, groups in ascending
// code order and rows within a group in their incoming order — so the sort
// the user picked still applies inside each file. Every row's code is 16
// digits in the live table (checked: all 2,195,282), but a shorter one is
// kept as its own group rather than dropped, so a data slip shows up as an
// odd file instead of missing rows.
func groupBySubSLS(items []points.Point) []subslsGroup {
	byCode := make(map[string][]points.Point)
	for _, p := range items {
		byCode[p.SubSLS] = append(byCode[p.SubSLS], p)
	}
	groups := make([]subslsGroup, 0, len(byCode))
	for code, rows := range byCode {
		groups = append(groups, subslsGroup{code: code, items: rows})
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].code < groups[j].code })
	return groups
}

// regionForSubSLS narrows the request's region to one SubSLS, so each file
// gets the pinned-to-SubSLS header and column set the ordinary single-SubSLS
// report has (see report.Region.PinnedToSubSLS). The kecamatan/desa/SLS/
// SubSLS parts come from the code itself (4+3+3+4+2 digits) because the
// request may only have named a kecamatan, or just the kabupaten/kota.
func regionForSubSLS(base report.Region, code string) report.Region {
	r := base
	if len(code) == 16 {
		r.Kecamatan = code[4:7]
		r.Desa = code[7:10]
		r.SLS = code[10:14]
		r.SubSLS = code[14:16]
	}
	return r
}

// splitWriteTimeout replaces the server's 30-second WriteTimeout for a
// split download. That global limit is right for API calls, but the ZIP
// is generated while it streams: the largest kecamatan takes ~11 s and a
// whole kabupaten/kota (Samarinda: 2,988 SubSLS, 552k rows) runs to about
// a minute, and a deadline that fires mid-archive hands the user a broken
// ZIP with no error.
const splitWriteTimeout = 15 * time.Minute

// handleSplitReport serves the ZIP of per-SubSLS reports for one format.
// Rows are fetched per unit of work — the whole scope for anything down
// to kecamatan width, one kecamatan at a time for a whole kabupaten/kota
// — grouped in memory, and each group is rendered straight into the ZIP
// stream. So the client sees bytes as soon as the first file is done
// rather than after the last, nothing is buffered twice, and the memory
// high-water mark stays at one kecamatan (≤ points.SplitReportMaxRows)
// even for a 550k-row kabupaten/kota. Entries are stored, not deflated:
// PDF and XLSX are already compressed, and deflating them again costs CPU
// for a few percent.
func (s *Server) handleSplitReport(w http.ResponseWriter, r *http.Request, q url.Values, f splitFormat) {
	scope, ok := s.parseReportScope(w, q, f.kind, true)
	if !ok {
		return
	}

	// Units of work: the scope itself, or one filter per kecamatan of the
	// kabupaten/kota. The kecamatan list is a cheap GROUP BY and comes
	// before any header is written, so a failure here is still a proper
	// error response.
	units := []points.Filter{scope.filter}
	if scope.filter.Kecamatan == "" {
		kecs, err := s.svc.KecamatanList(r.Context(), scope.filter.KabKota)
		if err != nil {
			if r.Context().Err() != nil {
				return
			}
			s.log.Error("split report: kecamatan list failed", "err", err, "kabkota", scope.filter.KabKota)
			writeError(w, http.StatusInternalServerError, "query failed")
			return
		}
		// An empty list (a kabupaten/kota with no rows at all) keeps the
		// scope itself as the one unit: its query returns nothing and the
		// archive comes out empty, rather than indexing into no units.
		if len(kecs) > 0 {
			units = units[:0]
		}
		for _, k := range kecs {
			u := scope.filter
			u.Kecamatan = k.Code
			units = append(units, u)
		}
	}

	// The first unit is fetched before the headers go out, so an empty or
	// failing query still gets a real status code; later units fail into
	// the stream (logged, short archive) like a generation error would.
	first, err := s.svc.ListAllUpTo(r.Context(), units[0], scope.sortBy, scope.dir, scope.maxRows)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		s.log.Error("list-report query failed", "err", err, "format", f.kind)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(splitWriteTimeout)); err != nil {
		// Not fatal: the download still works for anything that fits in the
		// global 30 s. Logged so a truncated kabupaten/kota ZIP has a trail.
		s.log.Warn("split report: could not extend write deadline", "err", err)
	}
	region := regionFor(scope.filter)
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="daftar-hasil-pendataan-%s-per-subsls.zip"`, region.FullCode()))
	w.Header().Set("Cache-Control", "no-store")

	zw := zip.NewWriter(w)
	now := time.Now()
	var truncated []string // kecamatan codes (or the scope's own) that hit the cap
	for i, unit := range units {
		items := first
		if i > 0 {
			// A closed connection is the only way out early; the rest of
			// the ZIP would just be written into the void.
			if r.Context().Err() != nil {
				return
			}
			items, err = s.svc.ListAllUpTo(r.Context(), unit, scope.sortBy, scope.dir, scope.maxRows)
			if err != nil {
				s.log.Error("split report: query failed mid-stream", "err", err, "format", f.kind, "kecamatan", unit.KabKota+unit.Kecamatan)
				return
			}
		}
		if len(items) >= scope.maxRows {
			truncated = append(truncated, unit.KabKota+unit.Kecamatan)
		}
		for _, g := range groupBySubSLS(items) {
			if r.Context().Err() != nil {
				return
			}
			fw, err := zw.CreateHeader(&zip.FileHeader{
				Name:     fmt.Sprintf("daftar-hasil-pendataan-%s.%s", g.code, f.ext),
				Method:   zip.Store,
				Modified: now,
			})
			if err != nil {
				s.log.Error("split report: zip entry failed", "err", err, "format", f.kind, "subsls", g.code)
				return
			}
			if err := f.generate(fw, regionForSubSLS(region, g.code), g.items, false); err != nil {
				// Same caveat as the single-file handlers: headers are
				// already sent, so the client gets a short archive rather
				// than an error body. Logged with the SubSLS so it can be
				// reproduced alone.
				s.log.Error("split report: generation failed", "err", err, "format", f.kind, "subsls", g.code)
				return
			}
		}
		first = nil
	}
	if len(truncated) > 0 {
		// The row cap was hit, so the highest-coded SubSLS of that unit may
		// be missing or partial. Say so inside the archive — the only place
		// the user will look — instead of in a header nobody reads on a
		// download.
		if fw, err := zw.CreateHeader(&zip.FileHeader{Name: "CATATAN.txt", Method: zip.Deflate, Modified: now}); err == nil {
			fmt.Fprintf(fw, "Daftar untuk wilayah %s dibatasi hingga %d baris pertama; SubSLS dengan kode terbesar di wilayah itu mungkin tidak lengkap atau tidak ikut. Persempit filternya (misalnya per desa/kelurahan) lalu unduh lagi.\r\n", strings.Join(truncated, ", "), points.SplitReportMaxRows)
		}
	}
	if err := zw.Close(); err != nil {
		s.log.Error("split report: zip close failed", "err", err, "format", f.kind)
	}
}
