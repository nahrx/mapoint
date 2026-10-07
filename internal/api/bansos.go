package api

import (
	"fmt"
	"net/http"
	"strconv"

	"se2026-titik-maps/internal/bansos"
	"se2026-titik-maps/internal/pdfreport"
	"se2026-titik-maps/internal/points"
	"se2026-titik-maps/internal/report"
	"se2026-titik-maps/internal/xlsxreport"
)

// handleBansos serves one page of the Bansos menu's table.
//
//	GET /api/bansos?kabkota=&kecamatan=&desa=&sls=&subsls=&page=&pageSize=
//
// The five wilayah parameters are the same cascading, validated set every
// other menu uses (parseWilayah), so a code that isn't digits of the right
// length is a 400 here as well. How that prefix is matched against the two
// different wilayah columns this table has is bansos.Filter's business —
// see the comment on its clause method.
func (s *Server) handleBansos(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	wilayah, err := parseWilayah(q)
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

	search, err := parseSearch(q)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	filter := bansos.Filter{WilayahPrefix: wilayah.FullCode(), Search: search}
	resp, err := s.bansossvc.List(r.Context(), filter, page, pageSize,
		bansos.ParseSortColumn(q.Get("sortBy")), points.ParseSortDir(q.Get("dir")))
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		s.log.Error("bansos list query failed", "err", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

// bansosReport gathers what both download handlers need: the rows for the
// applied filter and the wilayah block describing it.
//
// Unlike the Daftar and Regsosek downloads there is no minimum wilayah
// here. Those two cap what a single report may span because their tables
// run to millions and hundreds of thousands of rows; this one is 16,086
// rows in total — smaller than one desa of se2026_titik2 — so even an
// unfiltered download is an ordinary report. ok is false when the error
// response has already been written.
func (s *Server) bansosReport(w http.ResponseWriter, r *http.Request) (report.Region, []bansos.Row, bool, bool) {
	q := r.URL.Query()
	wilayah, err := parseWilayah(q)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return report.Region{}, nil, false, false
	}
	search, err := parseSearch(q)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return report.Region{}, nil, false, false
	}

	// The download mirrors the screen: same filter, same search, same
	// column order.
	rows, err := s.bansossvc.ListAll(r.Context(),
		bansos.Filter{WilayahPrefix: wilayah.FullCode(), Search: search},
		bansos.ParseSortColumn(q.Get("sortBy")), points.ParseSortDir(q.Get("dir")))
	if err != nil {
		if r.Context().Err() != nil {
			return report.Region{}, nil, false, false
		}
		s.log.Error("bansos report query failed", "err", err)
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
		// Shown in the "Filter Tambahan" block, so a printed page says
		// which search produced it.
		Search: search,
	}
	s.wilayahNameIndexFor(r.Context()).apply(&region)

	return region, rows, len(rows) >= bansos.ReportMaxRows, true
}

// bansosFilename is the wilayah part of a download's filename: the code
// the report is pinned to, or "semua" when it spans everything.
func bansosFilename(region report.Region) string {
	if code := region.FullCode(); code != "" {
		return code
	}
	return "semua"
}

func (s *Server) handleBansosPDF(w http.ResponseWriter, r *http.Request) {
	split, err := parseSplit(r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if split.on() {
		s.handleBansosSplit(w, r, bansosSplitPDF, split)
		return
	}
	region, rows, truncated, ok := s.bansosReport(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="daftar-bansos-%s.pdf"`, bansosFilename(region)))
	w.Header().Set("Cache-Control", "no-store")
	if err := pdfreport.GenerateBansos(w, region, rows, truncated); err != nil {
		// Headers are already out; all that can be done is log it.
		s.log.Error("bansos pdf generation failed", "err", err)
	}
}

func (s *Server) handleBansosXLSX(w http.ResponseWriter, r *http.Request) {
	split, err := parseSplit(r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if split.on() {
		s.handleBansosSplit(w, r, bansosSplitXLSX, split)
		return
	}
	region, rows, truncated, ok := s.bansosReport(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="daftar-bansos-%s.xlsx"`, bansosFilename(region)))
	w.Header().Set("Cache-Control", "no-store")
	if err := xlsxreport.GenerateBansos(w, region, rows, truncated); err != nil {
		s.log.Error("bansos xlsx generation failed", "err", err)
	}
}
