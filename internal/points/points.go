// Package points implements viewport-aware queries against the
// se2026_titik2 ClickHouse table: individual points when the visible area
// is sparse enough, grid clusters otherwise. This is what lets a
// multi-million-row table be shown "seamlessly" without ever shipping more
// than a few thousand markers to the browser at once.
package points

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

const (
	table = "se2026_titik2"

	// matchTable and regsosekTable are the sources the two dictionaries are
	// built from (see sql/dictionaries.sql). Queries never read them
	// directly any more — they go through dictHas/dictGetString instead.
	matchTable    = "se2026_match"
	regsosekTable = "se2026_match_regsosek"

	// IndividualLimit is the max rows returned as raw points for one
	// request. Below this the viewport is "sparse" and every point is
	// shown individually with full tooltip data.
	IndividualLimit = 3000

	// ClusterLimit caps how many grid cells one clustering response can
	// return, as a safety net against pathological grid sizes.
	ClusterLimit = 20000

	queryTimeout = 15 * time.Second
)

// BBox is a geographic viewport, WGS84 degrees.
type BBox struct {
	MinLat, MaxLat float64
	MinLon, MaxLon float64
}

// Parse validates and clamps a bbox from raw query-string values.
func ParseBBox(minLat, maxLat, minLon, maxLon string) (BBox, error) {
	vals := make([]float64, 4)
	for i, s := range []string{minLat, maxLat, minLon, maxLon} {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return BBox{}, fmt.Errorf("invalid coordinate %q: %w", s, err)
		}
		vals[i] = v
	}
	b := BBox{MinLat: vals[0], MaxLat: vals[1], MinLon: vals[2], MaxLon: vals[3]}
	if b.MinLat > b.MaxLat || b.MinLon > b.MaxLon {
		return BBox{}, fmt.Errorf("bbox min must not exceed max")
	}
	b.MinLat = clamp(b.MinLat, -90, 90)
	b.MaxLat = clamp(b.MaxLat, -90, 90)
	b.MinLon = clamp(b.MinLon, -180, 180)
	b.MaxLon = clamp(b.MaxLon, -180, 180)
	return b, nil
}

var (
	fourDigitCodeRe  = regexp.MustCompile(`^[0-9]{4}$`) // shared by kabkota and SLS codes
	threeDigitCodeRe = regexp.MustCompile(`^[0-9]{3}$`) // shared by kecamatan and desa/kelurahan codes
	twoDigitCodeRe   = regexp.MustCompile(`^[0-9]{2}$`) // SubSLS codes
)

// ParseKabKota validates a kabupaten/kota filter value: either empty (no
// filter) or exactly 4 digits — the provinsi+kabupaten/kota prefix of
// level_6_full_code.
func ParseKabKota(code string) (string, error) {
	if code == "" {
		return "", nil
	}
	if !fourDigitCodeRe.MatchString(code) {
		return "", fmt.Errorf("invalid kabkota code %q: expected 4 digits", code)
	}
	return code, nil
}

// ParseKecamatan validates a kecamatan filter value: either empty (no
// filter) or exactly 3 digits — the 3 characters right after the
// kabupaten/kota prefix in level_6_full_code. A kecamatan code is only
// unique within its kabupaten/kota, so it's meaningless without one — the
// caller (see Filter.Validate) must ensure KabKota is set whenever
// Kecamatan is.
func ParseKecamatan(code string) (string, error) {
	if code == "" {
		return "", nil
	}
	if !threeDigitCodeRe.MatchString(code) {
		return "", fmt.Errorf("invalid kecamatan code %q: expected 3 digits", code)
	}
	return code, nil
}

// ParseDesa validates a desa/kelurahan filter value: either empty (no
// filter) or exactly 3 digits — the 3 characters right after the
// kecamatan code in level_6_full_code. Like Kecamatan, it's only unique
// within its kecamatan, so it's meaningless without one — see Validate.
func ParseDesa(code string) (string, error) {
	if code == "" {
		return "", nil
	}
	if !threeDigitCodeRe.MatchString(code) {
		return "", fmt.Errorf("invalid desa/kelurahan code %q: expected 3 digits", code)
	}
	return code, nil
}

// ParseSLS validates a Kode SLS filter value: either empty (no filter) or
// exactly 4 digits — the 4 characters right after the desa/kelurahan code
// in level_6_full_code. Like Desa, it's only unique within its
// desa/kelurahan, so it's meaningless without one — see Validate.
func ParseSLS(code string) (string, error) {
	if code == "" {
		return "", nil
	}
	if !fourDigitCodeRe.MatchString(code) {
		return "", fmt.Errorf("invalid SLS code %q: expected 4 digits", code)
	}
	return code, nil
}

// ParseSubSLS validates a Kode SubSLS filter value: either empty (no
// filter) or exactly 2 digits — the 2 characters right after the SLS code
// in level_6_full_code (the last 2 of its 16 digits). Like SLS, it's only
// unique within its SLS, so it's meaningless without one — see Validate.
func ParseSubSLS(code string) (string, error) {
	if code == "" {
		return "", nil
	}
	if !twoDigitCodeRe.MatchString(code) {
		return "", fmt.Errorf("invalid SubSLS code %q: expected 2 digits", code)
	}
	return code, nil
}

// jenisPrelistValues, keberadaanKeluargaValues and statusValues are the
// closed sets of values jenis_prelist_root, keberadaan_keluarga and
// assignment_status_alias actually hold. They double as the dropdown
// choices served by GET /api/filter-options and the server-side whitelist
// a raw filter value is checked against in parseEnumFilter — so an
// attribute filter can only ever become a value that column can genuinely
// hold, never arbitrary text dropped into SQL.
var (
	jenisPrelistValues = []string{
		"keluarga", "UMKM", "OSS Perorangan", "OSS Badan Usaha",
		"bangunan_lain", "UB", "dummy", "KEK-KI",
	}
	keberadaanKeluargaValues = []string{
		"0. Tidak Ditemukan (STOP)", "1. Ditemukan", "2. Baru", "3. Meninggal",
		"4. Tidak Eligible", "5. Tidak dapat ditemui sampai akhir pendataan",
		"6. Keluarga Khusus",
	}
	statusValues = []string{
		"OPEN", "DRAFT", "SUBMITTED BY Pencacah", "APPROVED BY Pengawas",
		"REJECTED BY Pengawas", "REVOKED BY Pengawas", "SUBMITTED RESPONDENT",
		"EDITED BY Pengawas", "REJECTED BY Admin Kabupaten",
		"EDITED BY Admin Kabupaten", "COMPLETED BY Admin Kabupaten",
		"REVOKED BY Admin Kabupaten",
	}
	// keberadaan_BKU, shown everywhere as "Keberadaan Usaha" — the column
	// name keeps the source's spelling, the label is what the form calls
	// it. Listed in the source's own numeric order, and note the gaps
	// (5 and 6 are absent from the data). Blank on 68.5% of rows, by far
	// the most common value, but not listed here: like every other
	// attribute filter it reaches the filter as EmptyValue.
	keberadaanBKUValues = []string{
		"0. Tidak Ditemukan",
		"1. Ditemukan",
		"2. Baru",
		"3. Tutup",
		"4. Ganda",
		"7. Data diperoleh dari Kantor Pusat (KP)",
	}
	// kode_penggunaan_bangunan_label, listed in the source's own numeric
	// order rather than by frequency so the dropdown reads like the form it
	// came from. Blank is by far the most common value (42% of rows at the
	// time of writing) but isn't listed here — it reaches the filter as
	// EmptyValue, the same as every other attribute filter.
	penggunaanBangunanValues = []string{
		"1. Bangunan Khusus Usaha",
		"2. Bangunan Campuran",
		"3. Bangunan Tempat Tinggal",
		"4. Tempat ibadah, kantor organisasi (profesi, kemasyarakatan, sosial, politik)",
		"5. Kantor pemerintah, kedutaan/konsulat",
		"6. Bangunan Lainnya yang Tidak Tercakup (Tempat Judi, Tempat Layanan Kencan, Bangunan Kosong/ Rusak)",
		"7. Virtual Office (VO)",
		"8. Panti asuhan, panti jompo(yang tidak berbayar), lapas (lembaga pemasyarakatan), barak militer",
		"9. Non Respon",
	}
)

