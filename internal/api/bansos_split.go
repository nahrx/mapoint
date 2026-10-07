package api

import (
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"

	"se2026-titik-maps/internal/bansos"
	"se2026-titik-maps/internal/pdfreport"
	"se2026-titik-maps/internal/points"
	"se2026-titik-maps/internal/report"
	"se2026-titik-maps/internal/xlsxreport"
)

// The split downloads of the Bansos menu — one file per desa/SLS/SubSLS in
// a ZIP, same idea and same `split=` values as the Daftar menu (see
// report_split.go, whose splitLevel and parseSplit this reuses).
//
// What differs is that not every row can be placed at every level, because
// this table locates rows two different ways (see bansos.Filter.clause):
//
//   - per desa: a row uses the first 10 digits of `subsls`, or
//     `dtsen_kode_desa` when it has no subsls. 1,584 of 16,086 rows have
//     neither and cannot be placed at all.
//   - per SLS / per SubSLS: only `subsls` can answer, so the 3,322 rows
//     without one cannot be placed either.
//
// Those rows are **not** dropped. They go into one extra file at the end of
// the ZIP, and a CATATAN.txt says how many and why. A download that
// silently omits a fifth of the table would be worse than one with an
// awkward extra file — whoever opens it has to be able to see that the
// parts add up to the whole.
const bansosUnplacedName = "tanpa-kode-wilayah"

// bansosGroupCode is the wilayah code the row's file is named after, or ""
// when the row can't be placed at this level.
func bansosGroupCode(r bansos.Row, level splitLevel) string {
	n := level.codeLen()
	if len(r.SubSLS) >= n {
		return r.SubSLS[:n]
	}
	// dtsen_kode_desa is exactly desa-deep, so it can only stand in for the
	// desa level.
	if level == splitDesa && r.DtsenKodeDesa != "" {
		return r.DtsenKodeDesa
	}
	return ""
}

// bansosGroup is the rows of one file, in the caller's sort order.
type bansosGroup struct {
	code  string // "" for the unplaceable rows, which sort last
	items []bansos.Row
}

func groupBansos(rows []bansos.Row, level splitLevel) []bansosGroup {
	byCode := make(map[string][]bansos.Row)
	for _, r := range rows {
		code := bansosGroupCode(r, level)
		byCode[code] = append(byCode[code], r)
	}
	groups := make([]bansosGroup, 0, len(byCode))
	for code, items := range byCode {
		groups = append(groups, bansosGroup{code: code, items: items})
	}
	sort.Slice(groups, func(i, j int) bool {
		// The unplaceable group goes last, where it reads as a remainder
		// rather than as the first thing in the archive.
		if (groups[i].code == "") != (groups[j].code == "") {
			return groups[j].code == ""
		}
		return groups[i].code < groups[j].code
	})
	return groups
}

// bansosSplitFormat is the per-format part of a split download.
type bansosSplitFormat struct {
	kind     string
	ext      string
	generate func(w io.Writer, region report.Region, rows []bansos.Row, truncated bool) error
}

var (
	bansosSplitPDF = bansosSplitFormat{kind: "PDF", ext: "pdf",
		generate: func(w io.Writer, region report.Region, rows []bansos.Row, truncated bool) error {
			return pdfreport.GenerateBansos(w, region, rows, truncated)
		}}
	bansosSplitXLSX = bansosSplitFormat{kind: "Excel", ext: "xlsx", generate: xlsxreport.GenerateBansos}
)

// handleBansosSplit serves the ZIP of per-wilayah reports. The whole table
// is 16k rows, so unlike the Daftar split this one fetches everything once
// and needs neither the per-kecamatan streaming nor the extended write
// deadline: the largest possible job here is 996 small files.
func (s *Server) handleBansosSplit(w http.ResponseWriter, r *http.Request, f bansosSplitFormat, level splitLevel) {
	region, rows, truncated, ok := s.bansosReport(w, r)
	if !ok {
		return
	}

	// The whole table split per SubSLS is 6,377 files / 47 MB and took 17s
	// measured here — inside the server's 30s WriteTimeout, but not by
	// enough to trust on a slower box or a slow client. Same treatment as
	// the Daftar split: lift the deadline rather than risk handing out a
	// truncated ZIP with no error (reportWriteTimeout, report_plan.go).
	if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(reportWriteTimeout)); err != nil {
		s.log.Warn("bansos split: could not extend write deadline", "err", err)
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="daftar-bansos-%s-%s.zip"`, bansosFilename(region), level.slug()))
	w.Header().Set("Cache-Control", "no-store")

	zw := zip.NewWriter(w)
	now := time.Now()
	unplaced := 0
	for _, g := range groupBansos(rows, level) {
		if r.Context().Err() != nil {
			return
		}
		name := g.code
		if name == "" {
			name = bansosUnplacedName
			unplaced = len(g.items)
		}
		fw, err := zw.CreateHeader(&zip.FileHeader{
			Name:     fmt.Sprintf("daftar-bansos-%s.%s", name, f.ext),
			Method:   zip.Store,
			Modified: now,
		})
		if err != nil {
			s.log.Error("bansos split: zip entry failed", "err", err, "format", f.kind, "code", g.code)
			return
		}
		if err := f.generate(fw, bansosRegionForCode(region, g.code, s.wilayahNameIndexFor(r.Context())), g.items, truncated); err != nil {
			// Headers are already sent, so the client gets a short archive
			// rather than an error body.
			s.log.Error("bansos split: generation failed", "err", err, "format", f.kind, "code", g.code)
			return
		}
	}
	if unplaced > 0 {
		if fw, err := zw.CreateHeader(&zip.FileHeader{Name: "CATATAN.txt", Method: zip.Deflate, Modified: now}); err == nil {
			fmt.Fprintf(fw, "%d baris tidak punya kode wilayah yang cukup untuk dipisah per %s, jadi dikumpulkan di file daftar-bansos-%s.%s. Baris itu tetap ikut — tidak ada baris yang hilang dari ZIP ini.\r\n",
				unplaced, level.label(), bansosUnplacedName, f.ext)
		}
	}
	if err := zw.Close(); err != nil {
		s.log.Error("bansos split: zip close failed", "err", err, "format", f.kind)
	}
}

// bansosRegionForCode narrows the report header to one file's wilayah. An
// empty code is the unplaceable group: its header keeps whatever the
// request was filtered to, since that is all that is true of those rows.
func bansosRegionForCode(base report.Region, code string, names *wilayahNameIndex) report.Region {
	if len(code) < 10 {
		return base
	}
	r := base
	r.KabKotaCode = code[:4]
	r.KabKotaName = points.KabKotaName(code[:4])
	r.Kecamatan = code[4:7]
	r.Desa = code[7:10]
	r.SLS, r.SubSLS = "", ""
	if len(code) >= 14 {
		r.SLS = code[10:14]
	}
	if len(code) >= 16 {
		r.SubSLS = code[14:16]
	}
	r.KecamatanName, r.DesaName, r.SLSName = "", "", ""
	names.apply(&r)
	return r
}
