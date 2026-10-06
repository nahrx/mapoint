package api

import (
	"archive/zip"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"se2026-titik-maps/internal/pdfreport"
	"se2026-titik-maps/internal/points"
	"se2026-titik-maps/internal/report"
	"se2026-titik-maps/internal/xlsxreport"
)

// The split download modes of the Daftar menu: instead of one PDF/Excel
// for the whole filter, one file per SLS or per SubSLS, delivered as a
// single ZIP so a click yields a single download whatever the count
// (browsers block a page that fires hundreds of downloads at once, and
// the largest kecamatan has 593 SubSLS in 412 SLS).
//
// It is switched on with split=sls or split=subsls on the same two
// endpoints, so every other parameter — filters, sort, the scope rules in
// parseReportScope — keeps its meaning; only the response shape changes.
// Each file covers one wilayah at the chosen level, so the size of any
// single document barely depends on how wide the filter is; what grows is
// the number of files, and that is what the ZIP is for.
//
// Which level to pick is a real choice, not a detail. Measured over the
// live batch: a desa holds 766 rows at the median and 29,439 at the
// largest, an SLS 108 and 2,604; 1,586 of 15,303 SLS are split into
// several SubSLS (one into 35). So per-Desa gives few, fat files — a
// kecamatan has 8 desa at the median, 26 at most — while per-SubSLS gives
// many thin ones, and per-SLS sits between them.

// splitParam is the query parameter that selects split mode. Anything
// outside the values below is a 400 rather than silently ignored, so a
// typo doesn't quietly hand back the single report.
const splitParam = "split"

// splitLevel is the wilayah level each file of a split download covers.
type splitLevel string

const (
	splitOff    splitLevel = ""
	splitDesa   splitLevel = "desa"
	splitSLS    splitLevel = "sls"
	splitSubSLS splitLevel = "subsls"
)

// splitLevels lists the real levels, coarsest first — the order the
// dialog offers them in, and the list the error message quotes.
var splitLevels = []splitLevel{splitDesa, splitSLS, splitSubSLS}

// on reports whether this is a split download at all.
func (l splitLevel) on() bool { return l != splitOff }

// codeLen is how many digits of level_6_full_code identify one file's
// wilayah. level_6_full_code is 4+3+3+4+2: kabkota, kecamatan, desa, SLS,
// SubSLS.
func (l splitLevel) codeLen() int {
	switch l {
	case splitDesa:
		return 10
	case splitSLS:
		return 14
	default:
		return 16
	}
}

// label is the level's name for messages, slug its filename form.
func (l splitLevel) label() string {
	switch l {
	case splitDesa:
		return "Desa/Kelurahan"
	case splitSLS:
		return "SLS"
	default:
		return "SubSLS"
	}
}

func (l splitLevel) slug() string {
	switch l {
	case splitDesa:
		return "per-desa"
	case splitSLS:
		return "per-sls"
	default:
		return "per-subsls"
	}
}

// parseSplit reads the split parameter: off, one of splitLevels, or an
// error.
func parseSplit(q url.Values) (splitLevel, error) {
	v := splitLevel(q.Get(splitParam))
	if v == splitOff {
		return splitOff, nil
	}
	if slices.Contains(splitLevels, v) {
		return v, nil
	}
	names := make([]string, len(splitLevels))
	for i, l := range splitLevels {
		names[i] = strconv.Quote(string(l))
	}
	return splitOff, fmt.Errorf("unknown %s value %q (only %s are supported)", splitParam, string(v), strings.Join(names, ", "))
}

// splitFormat is the per-format part of a split download: the two
// handlers differ only in what they call to render one SubSLS.
type splitFormat struct {
	kind     string // "PDF" / "Excel", for messages and logs
	ext      string
	generate func(w io.Writer, region report.Region, total int, rows iter.Seq[points.Point], truncated bool) error
}

var (
	splitPDF  = splitFormat{kind: "PDF", ext: "pdf", generate: pdfreport.Generate}
	splitXLSX = splitFormat{kind: "Excel", ext: "xlsx", generate: xlsxreport.Generate}
)

// codeGroup is the rows of one wilayah at the split level, in the
// caller's sort order.
type codeGroup struct {
	code  string // the first codeLen digits of level_6_full_code
	items []points.Point
}

// groupByCode partitions items by the first codeLen digits of their
// SubSLS code, groups in ascending code order and rows within a group in
// their incoming order — so the sort the user picked still applies inside
// each file. Every row's code is 16 digits in the live table (checked:
// all 2.2M), but a shorter one is kept as its own group rather than
// dropped, so a data slip shows up as an odd file instead of missing rows.
func groupByCode(items []points.Point, codeLen int) []codeGroup {
	byCode := make(map[string][]points.Point)
	for _, p := range items {
		code := p.SubSLS
		if len(code) > codeLen {
			code = code[:codeLen]
		}
		byCode[code] = append(byCode[code], p)
	}
	groups := make([]codeGroup, 0, len(byCode))
	for code, rows := range byCode {
		groups = append(groups, codeGroup{code: code, items: rows})
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].code < groups[j].code })
	return groups
}

