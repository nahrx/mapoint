// Package report holds the pieces shared by every "Daftar Hasil
// Pendataan" export format — currently pdfreport and xlsxreport: the
// wilayah/filter metadata a report is scoped to, and the couple of
// formatting rules every format renders identically. Neither export
// format is aware of the other; both only depend on this package.
package report

import (
	"fmt"
	"strconv"
	"strings"

	"se2026-titik-maps/internal/points"
)

// Region describes the wilayah — and, optionally, the extra attribute
// filters — a report is scoped to. All fields are expected to be
// already-validated (see points.Parse*) — this package only formats them,
// it doesn't validate. JenisPrelist, KeberadaanKeluarga and Status are
// empty when that filter wasn't applied; their raw values (including
// points.EmptyValue for "filter for a blank column") are rendered via
// AttrLabels. Search is "" when no name search was applied.
type Region struct {
	KabKotaCode string
	KabKotaName string
	Kecamatan   string
	Desa        string
	SLS         string
	SubSLS      string

	// JenisPrelist, KeberadaanKeluarga and Status are multi-select: each
	// holds every value the user picked, or is empty when that filter is
	// off. Rendered as one comma-separated line by AttrLabels.
	JenisPrelist       []string
	KeberadaanKeluarga []string
	Status             []string
	PenggunaanBangunan []string
	Search             string

	// FlagBaru and FlagRegsosek are the raw points.FlagYes/FlagNo values of
	// the two membership filters, or "" when that filter was off. Rendered
	// by FlagLabel.
	FlagBaru     string
	FlagRegsosek string
}

// FlagLabel renders one of the two membership filter values for a report
// header. Anything other than the two known values reads as unset, which
// only reachable if a caller skips points.ParseFlag.
func FlagLabel(val string) string {
	switch val {
	case points.FlagYes:
		return "Ya"
	case points.FlagNo:
		return "Tidak"
	}
	return ""
}

// FullCode joins the wilayah codes that are set into the longest prefix
// of level_6_full_code the report is pinned to (4+3+3+4+2 digits): all 16
// digits when drilled down to a SubSLS, 14 at SLS level, 10 at
// desa/kelurahan level. Shown once in a report's header instead of
// repeated in every row, since it's identical for the whole report.
func (r Region) FullCode() string {
	return r.KabKotaCode + r.Kecamatan + r.Desa + r.SLS + r.SubSLS
}

// PinnedToSubSLS reports whether the report covers exactly one SubSLS.
// When it doesn't, level_6_full_code varies from row to row, so the table
// needs its own ID SUBSLS column instead of relying on the single value in
// the header (see FullCode).
func (r Region) PinnedToSubSLS() bool {
	return r.SubSLS != ""
}

// WilayahRows is the "Keterangan Wilayah" label/value block, shared so PDF
// and Excel describe the same scope in the same words. Levels below what
// the filter reached read "(Semua)" rather than being dropped, so it's
// clear the report deliberately spans all of them rather than having lost
// a line.
func (r Region) WilayahRows(total int) [][2]string {
	codeLabel := "Kode Wilayah"
	if r.PinnedToSubSLS() {
		codeLabel = "Kode Wilayah (ID SUBSLS)"
	}
	return [][2]string{
		{"Kabupaten/Kota", fmt.Sprintf("%s (%s)", r.KabKotaName, r.KabKotaCode)},
		{"Kecamatan", r.Kecamatan},
		{"Desa/Kelurahan", r.Desa},
		{"SLS", allIfEmpty(r.SLS)},
		{"SubSLS", allIfEmpty(r.SubSLS)},
		{codeLabel, r.FullCode()},
		{"Jumlah Data", strconv.Itoa(total)},
	}
}

func allIfEmpty(s string) string {
	if s == "" {
		return "(Semua)"
	}
	return s
}

// ExtraFilters collects the "Filter Tambahan" label/value rows both
// export formats show identically: one pair for each of JenisPrelist,
// KeberadaanKeluarga, Status and Search that was actually applied. Empty
// when none of them were.
func (r Region) ExtraFilters() [][2]string {
	var extra [][2]string
	if len(r.JenisPrelist) > 0 {
		extra = append(extra, [2]string{"Jenis Prelist", AttrLabels(r.JenisPrelist)})
	}
	if len(r.KeberadaanKeluarga) > 0 {
		extra = append(extra, [2]string{"Keberadaan Keluarga", AttrLabels(r.KeberadaanKeluarga)})
	}
	if len(r.Status) > 0 {
		extra = append(extra, [2]string{"Status", AttrLabels(r.Status)})
	}
	if len(r.PenggunaanBangunan) > 0 {
		extra = append(extra, [2]string{"Penggunaan Bangunan", AttrLabels(r.PenggunaanBangunan)})
	}
	if r.Search != "" {
		extra = append(extra, [2]string{"Cari Nama", r.Search})
	}
	if lbl := FlagLabel(r.FlagBaru); lbl != "" {
		extra = append(extra, [2]string{"Ditemukan di Assignment Baru", lbl})
	}
	if lbl := FlagLabel(r.FlagRegsosek); lbl != "" {
		extra = append(extra, [2]string{"Ditemukan di Regsosek", lbl})
	}
	return extra
}

// AttrLabel renders an attribute filter's raw value for display, turning
// points.EmptyValue (the sentinel for "filter for a blank column") into a
// human-readable label instead of the literal sentinel string.
func AttrLabel(val string) string {
	if val == points.EmptyValue {
		return "(Kosong)"
	}
	return val
}

// AttrLabels renders every picked value of one multi-select attribute
// filter as a single comma-separated line, so a report header shows what
// was actually selected rather than just "3 dipilih".
func AttrLabels(vals []string) string {
	out := make([]string, len(vals))
	for i, v := range vals {
		out[i] = AttrLabel(v)
	}
	return strings.Join(out, ", ")
}

// DashIfEmpty renders "" as "-" — used for table cells so an empty value
// reads as deliberately blank, not like a rendering glitch.
func DashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
