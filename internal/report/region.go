// Package report holds the pieces shared by every "Daftar Hasil
// Pendataan" export format — currently pdfreport and xlsxreport: the
// wilayah/filter metadata a report is scoped to, and the couple of
// formatting rules every format renders identically. Neither export
// format is aware of the other; both only depend on this package.
package report

import "se2026-titik-maps/internal/points"

// Region describes the wilayah — and, optionally, the extra attribute
// filters — a report is scoped to. All fields are expected to be
// already-validated (see points.Parse*) — this package only formats them,
// it doesn't validate. JenisPrelist, KeberadaanKeluarga and Status are ""
// when that filter wasn't applied; a set filter's raw value (including
// points.EmptyValue for "filter for a blank column") is rendered via
// AttrLabel. Search is "" when no name search was applied.
type Region struct {
	KabKotaCode string
	KabKotaName string
	Kecamatan   string
	Desa        string
	SLS         string
	SubSLS      string

	JenisPrelist       string
	KeberadaanKeluarga string
	Status             string
	Search             string
}

// FullCode reconstructs the 16-digit level_6_full_code these five codes
// pin exactly (4+3+3+4+2 digits) — shown once in a report's header
// instead of repeated in every row, since it's identical for the whole
// report.
func (r Region) FullCode() string {
	return r.KabKotaCode + r.Kecamatan + r.Desa + r.SLS + r.SubSLS
}

// ExtraFilters collects the "Filter Tambahan" label/value rows both
// export formats show identically: one pair for each of JenisPrelist,
// KeberadaanKeluarga, Status and Search that was actually applied. Empty
// when none of them were.
func (r Region) ExtraFilters() [][2]string {
	var extra [][2]string
	if r.JenisPrelist != "" {
		extra = append(extra, [2]string{"Jenis Prelist", AttrLabel(r.JenisPrelist)})
	}
	if r.KeberadaanKeluarga != "" {
		extra = append(extra, [2]string{"Keberadaan Keluarga", AttrLabel(r.KeberadaanKeluarga)})
	}
	if r.Status != "" {
		extra = append(extra, [2]string{"Status", AttrLabel(r.Status)})
	}
	if r.Search != "" {
		extra = append(extra, [2]string{"Cari Nama", r.Search})
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

// DashIfEmpty renders "" as "-" — used for table cells so an empty value
// reads as deliberately blank, not like a rendering glitch.
func DashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
