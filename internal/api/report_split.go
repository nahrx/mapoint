package api

import (
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
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
// report has (see report.Region.PinnedToSubSLS). The desa/SLS/SubSLS parts
// come from the code itself (4+3+3+4+2 digits) because the request may
// only have named a kecamatan.
func regionForSubSLS(base report.Region, code string) report.Region {
	r := base
	if len(code) == 16 {
		r.Desa = code[7:10]
		r.SLS = code[10:14]
		r.SubSLS = code[14:16]
	}
	return r
}

// handleSplitReport serves the ZIP of per-SubSLS reports for one format.
// Rows are fetched once for the whole scope (points.SplitReportMaxRows),
// grouped in memory, and each group is rendered straight into the ZIP
// stream — so the client sees bytes as soon as the first file is done
// rather than after the last, and nothing is buffered twice. Entries are
// stored, not deflated: PDF and XLSX are already compressed, and
// deflating them again costs CPU for a few percent.
func (s *Server) handleSplitReport(w http.ResponseWriter, r *http.Request, q url.Values, f splitFormat) {
	data, ok := s.prepareReport(w, r, q, f.kind, true)
	if !ok {
		return
	}
	groups := groupBySubSLS(data.items)

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="daftar-hasil-pendataan-%s-per-subsls.zip"`, data.region.FullCode()))
	w.Header().Set("Cache-Control", "no-store")

	zw := zip.NewWriter(w)
	now := time.Now()
	for _, g := range groups {
		// A closed connection is the only way out of this loop early; the
		// rest of the ZIP would just be written into the void.
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
		if err := f.generate(fw, regionForSubSLS(data.region, g.code), g.items, false); err != nil {
			// Same caveat as the single-file handlers: headers are already
			// sent, so the client gets a short archive rather than an error
			// body. Logged with the SubSLS so it can be reproduced alone.
			s.log.Error("split report: generation failed", "err", err, "format", f.kind, "subsls", g.code)
			return
		}
	}
	if data.truncated {
		// The row cap was hit, so the highest-coded SubSLS may be missing or
		// partial. Say so inside the archive — the only place the user will
		// look — instead of in a header nobody reads on a download.
		if fw, err := zw.CreateHeader(&zip.FileHeader{Name: "CATATAN.txt", Method: zip.Deflate, Modified: now}); err == nil {
			fmt.Fprintf(fw, "Daftar ini dibatasi hingga %d baris pertama; SubSLS dengan kode terbesar mungkin tidak lengkap atau tidak ikut. Persempit filternya (misalnya per desa/kelurahan) lalu unduh lagi.\r\n", points.SplitReportMaxRows)
		}
	}
	if err := zw.Close(); err != nil {
		s.log.Error("split report: zip close failed", "err", err, "format", f.kind)
	}
}