// regionForCode narrows the request's region to one file's wilayah, so
// each file carries its own "Keterangan Wilayah" header. Only a 16-digit
// code pins a SubSLS, which also drops the per-row ID SUBSLS column from
// the PDF (see report.Region.PinnedToSubSLS); a shorter one leaves the
// levels below it empty, so the file keeps that column — which it needs,
// because a desa or an SLS file can hold many SubSLS. The parts come from
// the code itself because the request may only have named a kecamatan, or
// just the kabupaten/kota.
func regionForCode(base report.Region, code string, names *wilayahNameIndex) report.Region {
	r := base
	if len(code) < 10 {
		return r
	}
	r.Kecamatan = code[4:7]
	r.Desa = code[7:10]
	r.SLS, r.SubSLS = "", ""
	if len(code) >= 14 {
		r.SLS = code[10:14]
	}
	if len(code) >= 16 {
		r.SubSLS = code[14:16]
	}
	// The narrowed region reaches levels the request never named, so its
	// names have to be looked up again rather than inherited.
	r.KecamatanName, r.DesaName, r.SLSName = "", "", ""
	names.apply(&r)
	return r
}

// handleSplitReport serves the ZIP of per-SLS or per-SubSLS reports for
// one format. Rows come from a reportPlan (one query down to kecamatan
// width, one per kecamatan for a whole kabupaten/kota — see
// report_plan.go), are grouped at the chosen level one unit at a time, and
// each group is rendered straight into the ZIP stream: the client sees
// bytes as soon as the first file is done, nothing is buffered twice, and
// memory stays at one kecamatan. Entries are stored, not deflated: PDF and
// XLSX are already compressed, and deflating them again costs CPU for a
// few percent.
func (s *Server) handleSplitReport(w http.ResponseWriter, r *http.Request, q url.Values, f splitFormat, level splitLevel) {
	plan, ok := s.planReport(w, r, q, f.kind, true)
	if !ok {
		return
	}

	s.extendWriteDeadline(w, plan)
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="daftar-hasil-pendataan-%s-%s.zip"`, plan.region.FullCode(), level.slug()))
	w.Header().Set("Cache-Control", "no-store")

	zw := zip.NewWriter(w)
	now := time.Now()
	ctx := r.Context()
	var truncated []string // units (kabkota+kecamatan) that hit the row cap
	for i, unit := range plan.units {
		items := plan.first
		if i > 0 {
			// A closed connection is the only way out early; the rest of
			// the ZIP would just be written into the void.
			if ctx.Err() != nil {
				return
			}
			var err error
			items, err = s.svc.ListAllUpTo(ctx, unit, plan.scope.sortBy, plan.scope.dir, plan.scope.maxRows)
			if err != nil {
				s.log.Error("split report: query failed mid-stream", "err", err, "format", f.kind, "kecamatan", unit.KabKota+unit.Kecamatan)
				return
			}
		}
		if len(items) >= plan.scope.maxRows {
			truncated = append(truncated, unit.KabKota+unit.Kecamatan)
		}
		for _, g := range groupByCode(items, level.codeLen()) {
			if ctx.Err() != nil {
				return
			}
			fw, err := zw.CreateHeader(&zip.FileHeader{
				Name:     fmt.Sprintf("daftar-hasil-pendataan-%s.%s", g.code, f.ext),
				Method:   zip.Store,
				Modified: now,
			})
			if err != nil {
				s.log.Error("split report: zip entry failed", "err", err, "format", f.kind, "code", g.code)
				return
			}
			if err := f.generate(fw, regionForCode(plan.region, g.code, plan.names), len(g.items), slices.Values(g.items), false); err != nil {
				// Same caveat as the single-file handler: headers are
				// already sent, so the client gets a short archive rather
				// than an error body. Logged with the code so it can be
				// reproduced alone.
				s.log.Error("split report: generation failed", "err", err, "format", f.kind, "code", g.code)
				return
			}
		}
		plan.first = nil
	}
	if len(truncated) > 0 {
		// The row cap was hit, so the highest-coded SubSLS of that unit may
		// be missing or partial. Say so inside the archive — the only place
		// the user will look — instead of in a header nobody reads on a
		// download.
		if fw, err := zw.CreateHeader(&zip.FileHeader{Name: "CATATAN.txt", Method: zip.Deflate, Modified: now}); err == nil {
			fmt.Fprintf(fw, "Daftar untuk wilayah %s dibatasi hingga %d baris pertama; %s dengan kode terbesar di wilayah itu mungkin tidak lengkap atau tidak ikut. Persempit filternya (misalnya per desa/kelurahan) lalu unduh lagi.\r\n", strings.Join(truncated, ", "), points.SplitReportMaxRows, level.label())
		}
	}
	if err := zw.Close(); err != nil {
		s.log.Error("split report: zip close failed", "err", err, "format", f.kind)
	}
}
