package api

import (
	"context"
	"iter"
	"net/http"
	"net/url"
	"time"

	"se2026-titik-maps/internal/points"
	"se2026-titik-maps/internal/report"
)

// How a Daftar download gets its rows, shared by the single-file handlers
// (handleListPDF / handleListXLSX) and the per-SubSLS ZIP
// (handleSplitReport).
//
// Any scope down to one kecamatan is one query. A whole kabupaten/kota is
// not: Samarinda is 552k rows, and holding that in memory once per
// download (~300 MB of points.Point) is the wrong trade for a download
// that runs a few times a day, so it is fetched and rendered one kecamatan
// at a time — the memory high-water mark stays at one kecamatan
// (≤ points.SplitReportMaxRows) whatever the width. Both generators take
// an iterator for exactly this reason: they never see the whole set.
//
// The first unit is always fetched before any header is written, so an
// empty or failing query still gets a real status code; later units fail
// into the stream (logged, short file), the same way a generation error
// already did.

// reportPlan is a validated download with its rows planned but, beyond the
// first unit, not yet fetched.
type reportPlan struct {
	scope  reportScope
	region report.Region
	units  []points.Filter // one filter per query, in output order
	first  []points.Point  // rows of units[0], fetched up front
	// total is the row count for the report header. Exact for a single
	// unit (len(first)); a count query for a kabupaten/kota, made before
	// any row is rendered.
	total int
	// truncated is len(first) >= maxRows for a single unit. A multi-unit
	// plan can't be truncated in practice (the cap is per kecamatan and
	// above the largest one), so it reports false.
	truncated bool
}

// planReport validates the request (parseReportScope), decides the units
// and fetches the first one. ok is false when it has already written the
// error response.
func (s *Server) planReport(w http.ResponseWriter, r *http.Request, q url.Values, downloadKind string, split bool) (*reportPlan, bool) {
	scope, ok := s.parseReportScope(w, q, downloadKind, split)
	if !ok {
		return nil, false
	}
	p := &reportPlan{scope: scope, region: regionFor(scope.filter), units: []points.Filter{scope.filter}}
	ctx := r.Context()

	if scope.filter.Kecamatan == "" {
		kecs, err := s.svc.KecamatanList(ctx, scope.filter.KabKota)
		if err != nil {
			if ctx.Err() != nil {
				return nil, false
			}
			s.log.Error("report: kecamatan list failed", "err", err, "kabkota", scope.filter.KabKota)
			writeError(w, http.StatusInternalServerError, "query failed")
			return nil, false
		}
		// An empty list (a kabupaten/kota with no rows at all) keeps the
		// scope itself as the one unit: its query returns nothing and the
		// report comes out empty, rather than indexing into no units.
		if len(kecs) > 0 {
			p.units = p.units[:0]
			for _, k := range kecs {
				u := scope.filter
				u.Kecamatan = k.Code
				p.units = append(p.units, u)
			}
		}
	}

	first, err := s.svc.ListAllUpTo(ctx, p.units[0], scope.sortBy, scope.dir, scope.maxRows)
	if err != nil {
		if ctx.Err() != nil {
			return nil, false
		}
		s.log.Error("list-report query failed", "err", err, "format", downloadKind)
		writeError(w, http.StatusInternalServerError, "query failed")
		return nil, false
	}
	p.first = first
	p.total = len(first)
	p.truncated = len(first) >= scope.maxRows

	if len(p.units) > 1 {
		n, err := s.svc.Count(ctx, scope.filter)
		if err != nil {
			if ctx.Err() != nil {
				return nil, false
			}
			s.log.Error("report: count failed", "err", err, "format", downloadKind)
			writeError(w, http.StatusInternalServerError, "query failed")
			return nil, false
		}
		p.total = n
		p.truncated = false
	}
	return p, true
}

// wide reports whether the download is wider than one desa/kelurahan — the
// ones that can outlive the server's global 30-second WriteTimeout.
func (p *reportPlan) wide() bool {
	return p.scope.filter.Desa == ""
}

// rows yields every row of the plan in order: the prefetched first unit,
// then each further unit as it is fetched. A query error after the first
// unit is reported through onErr and ends the sequence early — the
// response is already streaming by then, so there is nowhere else for it
// to go. A cancelled context ends it silently.
func (p *reportPlan) rows(ctx context.Context, s *Server, onErr func(unit points.Filter, err error)) iter.Seq[points.Point] {
	return func(yield func(points.Point) bool) {
		for i, unit := range p.units {
			items := p.first
			if i > 0 {
				if ctx.Err() != nil {
					return
				}
				var err error
				items, err = s.svc.ListAllUpTo(ctx, unit, p.scope.sortBy, p.scope.dir, p.scope.maxRows)
				if err != nil {
					onErr(unit, err)
					return
				}
			}
			for _, pt := range items {
				if !yield(pt) {
					return
				}
			}
			p.first = nil // let the first unit be collected once rendered
		}
	}
}

// reportWriteTimeout replaces the server's 30-second WriteTimeout for a
// download wider than one desa. That global limit is right for API calls,
// but these files are generated while they stream: the largest kecamatan
// takes ~11 s as a ZIP and a whole kabupaten/kota (Samarinda: 2,988
// SubSLS, 552k rows) runs to about a minute, and a deadline that fires
// mid-file hands the user a broken download with no error.
const reportWriteTimeout = 15 * time.Minute

// extendWriteDeadline lifts the write deadline for a wide download. Not
// fatal when unsupported: the download still works for anything that fits
// in the global 30 s, and the log has a trail for a truncated one.
func (s *Server) extendWriteDeadline(w http.ResponseWriter, p *reportPlan) {
	if !p.wide() {
		return
	}
	if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(reportWriteTimeout)); err != nil {
		s.log.Warn("report: could not extend write deadline", "err", err)
	}
}