// FilterOptions is the payload for GET /api/filter-options: the fixed
// dropdown choices for the Daftar menu's attribute filters, so the
// frontend never has to hardcode (and risk drifting from) these lists.
type FilterOptions struct {
	JenisPrelist       []string `json:"jenis_prelist"`
	KeberadaanKeluarga []string `json:"keberadaan_keluarga"`
	Status             []string `json:"status"`
	PenggunaanBangunan []string `json:"penggunaan_bangunan"`
	KeberadaanBKU      []string `json:"keberadaan_bku"`
}

// GetFilterOptions returns the attribute filter dropdown choices.
func GetFilterOptions() FilterOptions {
	return FilterOptions{
		JenisPrelist:       jenisPrelistValues,
		KeberadaanKeluarga: keberadaanKeluargaValues,
		Status:             statusValues,
		PenggunaanBangunan: penggunaanBangunanValues,
		KeberadaanBKU:      keberadaanBKUValues,
	}
}

// The two flags and the joined assignment_id_baru all resolve through
// ClickHouse dictionaries rather than IN-subqueries or a JOIN. See
// sql/dictionaries.sql for the DDL — the app cannot serve /api/list or
// /api/points without them.
//
// Why: an IN-subquery or JOIN re-reads its source table on every single
// query (89k rows for se2026_match, 167k for se2026_match_regsosek), and
// that was the largest cost in the whole database layer. Measured on one
// Daftar page over the largest desa: 112ms with JOIN + IN, 21ms with these
// dictionaries — against 19ms for the same query carrying none of these
// three columns at all. Both were verified equivalent across all 2,168,304
// rows before the switch: 0 differing flags and 0 differing
// assignment_id_baru values.
//
// The names are unqualified so they resolve against the connection's
// database, exactly like `table` above — DATABASE in .env stays the single
// place that decides which database is used.
const (
	matchDict    = "dict_match_tdk"
	regsosekDict = "dict_regsosek_key"
)

// dictHasExpr tests membership; dictGetBaruExpr reads the matched row's
// assignment_id_baru ("" when there is no match). Both take the column
// holding se2026_titik2.assignment_id. tuple() is required because the
// dictionaries are COMPLEX_KEY_* — their key is a String, and ClickHouse's
// plain HASHED layout only accepts UInt64 keys.
//
// Only this package's own constants reach these format strings; col is
// likewise a literal at every call site, never caller input.
func dictHasExpr(dict, col string) string {
	return fmt.Sprintf("dictHas('%s', tuple(%s))", dict, col)
}

func dictGetBaruExpr(col string) string {
	return fmt.Sprintf("dictGetString('%s', 'aid_baru', tuple(%s))", matchDict, col)
}

// dictionarySources pairs each dictionary with the table it is built from,
// so CheckDictionaries can say which one is missing and where it comes
// from. A slice rather than a map so the order of any startup complaint is
// stable.
var dictionarySources = []struct{ dict, source string }{
	{matchDict, matchTable},
	{regsosekDict, regsosekTable},
}

// CheckDictionaries probes every dictionary the query layer depends on.
// Worth doing at startup because the failure mode is otherwise invisible
// until a user hits it: a missing dictionary doesn't degrade a query, it
// makes /api/list and /api/points fail outright. The probe uses a key that
// matches nothing, so it costs one lookup and forces a lazy-loading
// dictionary to actually load.
func (s *Service) CheckDictionaries(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	for _, d := range dictionarySources {
		var found uint8
		q := "SELECT " + dictHasExpr(d.dict, "''")
		if err := s.conn.QueryRow(ctx, q).Scan(&found); err != nil {
			return fmt.Errorf("dictionary %q (built from %s) is not usable — create it with sql/dictionaries.sql: %w", d.dict, d.source, err)
		}
	}
	return nil
}

// FlagYes and FlagNo are the two values a flag filter can take over the
// wire; "" means the filter is unset. Kept as strings rather than a *bool
// so the filter reads the same as every other optional filter here.
const (
	FlagYes = "1"
	FlagNo  = "0"
)

// ParseFlag validates one of the two "ditemukan di …" filter values.
// Anything other than "", "1" or "0" is rejected rather than coerced, so a
// typo surfaces as a 400 instead of silently filtering the wrong way.
func ParseFlag(raw string) (string, error) {
	switch raw {
	case "", FlagYes, FlagNo:
		return raw, nil
	}
	return "", fmt.Errorf("invalid flag filter value %q: expected %q or %q", raw, FlagYes, FlagNo)
}

// flagClause turns a validated flag value into a WHERE fragment.
func flagClause(dict, val string) string {
	expr := dictHasExpr(dict, "assignment_id")
	if val == FlagNo {
		return "NOT " + expr
	}
	return expr
}

// EmptyValue is the sentinel a caller passes to explicitly filter for rows
// where the column itself is blank, as distinct from leaving the filter
// unset entirely. Parse*'s zero value ("") already means "no filter", so
// plain "" can't also stand for "the column is blank" — this sentinel
// disambiguates the two states over the wire (e.g. ?jenisPrelist=__EMPTY__).
const EmptyValue = "__EMPTY__"

