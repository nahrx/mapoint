package points

// kabkotaNames maps a 4-digit BPS/Kemendagri kabupaten/kota code (the
// first 4 characters of level_6_full_code — 2-digit provinsi + 2-digit
// kabupaten/kota) to its official name. Only entries actually present in
// se2026_titik need to be here; a code with no entry still works as a
// filter, it just falls back to showing the bare code instead of a name
// (see kabkotaName below) — so this map is safe to extend incrementally
// as fieldwork covers new regions, nothing breaks if it lags behind.
var kabkotaNames = map[string]string{
	"6401": "Paser",
	"6402": "Kutai Barat",
	"6403": "Kutai Kartanegara",
	"6404": "Kutai Timur",
	"6405": "Berau",
	"6409": "Penajam Paser Utara",
	"6411": "Mahakam Ulu",
	"6471": "Balikpapan",
	"6472": "Samarinda",
	"6474": "Bontang",
}

// KabKotaName returns the known name for code, or code itself if unknown.
// Exported so callers outside this package (e.g. the PDF report builder)
// can label a kabupaten/kota without duplicating the lookup.
func KabKotaName(code string) string {
	if n, ok := kabkotaNames[code]; ok {
		return n
	}
	return code
}