// parseEnumFilter validates raw against allowed, the closed set of values
// its target column can hold. An empty or all-blank list means "no
// filter"; EmptyValue means "filter for a blank column" and may be
// combined with real values; anything else must match allowed exactly,
// since only whitelisted strings ever reach the SQL WHERE clause this
// feeds — see (Filter).clause.
//
// Duplicates are collapsed so a repeated query parameter can't inflate the
// IN list, and the original order is kept so a report header lists the
// values the way the user picked them.
func parseEnumFilter(raw []string, allowed []string) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	for _, v := range raw {
		if v == "" {
			// The "Semua" option submits an empty value; ignoring it lets a
			// form post a blank alongside real picks without meaning
			// "match rows whose column is blank" — that's EmptyValue.
			continue
		}
		if seen[v] {
			continue
		}
		if v != EmptyValue {
			ok := false
			for _, a := range allowed {
				if v == a {
					ok = true
					break
				}
			}
			if !ok {
				return nil, fmt.Errorf("invalid filter value %q", v)
			}
		}
		seen[v] = true
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// ParseJenisPrelist validates the Jenis Prelist filter values.
func ParseJenisPrelist(raw []string) ([]string, error) {
	return parseEnumFilter(raw, jenisPrelistValues)
}

// ParseKeberadaanKeluarga validates the Keberadaan Keluarga filter values.
func ParseKeberadaanKeluarga(raw []string) ([]string, error) {
	return parseEnumFilter(raw, keberadaanKeluargaValues)
}

// ParseStatus validates the Status filter values.
func ParseStatus(raw []string) ([]string, error) {
	return parseEnumFilter(raw, statusValues)
}

// ParsePenggunaanBangunan validates the Penggunaan Bangunan filter values.
func ParsePenggunaanBangunan(raw []string) ([]string, error) {
	return parseEnumFilter(raw, penggunaanBangunanValues)
}

// ParseKeberadaanBKU validates the Keberadaan Usaha filter values
// (keberadaan_BKU in the table).
func ParseKeberadaanBKU(raw []string) ([]string, error) {
	return parseEnumFilter(raw, keberadaanBKUValues)
}

// Filter narrows a query down to a wilayah and, optionally, a handful of
// row attributes. The five wilayah fields come from level_6_full_code:
// KabKota is characters 1-4, Kecamatan is characters 5-7, Desa is
// characters 8-10, SLS is characters 11-14, SubSLS is characters 15-16
// (its last two). Each is meaningless without the one above it — see
// Validate. JenisPrelist, KeberadaanKeluarga and Status are independent of
// wilayah and of each other, and each holds a *set* of accepted values:
// empty (unset), or any mix of that column's known values and EmptyValue
// (blank column) — see the Parse* functions above. Values within one
// filter are OR-ed; the filters themselves are AND-ed together.
type Filter struct {
	KabKota   string
	Kecamatan string
	Desa      string
	SLS       string
	SubSLS    string

	JenisPrelist       []string
	KeberadaanKeluarga []string
	Status             []string
	PenggunaanBangunan []string
	KeberadaanBKU      []string

	// FlagBaru and FlagRegsosek filter on the two membership flags:
	// FlagYes keeps only rows found in that table, FlagNo only rows not
	// found in it, "" leaves the filter off. Validated by ParseFlag.
	FlagBaru     string
	FlagRegsosek string

	// Search is free-text user input matched against nama_assignment
	// (see clause). Unlike every other field above, it is never validated
	// against a whitelist — it's bound as a query parameter instead of
	// being interpolated into the SQL text, so it's safe as-is.
	Search string
}

// FullCode reconstructs the 16-digit level_6_full_code the five wilayah
// fields pin exactly (4+3+3+4+2 digits) when all of them are set — meant
// for callers that need a single SubSLS pinned down, like the PDF report
// or the SubSLS boundary polygon lookup. Meaningless (and not guaranteed
// to be 16 characters) unless every level down to SubSLS is set.
func (f Filter) FullCode() string {
	return f.KabKota + f.Kecamatan + f.Desa + f.SLS + f.SubSLS
}

// Validate reports an error if a narrower field is set without its parent,
// since each code is only unique within its parent.
func (f Filter) Validate() error {
	if f.Kecamatan != "" && f.KabKota == "" {
		return fmt.Errorf("kecamatan filter requires a kabkota filter")
	}
	if f.Desa != "" && f.Kecamatan == "" {
		return fmt.Errorf("desa/kelurahan filter requires a kecamatan filter")
	}
	if f.SLS != "" && f.Desa == "" {
		return fmt.Errorf("SLS filter requires a desa/kelurahan filter")
	}
	if f.SubSLS != "" && f.SLS == "" {
		return fmt.Errorf("SubSLS filter requires an SLS filter")
	}
	return nil
}

// clause builds the WHERE fragment for filter, plus any bind arguments it
// needs. Every field except Search is either a regex-validated digit
// string or matched against a closed whitelist (see Parse* above), so it's
// safe to interpolate directly into the SQL text. Search is genuine
// free-text user input, so it's never interpolated — it's passed back as a
// bind arg for the ? placeholder in the search fragment, and the caller
// must forward args to conn.Query/QueryRow alongside the query text.
func (f Filter) clause() (string, []any) {
	parts := make([]string, 0, 6)
	var args []any
	// The five wilayah levels are hierarchical and Validate rejects a level
	// whose parent is unset, so whatever is set always forms a contiguous
	// prefix of level_6_full_code — which FullCode already concatenates.
	// That lets one startsWith replace what used to be five separate
	// substring(...) = ... predicates, and it matters a lot: ClickHouse
	// rewrites startsWith on the sort key into a primary-key range
	// (level_6_full_code is the first sort column), while substring() is
	// opaque to the index and forces a full scan. Measured on 2.17M rows,
	// one kecamatan: 2,184,688 rows read before vs 131,072 after.
	//
	// Only valid for a filter that has been through Validate — callers get
	// that via parseFilter. An unvalidated Filter with a gap (say Desa set
	// but KabKota empty) would build a prefix that means something else.
	if prefix := f.FullCode(); prefix != "" {
		parts = append(parts, fmt.Sprintf("startsWith(level_6_full_code, '%s')", prefix))
	}
	if len(f.JenisPrelist) > 0 {
		parts = append(parts, attrClause("jenis_prelist_root", f.JenisPrelist))
	}
	if len(f.KeberadaanKeluarga) > 0 {
		parts = append(parts, attrClause("keberadaan_keluarga", f.KeberadaanKeluarga))
	}
	if len(f.Status) > 0 {
		parts = append(parts, attrClause("assignment_status_alias", f.Status))
	}
	if len(f.PenggunaanBangunan) > 0 {
		parts = append(parts, attrClause("kode_penggunaan_bangunan_label", f.PenggunaanBangunan))
	}
	// Quoted identifier: the column really is spelled keberadaan_BKU, and
	// ClickHouse identifiers are case-sensitive.
	if len(f.KeberadaanBKU) > 0 {
		parts = append(parts, attrClause("keberadaan_BKU", f.KeberadaanBKU))
	}
	// Both are dictionary lookups now, so they no longer carry a per-query
	// set build — the fragment is still only added when the filter is on,
	// simply because an absent filter has nothing to say.
	if f.FlagBaru != "" {
		parts = append(parts, flagClause(matchDict, f.FlagBaru))
	}
	if f.FlagRegsosek != "" {
		parts = append(parts, flagClause(regsosekDict, f.FlagRegsosek))
	}
	if f.Search != "" {
		// positionCaseInsensitive is a plain substring search, not a LIKE
		// pattern, so there's no %/_ wildcard to escape on top of the bind
		// parameter already keeping the raw text out of the SQL text.
		parts = append(parts, "positionCaseInsensitive(nama_assignment, ?) > 0")
		args = append(args, f.Search)
	}
	if len(parts) == 0 {
		return "1", nil
	}
	return strings.Join(parts, " AND "), args
}

// attrClause builds the WHERE fragment for one attribute filter: an IN
// list over the picked values, which is what makes these filters
// multi-select. Every value has already passed parseEnumFilter against a
// fixed whitelist (see ParseJenisPrelist etc.), so each is either
// EmptyValue or a known-safe string — never arbitrary text — by the time
// it lands here, which is why they can be interpolated directly.
//
// EmptyValue becomes a literal ” inside the same list rather than a
// separate OR: "blank" is just another value the column can hold, so
// "(Kosong)" combines with real picks for free.
func attrClause(col string, vals []string) string {
	quoted := make([]string, len(vals))
	for i, v := range vals {
		if v == EmptyValue {
			quoted[i] = "''"
			continue
		}
		quoted[i] = "'" + v + "'"
	}
	return fmt.Sprintf("%s IN (%s)", col, strings.Join(quoted, ", "))
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// Point is a single map marker with the fields needed for the hover tooltip.
type Point struct {
	Lat          float64 `json:"lat"`
	Lon          float64 `json:"lon"`
	AssignmentID string  `json:"assignment_id"`
	Nama         string  `json:"nama"`
	Alamat       string  `json:"alamat"`
	SubSLS       string  `json:"subsls"`
	JenisPrelist string  `json:"jenis_prelist"`
	// PenggunaanBangunan is kode_penggunaan_bangunan_label. Blank on 42% of
	// rows, which is a real value here (the question went unanswered), not
	// a rendering gap — hence no omitempty.
	PenggunaanBangunan string `json:"penggunaan_bangunan"`
	// KeberadaanUsaha is scanned from the table's jumlah_usaha column
	// (renamed there from keberadaan_usaha; it always held a count of usaha
	// at the point, not a 0/1 presence flag). int32 to match that column's
	// type — a uint8 would fail to scan the ~1.8k rows whose value exceeds
	// 255. Still fetched on every query (unused fields cost nothing extra
	// here) even though it's temporarily hidden from every display —
	// tooltip, Daftar table, PDF and Excel all skip rendering it for now;
	// see NomorBangunan below for what replaced it in those views.
	KeberadaanUsaha int32 `json:"keberadaan_usaha"`
	// NomorBangunan is shown wherever KeberadaanUsaha used to be: tooltip,
	// Daftar table, PDF and Excel. int32 to match the column (its one
	// sentinel/overflow-looking value is math.MaxInt32, which still scans
	// fine at this width).
	NomorBangunan      int32  `json:"nomor_bangunan"`
	KeberadaanKeluarga string `json:"keberadaan_keluarga"`
	// KeberadaanBKU is the keberadaan_BKU column, displayed as "Keberadaan
	// Usaha" in both menus. Not to be confused with KeberadaanUsaha above,
	// which is the jumlah_usaha *count* and stays hidden — these are two
	// different columns that the form happens to name similarly.
	KeberadaanBKU string `json:"keberadaan_bku"`
	Status        string `json:"status"`
	// AdaAssignmentBaru and AdaRegsosek report whether this row's
	// assignment_id appears in se2026_match (as assignment_id_tdk) and in
	// se2026_match_regsosek respectively. They are presence flags only —
	// neither source table contributes any other column here.
	AdaAssignmentBaru bool `json:"ada_assignment_baru"`
	AdaRegsosek       bool `json:"ada_regsosek"`
	// AssignmentIDBaru is se2026_match.assignment_id_baru for this row, or
	// "" when there is none. Only populated by the Daftar list path —
	// /api/points doesn't select it, so it is omitted from the map payload
	// rather than shipped empty on every marker. For
	// the 0.9% of matched rows whose key maps to more than one new
	// assignment, this holds all of them, comma-separated.
	AssignmentIDBaru string `json:"assignment_id_baru,omitempty"`
	// Catatan is free-text field notes — only populated by ListAll (the
	// PDF/Excel report path, via listItems' includeCatatan). Left "" for
	// /api/points and /api/list, which don't select this column at all:
	// it can run to over a thousand characters and isn't meant for
	// on-screen browsing, only for the downloaded report.
	Catatan string `json:"catatan,omitempty"`
}

// Cluster is an aggregated grid cell representing many points.
type Cluster struct {
	Lat   float64 `json:"lat"`
	Lon   float64 `json:"lon"`
	Count uint64  `json:"count"`
}

// Response is what /api/points returns.
type Response struct {
	Type     string    `json:"type"` // "points" or "clusters"
	Total    uint64    `json:"total"`
	Points   []Point   `json:"points"`
	Clusters []Cluster `json:"clusters"`
}

// Bounds summarizes the full dataset's geographic extent and row count, used
// to set the map's initial view.
type Bounds struct {
	MinLat, MaxLat float64 `json:"-"`
	MinLon, MaxLon float64 `json:"-"`
	Total          uint64  `json:"total"`
}

type BoundsJSON struct {
	MinLat float64 `json:"min_lat"`
	MaxLat float64 `json:"max_lat"`
	MinLon float64 `json:"min_lon"`
	MaxLon float64 `json:"max_lon"`
	Total  uint64  `json:"total"`
}

// Service executes the queries against ClickHouse.
type Service struct {
	conn driver.Conn
}

func NewService(conn driver.Conn) *Service {
	return &Service{conn: conn}
}

const validCoords = "latitude_ppl != 0 AND longitude_ppl != 0 AND isFinite(latitude_ppl) AND isFinite(longitude_ppl)"

// bboxIf builds the four percentile aggregates a wilayah list returns for
// map auto-zoom, computed over plottable rows only.
//
// The condition lives in the aggregate rather than the WHERE on purpose.
// These lists feed the filter dropdowns of every menu, and the Daftar menu
// shows rows whether or not they carry coordinates — filtering the whole
// query by validCoords silently dropped any wilayah with no plottable
// point from the dropdowns. Measured against live data at the time: 1,216
// of 14,412 SLS, 1,351 of 17,116 SubSLS, and 6 each of kecamatan and desa
// were missing that way.
//
// A wilayah with no plottable row yields NaN here; scanBBox turns that into
// a nil pointer so it is omitted from the JSON rather than crashing the
// encoder (encoding/json refuses NaN) or arriving as a fake 0/0.
const bboxIf = `quantileIf(0.01)(latitude_ppl, ` + validCoords + `),
		quantileIf(0.99)(latitude_ppl, ` + validCoords + `),
		quantileIf(0.01)(longitude_ppl, ` + validCoords + `),
		quantileIf(0.99)(longitude_ppl, ` + validCoords + `)`

// FiniteOrNil is the NaN guard between ClickHouse and the JSON encoder:
// a wilayah with no plottable row has no bounding box, and NaN would
// crash encoding/json while 0 would be a real coordinate in the Atlantic.
func FiniteOrNil(v float64) *float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	return &v
}

func bboxClause(b BBox) string {
	return fmt.Sprintf(
		"latitude_ppl BETWEEN %s AND %s AND longitude_ppl BETWEEN %s AND %s",
		fFloat(b.MinLat), fFloat(b.MaxLat), fFloat(b.MinLon), fFloat(b.MaxLon),
	)
}

func fFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// Query returns individual points if the viewport is sparse, or grid
// clusters otherwise. zoom is the Leaflet zoom level (0-20+) and only
// affects cluster cell size, never correctness. filter, if set, restricts
// results to a kabupaten/kota and/or kecamatan.
func (s *Service) Query(ctx context.Context, b BBox, zoom int, filter Filter) (Response, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	// Cluster first, then decide. The grid aggregation already carries a
	// count per cell, so summing those gives the viewport total for free —
	// which is what the separate count() this used to run first was for.
	// Both queries read exactly the same rows under the same WHERE, so
	// dropping one halves the database work for any viewport dense enough
	// to be shown as clusters (measured on an unfiltered province-wide
	// view: ~600k rows scanned per query, ~30ms, twice).
	clusters, err := s.queryClusters(ctx, b, zoom, filter)
	if err != nil {
		return Response{}, fmt.Errorf("points: query clusters: %w", err)
	}

	var total uint64
	for _, c := range clusters {
		total += c.Count
	}

	// Summing only works while the cluster list is complete. It's capped at
	// ClusterLimit — far more cells than a screenful of ~60px cells ever
	// needs, so this is a safety net rather than a normal path — and a
	// truncated list would understate the total, so count properly instead.
	if len(clusters) >= ClusterLimit {
		total, err = s.count(ctx, b, filter)
		if err != nil {
			return Response{}, fmt.Errorf("points: count: %w", err)
		}
	}

	if total > IndividualLimit {
		return Response{Type: "clusters", Total: total, Clusters: clusters}, nil
	}

	// Sparse enough to draw every point individually. Still two queries in
	// this branch, same as before — but at this zoom the grid has roughly
	// one cell per point, so the aggregation it replaced was cheap.
	pts, err := s.queryPoints(ctx, b, filter)
	if err != nil {
		return Response{}, fmt.Errorf("points: query points: %w", err)
	}
	return Response{Type: "points", Total: total, Points: pts}, nil
}

func (s *Service) count(ctx context.Context, b BBox, filter Filter) (uint64, error) {
	where, args := filter.clause()
	q := fmt.Sprintf(
		"SELECT count() FROM %s WHERE %s AND %s AND %s",
		table, validCoords, bboxClause(b), where,
	)
	row := s.conn.QueryRow(ctx, q, args...)
	var n uint64
	if err := row.Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

func (s *Service) queryPoints(ctx context.Context, b BBox, filter Filter) ([]Point, error) {
	where, args := filter.clause()
	q := fmt.Sprintf(`SELECT
		assignment_id, nama_assignment, alamat, level_6_full_code, jenis_prelist_root,
		kode_penggunaan_bangunan_label,
		jumlah_usaha, nomor_bangunan, keberadaan_keluarga, keberadaan_BKU,
		assignment_status_alias,
		latitude_ppl, longitude_ppl,
		%s, %s
	FROM %s
	WHERE %s AND %s AND %s
	LIMIT %d`,
		dictHasExpr(matchDict, "assignment_id"), dictHasExpr(regsosekDict, "assignment_id"),
		table, validCoords, bboxClause(b), where, IndividualLimit)

	rows, err := s.conn.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	pts := make([]Point, 0, 512)
	for rows.Next() {
		var p Point
		// ClickHouse types an IN expression as UInt8, which the driver
		// won't scan straight into a bool — hence the two temporaries.
		var adaBaru, adaRegsosek uint8
		if err := rows.Scan(
			&p.AssignmentID, &p.Nama, &p.Alamat, &p.SubSLS, &p.JenisPrelist,
			&p.PenggunaanBangunan,
			&p.KeberadaanUsaha, &p.NomorBangunan, &p.KeberadaanKeluarga,
			&p.KeberadaanBKU, &p.Status,
			&p.Lat, &p.Lon, &adaBaru, &adaRegsosek,
		); err != nil {
			return nil, err
		}
		p.AdaAssignmentBaru = adaBaru == 1
		p.AdaRegsosek = adaRegsosek == 1
		pts = append(pts, p)
	}
	return pts, rows.Err()
}

// CellSizeForZoom returns a grid cell size, in degrees of longitude, tuned
// so each cluster covers roughly 60 screen pixels at that Leaflet zoom
// level (256px tiles, doubling every zoom level). Exported so the match
// map in internal/regsosek clusters at exactly the same scale as this one.
func CellSizeForZoom(zoom int) float64 {
	if zoom < 0 {
		zoom = 0
	}
	if zoom > 20 {
		zoom = 20
	}
	const targetPixels = 60.0
	return targetPixels * 360.0 / (256.0 * math.Pow(2, float64(zoom)))
}

func (s *Service) queryClusters(ctx context.Context, b BBox, zoom int, filter Filter) ([]Cluster, error) {
	cell := CellSizeForZoom(zoom)
	cellStr := fFloat(cell)

	where, args := filter.clause()
	q := fmt.Sprintf(`SELECT
		floor(latitude_ppl / %s) * %s + %s / 2 AS glat,
		floor(longitude_ppl / %s) * %s + %s / 2 AS glon,
		count() AS cnt
	FROM %s
	WHERE %s AND %s AND %s
	GROUP BY glat, glon
	ORDER BY cnt DESC
	LIMIT %d`, cellStr, cellStr, cellStr, cellStr, cellStr, cellStr, table, validCoords, bboxClause(b), where, ClusterLimit)

	rows, err := s.conn.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	clusters := make([]Cluster, 0, 512)
	for rows.Next() {
		var c Cluster
		if err := rows.Scan(&c.Lat, &c.Lon, &c.Count); err != nil {
			return nil, err
		}
		clusters = append(clusters, c)
	}
	return clusters, rows.Err()
}

// DatasetBounds computes the geographic extent and total row count of all
// valid-coordinate rows. Intended to be called once at startup (and cached)
// since it scans the whole table.
// FilteredBounds is the geographic extent of the rows matching filter,
// used to zoom the map onto a name search's results. Unlike DatasetBounds
// (computed once at startup and cached) this runs per request, so it is
// only called when the map actually needs it — see the Peta menu's
// applyFilters, which asks for it only while a search is active. Measured:
// ~100ms with a name search, against ~55ms unfiltered.
//
// Percentiles rather than min/max, matching the wilayah bounding boxes, so
// one row with a bad GPS fix can't stretch the view across the country.
// Total is 0 when nothing matches, and the caller must not fit to the
// extent in that case — the percentiles are NaN there.
func (s *Service) FilteredBounds(ctx context.Context, filter Filter) (Bounds, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	where, args := filter.clause()
	q := fmt.Sprintf(`SELECT
		quantile(0.01)(latitude_ppl), quantile(0.99)(latitude_ppl),
		quantile(0.01)(longitude_ppl), quantile(0.99)(longitude_ppl),
		count()
	FROM %s WHERE %s AND %s`, table, validCoords, where)

	row := s.conn.QueryRow(ctx, q, args...)
	var b Bounds
	if err := row.Scan(&b.MinLat, &b.MaxLat, &b.MinLon, &b.MaxLon, &b.Total); err != nil {
		return Bounds{}, err
	}
	return b, nil
}

func (s *Service) DatasetBounds(ctx context.Context) (Bounds, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	q := fmt.Sprintf(`SELECT
		min(latitude_ppl), max(latitude_ppl),
		min(longitude_ppl), max(longitude_ppl),
		count()
	FROM %s WHERE %s`, table, validCoords)

	row := s.conn.QueryRow(ctx, q)
	var b Bounds
	if err := row.Scan(&b.MinLat, &b.MaxLat, &b.MinLon, &b.MaxLon, &b.Total); err != nil {
		return Bounds{}, err
	}
	return b, nil
}

// KabKotaInfo describes one kabupaten/kota filter option: its code, a
// human name where known (see kabkotaNames), how many rows it has, and a
// bounding box to fit the map to when it's selected.
type KabKotaInfo struct {
	Code                           string `json:"code"`
	Name                           string `json:"name"`
	Total                          uint64 `json:"total"`
	MinLat, MaxLat, MinLon, MaxLon float64
}

type KabKotaInfoJSON struct {
	Code  string `json:"code"`
	Name  string `json:"name"`
	Total uint64 `json:"total"`
	// Nil when this wilayah has no plottable point at all — the four
	// are then omitted from the JSON entirely, and the frontend's boundsOf
	// falls back to the wilayah above it. A zero would be worse than
	// absent: it is a real coordinate off the coast of Africa.
	MinLat *float64 `json:"min_lat,omitempty"`
	MaxLat *float64 `json:"max_lat,omitempty"`
	MinLon *float64 `json:"min_lon,omitempty"`
	MaxLon *float64 `json:"max_lon,omitempty"`
}

// KabKotaList returns every kabupaten/kota actually present in the data,
// ordered by code ascending, each with a bounding box for the map to fit
// to when selected. The bbox uses the 1st/99th percentile of coordinates
// rather than true min/max so the occasional bad-GPS outlier doesn't blow
// up the zoom level.
func (s *Service) KabKotaList(ctx context.Context) ([]KabKotaInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	q := fmt.Sprintf(`SELECT
		substring(level_6_full_code, 1, 4) AS kabkota,
		count(),
		%s
	FROM %s
	WHERE length(level_6_full_code) >= 4
	GROUP BY kabkota
	ORDER BY kabkota ASC`, bboxIf, table)

	rows, err := s.conn.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := make([]KabKotaInfo, 0, 32)
	for rows.Next() {
		var k KabKotaInfo
		if err := rows.Scan(&k.Code, &k.Total, &k.MinLat, &k.MaxLat, &k.MinLon, &k.MaxLon); err != nil {
			return nil, err
		}
		k.Name = KabKotaName(k.Code)
		list = append(list, k)
	}
	return list, rows.Err()
}

// KecamatanInfo describes one kecamatan filter option within a
// kabupaten/kota: its 3-digit code, row count, and a bounding box (same
// 1st/99th-percentile approach as KabKotaInfo). ClickHouse itself has no
// name reference table for kecamatan — the API layer enriches the
// serialized KecamatanInfoJSON with a name from the optional PostGIS
// database when it's configured (see internal/mapdb.KecamatanNames); the
// frontend falls back to "Kec. <code>" when no name is available.
type KecamatanInfo struct {
	Code                           string `json:"code"`
	Total                          uint64 `json:"total"`
	MinLat, MaxLat, MinLon, MaxLon float64
}

// KecamatanInfoJSON is the API shape for a kecamatan option. Name is ""
// unless the server enriched it from the optional PostGIS database (see
// internal/mapdb.KecamatanNames) — the frontend falls back to showing
// Code alone when Name is blank.
type KecamatanInfoJSON struct {
	Code  string `json:"code"`
	Name  string `json:"name"`
	Total uint64 `json:"total"`
	// Nil when this wilayah has no plottable point at all — the four
	// are then omitted from the JSON entirely, and the frontend's boundsOf
	// falls back to the wilayah above it. A zero would be worse than
	// absent: it is a real coordinate off the coast of Africa.
	MinLat *float64 `json:"min_lat,omitempty"`
	MaxLat *float64 `json:"max_lat,omitempty"`
	MinLon *float64 `json:"min_lon,omitempty"`
	MaxLon *float64 `json:"max_lon,omitempty"`
}

// KecamatanList returns every kecamatan present within one kabupaten/kota,
// ordered by code ascending. kabkota must be a validated 4-digit code (see
// ParseKabKota) — this is always scoped to one kabupaten/kota since
// kecamatan codes repeat across them.
func (s *Service) KecamatanList(ctx context.Context, kabkota string) ([]KecamatanInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	q := fmt.Sprintf(`SELECT
		substring(level_6_full_code, 5, 3) AS kec,
		count(),
		%s
	FROM %s
	WHERE length(level_6_full_code) >= 7 AND substring(level_6_full_code, 1, 4) = '%s'
	GROUP BY kec
	ORDER BY kec ASC`, bboxIf, table, kabkota)

	rows, err := s.conn.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := make([]KecamatanInfo, 0, 32)
	for rows.Next() {
		var k KecamatanInfo
		if err := rows.Scan(&k.Code, &k.Total, &k.MinLat, &k.MaxLat, &k.MinLon, &k.MaxLon); err != nil {
			return nil, err
		}
		list = append(list, k)
	}
	return list, rows.Err()
}

// DesaInfo describes one desa/kelurahan filter option within a kecamatan:
// its 3-digit code, row count, and a bounding box (same 1st/99th-percentile
// approach as KabKotaInfo). Same name-enrichment story as KecamatanInfo —
// see DesaInfoJSON and internal/mapdb.DesaNames.
type DesaInfo struct {
	Code                           string `json:"code"`
	Total                          uint64 `json:"total"`
	MinLat, MaxLat, MinLon, MaxLon float64
}

// DesaInfoJSON is the API shape for a desa/kelurahan option. Name is ""
// unless the server enriched it from the optional PostGIS database.
type DesaInfoJSON struct {
	Code  string `json:"code"`
	Name  string `json:"name"`
	Total uint64 `json:"total"`
	// Nil when this wilayah has no plottable point at all — the four
	// are then omitted from the JSON entirely, and the frontend's boundsOf
	// falls back to the wilayah above it. A zero would be worse than
	// absent: it is a real coordinate off the coast of Africa.
	MinLat *float64 `json:"min_lat,omitempty"`
	MaxLat *float64 `json:"max_lat,omitempty"`
	MinLon *float64 `json:"min_lon,omitempty"`
	MaxLon *float64 `json:"max_lon,omitempty"`
}

// DesaList returns every desa/kelurahan present within one kabupaten/kota +
// kecamatan combination, ordered by code ascending. Both codes must
// already be validated (ParseKabKota / ParseKecamatan) — a desa code
// repeats across different kecamatan, so both parents are required to
// disambiguate it.
func (s *Service) DesaList(ctx context.Context, kabkota, kecamatan string) ([]DesaInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	q := fmt.Sprintf(`SELECT
		substring(level_6_full_code, 8, 3) AS desa,
		count(),
		%s
	FROM %s
	WHERE length(level_6_full_code) >= 10
		AND substring(level_6_full_code, 1, 4) = '%s'
		AND substring(level_6_full_code, 5, 3) = '%s'
	GROUP BY desa
	ORDER BY desa ASC`, bboxIf, table, kabkota, kecamatan)

	rows, err := s.conn.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := make([]DesaInfo, 0, 32)
	for rows.Next() {
		var d DesaInfo
		if err := rows.Scan(&d.Code, &d.Total, &d.MinLat, &d.MaxLat, &d.MinLon, &d.MaxLon); err != nil {
			return nil, err
		}
		list = append(list, d)
	}
	return list, rows.Err()
}

// SLSInfo describes one Kode SLS filter option within a desa/kelurahan:
// its 4-digit code, row count, and a bounding box (same 1st/99th-percentile
// approach as KabKotaInfo). ClickHouse holds no name for it, but the
// optional PostGIS table does (nmsls, e.g. "RT 051 DUSUN IV") — the API
// attaches it to SLSInfoJSON so the dropdown reads "SLS 0051 (RT 051)".
type SLSInfo struct {
	Code                           string `json:"code"`
	Total                          uint64 `json:"total"`
	MinLat, MaxLat, MinLon, MaxLon float64
}

type SLSInfoJSON struct {
	Code string `json:"code"`
	// Name is nmsls from PostGIS, or "" when PostGIS isn't configured or
	// has no name for this code — the frontend then shows the code alone,
	// exactly as before.
	Name  string `json:"name"`
	Total uint64 `json:"total"`
	// Nil when this wilayah has no plottable point at all — the four
	// are then omitted from the JSON entirely, and the frontend's boundsOf
	// falls back to the wilayah above it. A zero would be worse than
	// absent: it is a real coordinate off the coast of Africa.
	MinLat *float64 `json:"min_lat,omitempty"`
	MaxLat *float64 `json:"max_lat,omitempty"`
	MinLon *float64 `json:"min_lon,omitempty"`
	MaxLon *float64 `json:"max_lon,omitempty"`
}

// SLSList returns every Kode SLS present within one kabupaten/kota +
// kecamatan + desa/kelurahan combination, ordered by code ascending (the
// filter dropdown reads better sorted by code at this level than by
// count, unlike the coarser levels above it). All three codes must
// already be validated — an SLS code repeats across different
// desa/kelurahan, so all three parents are required to disambiguate it.
func (s *Service) SLSList(ctx context.Context, kabkota, kecamatan, desa string) ([]SLSInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	q := fmt.Sprintf(`SELECT
		substring(level_6_full_code, 11, 4) AS sls,
		count(),
		%s
	FROM %s
	WHERE length(level_6_full_code) >= 14
		AND substring(level_6_full_code, 1, 4) = '%s'
		AND substring(level_6_full_code, 5, 3) = '%s'
		AND substring(level_6_full_code, 8, 3) = '%s'
	GROUP BY sls
	ORDER BY sls ASC`, bboxIf, table, kabkota, kecamatan, desa)

	rows, err := s.conn.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := make([]SLSInfo, 0, 64)
	for rows.Next() {
		var sl SLSInfo
		if err := rows.Scan(&sl.Code, &sl.Total, &sl.MinLat, &sl.MaxLat, &sl.MinLon, &sl.MaxLon); err != nil {
			return nil, err
		}
		list = append(list, sl)
	}
	return list, rows.Err()
}

// SubSLSInfo describes one Kode SubSLS filter option within an SLS: its
// 2-digit code, row count, and a bounding box (same 1st/99th-percentile
// approach as KabKotaInfo). No name reference table exists for SubSLS
// either, so the code is the label — frontend renders it as "SubSLS <code>".
type SubSLSInfo struct {
	Code                           string `json:"code"`
	Total                          uint64 `json:"total"`
	MinLat, MaxLat, MinLon, MaxLon float64
}

type SubSLSInfoJSON struct {
	Code  string `json:"code"`
	Total uint64 `json:"total"`
	// Nil when this wilayah has no plottable point at all — the four
	// are then omitted from the JSON entirely, and the frontend's boundsOf
	// falls back to the wilayah above it. A zero would be worse than
	// absent: it is a real coordinate off the coast of Africa.
	MinLat *float64 `json:"min_lat,omitempty"`
	MaxLat *float64 `json:"max_lat,omitempty"`
	MinLon *float64 `json:"min_lon,omitempty"`
	MaxLon *float64 `json:"max_lon,omitempty"`
}

// SubSLSList returns every Kode SubSLS present within one kabupaten/kota +
// kecamatan + desa/kelurahan + SLS combination, ordered by code ascending
// (same reasoning as SLSList). All four codes must already be validated —
// a SubSLS code repeats across different SLS, so all four parents are
// required to disambiguate it.
func (s *Service) SubSLSList(ctx context.Context, kabkota, kecamatan, desa, sls string) ([]SubSLSInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	q := fmt.Sprintf(`SELECT
		substring(level_6_full_code, 15, 2) AS subsls,
		count(),
		%s
	FROM %s
	WHERE length(level_6_full_code) >= 16
		AND substring(level_6_full_code, 1, 4) = '%s'
		AND substring(level_6_full_code, 5, 3) = '%s'
		AND substring(level_6_full_code, 8, 3) = '%s'
		AND substring(level_6_full_code, 11, 4) = '%s'
	GROUP BY subsls
	ORDER BY subsls ASC`, bboxIf, table, kabkota, kecamatan, desa, sls)

	rows, err := s.conn.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := make([]SubSLSInfo, 0, 16)
	for rows.Next() {
		var sub SubSLSInfo
		if err := rows.Scan(&sub.Code, &sub.Total, &sub.MinLat, &sub.MaxLat, &sub.MinLon, &sub.MaxLon); err != nil {
			return nil, err
		}
		list = append(list, sub)
	}
	return list, rows.Err()
}

// --- paginated table listing (the "Daftar" menu) ----------------------------

const (
	DefaultPageSize = 50
	MaxPageSize     = 200
)

// SortDir is a column sort direction for List.
type SortDir string

const (
	SortAsc  SortDir = "asc"
	SortDesc SortDir = "desc"
)

// ParseSortDir validates a sort direction, defaulting to ascending for
// anything empty or unrecognized rather than erroring — the table always
// has to render some order, and "asc" is a harmless fallback.
func ParseSortDir(v string) SortDir {
	if strings.EqualFold(v, string(SortDesc)) {
		return SortDesc
	}
	return SortAsc
}

// SQL renders the direction as the ASC/DESC keyword for an ORDER BY.
// Exported because internal/regsosek builds its own list query with the
// same sort semantics.
func (d SortDir) SQL() string {
	if d == SortDesc {
		return "DESC"
	}
	return "ASC"
}

// listSortColumns maps the sortable column keys the Daftar table exposes
// (matching Point's JSON field names, so the frontend can pass back
// whatever column it rendered) to the ClickHouse column actually sorted
// on. A whitelist, not a passthrough — sortBy is user-controlled and goes
// straight into the SQL text via listSortColumns' value, never its key.
var listSortColumns = map[string]string{
	"nama":                "nama_assignment",
	"alamat":              "alamat",
	"subsls":              "level_6_full_code",
	"jenis_prelist":       "jenis_prelist_root",
	"keberadaan_usaha":    "jumlah_usaha",
	"nomor_bangunan":      "nomor_bangunan",
	"keberadaan_keluarga": "keberadaan_keluarga",
	"keberadaan_bku":      "keberadaan_BKU",
	"status":              "assignment_status_alias",
	"penggunaan_bangunan": "kode_penggunaan_bangunan_label",
	"assignment_id":       "assignment_id",

	// The three dictionary-backed columns. They are expressions rather than
	// stored columns, so listItems resolves them in the same SELECT the
	// ORDER BY applies to — see the comment there. Like every other value
	// in this map they are built from this package's own constants, never
	// from caller input.
	"ada_assignment_baru": dictHasExpr(matchDict, "assignment_id"),
	"ada_regsosek":        dictHasExpr(regsosekDict, "assignment_id"),
	"assignment_id_baru":  dictGetBaruExpr("assignment_id"),
}

// DefaultSortColumn is used by ParseSortColumn when sortBy is empty or not
// a recognized key.
const DefaultSortColumn = "nama"

// ParseSortColumn validates a sort column key against listSortColumns,
// defaulting to DefaultSortColumn for anything empty or unrecognized —
// same forgiving-default approach as ParseSortDir, since the Daftar table
// always has to render some order.
func ParseSortColumn(v string) string {
	if _, ok := listSortColumns[v]; ok {
		return v
	}
	return DefaultSortColumn
}

// ListPage is one page of the "Daftar" table: unlike Query, it is not
// viewport-bound — it lists every row matching filter (the whole table, if
// filter is empty), sorted and paginated for browsing rather than mapping.
type ListPage struct {
	Total    uint64  `json:"total"`
	Page     int     `json:"page"`
	PageSize int     `json:"page_size"`
	Items    []Point `json:"items"`
}

// List returns one page of rows matching filter, sorted by sortColumn (a
// key from listSortColumns — pass it through ParseSortColumn first). page
// is 1-indexed; pageSize is clamped to [1, MaxPageSize].
func (s *Service) List(ctx context.Context, filter Filter, page, pageSize int, sortColumn string, dir SortDir) (ListPage, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = DefaultPageSize
	}
	if pageSize > MaxPageSize {
		pageSize = MaxPageSize
	}

	total, err := s.listCount(ctx, filter)
	if err != nil {
		return ListPage{}, fmt.Errorf("points: list count: %w", err)
	}

	items, err := s.listItems(ctx, filter, page, pageSize, sortColumn, dir, false)
	if err != nil {
		return ListPage{}, fmt.Errorf("points: list items: %w", err)
	}

	return ListPage{Total: total, Page: page, PageSize: pageSize, Items: items}, nil
}

// ReportMaxRows caps how many rows ListAll will ever return. Reports are
// scoped to a desa/kelurahan or narrower (see prepareReport in
// internal/api); measured against live data, a desa runs ~700 rows at the
// median, ~18k at the 99th percentile and ~27k at the largest, so this
// clears every real desa with headroom while still bounding the damage if
// a much larger wilayah ever reaches ListAll. A report that does hit the
// cap says so in its header rather than truncating silently — see the
// truncated flag threaded through to pdfreport/xlsxreport.
const ReportMaxRows = 40000

// ListAll returns every row matching filter (up to ReportMaxRows), sorted
// by sortColumn, with no pagination. Intended for the PDF/Excel reports,
// which need the whole wilayah in one document rather than one page's
// worth — call sites should ensure filter is pinned at least down to a
// desa/kelurahan first, since anything wider is too big to be a report.
func (s *Service) ListAll(ctx context.Context, filter Filter, sortColumn string, dir SortDir) ([]Point, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	return s.listItems(ctx, filter, 1, ReportMaxRows, sortColumn, dir, true)
}

func (s *Service) listCount(ctx context.Context, filter Filter) (uint64, error) {
	where, args := filter.clause()
	q := fmt.Sprintf("SELECT count() FROM %s WHERE %s", table, where)
	row := s.conn.QueryRow(ctx, q, args...)
	var n uint64
	if err := row.Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// listItems fetches one page of rows. includeCatatan adds the catatan
// column to both the SELECT and the scan target — only ListAll (the
// report path) passes true; List (the Daftar table) leaves Point.Catatan
// unset, since that column isn't meant for on-screen browsing (see the
// field's doc comment on Point).
func (s *Service) listItems(ctx context.Context, filter Filter, page, pageSize int, sortColumn string, dir SortDir, includeCatatan bool) ([]Point, error) {
	offset := (page - 1) * pageSize
	sortCol, ok := listSortColumns[sortColumn]
	if !ok {
		sortCol = listSortColumns[DefaultSortColumn]
	}
	cols := "assignment_id, nama_assignment, alamat, level_6_full_code, jenis_prelist_root, kode_penggunaan_bangunan_label, jumlah_usaha, nomor_bangunan, keberadaan_keluarga, keberadaan_BKU, assignment_status_alias, latitude_ppl, longitude_ppl"
	if includeCatatan {
		cols += ", catatan"
	}
	// Three dictionary lookups, in the same SELECT the ORDER BY applies to
	// so they sort exactly like every other column. They cost almost
	// nothing: this page query measures 21ms on the largest desa against
	// 19ms without these columns at all, and read_rows stays at the left
	// side's own 57,344 — the dictionaries are already in memory, so
	// neither source table is touched.
	cols += fmt.Sprintf(", %s, %s, %s",
		dictHasExpr(matchDict, "assignment_id"),
		dictHasExpr(regsosekDict, "assignment_id"),
		dictGetBaruExpr("assignment_id"))
	where, args := filter.clause()
	q := fmt.Sprintf(`SELECT
		%s
	FROM %s
	WHERE %s
	ORDER BY %s %s
	LIMIT %d OFFSET %d`, cols, table, where, sortCol, dir.SQL(), pageSize, offset)

	rows, err := s.conn.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]Point, 0, pageSize)
	for rows.Next() {
		var p Point
		dest := []any{
			&p.AssignmentID, &p.Nama, &p.Alamat, &p.SubSLS, &p.JenisPrelist,
			&p.PenggunaanBangunan,
			&p.KeberadaanUsaha, &p.NomorBangunan, &p.KeberadaanKeluarga,
			&p.KeberadaanBKU, &p.Status,
			&p.Lat, &p.Lon,
		}
		if includeCatatan {
			dest = append(dest, &p.Catatan)
		}
		// UInt8 in ClickHouse, bool in Point — same as queryPoints.
		// dictHas returns UInt8, so a miss scans as 0.
		var adaBaru, adaRegsosek uint8
		dest = append(dest, &adaBaru, &adaRegsosek, &p.AssignmentIDBaru)
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		p.AdaAssignmentBaru = adaBaru == 1
		p.AdaRegsosek = adaRegsosek == 1
		items = append(items, p)
	}
	return items, rows.Err()
}
