# Peta Titik SE2026

Web map (Go + Leaflet/OpenStreetMap) yang menampilkan seluruh titik pada
tabel ClickHouse `dtsen.se2026_titik2` (jutaan baris) secara seamless, tanpa
mengirim seluruh dataset ke browser sekaligus.

## Cara kerja (ringkas)

Server tidak pernah mengirim >3000 marker dalam satu response. Setiap kali
peta digeser/di-zoom, frontend mengirim viewport (bbox) + level zoom ke
`GET /api/points`:

- Kalau jumlah titik di viewport itu ≤ 3000 → dikirim **individual**,
  lengkap dengan data untuk tooltip.
- Kalau lebih banyak → dikirim sebagai **cluster grid** (lat/lon dibulatkan
  ke sel grid yang ukurannya menyesuaikan level zoom, dengan `count()` per
  sel). Klik cluster untuk zoom ke area itu.

Request lama otomatis di-abort (AbortController) saat user menggeser peta
lagi sebelum response sebelumnya selesai, jadi tidak ada race condition /
flicker data lama menimpa data baru.

Total & extent dataset (untuk auto-fit peta saat awal buka) dihitung sekali
saat startup dan di-refresh otomatis setiap 10 menit di background, supaya
angka tetap akurat seiring data bertambah tanpa perlu restart server.

### Catatan performa (hasil pengukuran, bukan perkiraan)

Tabelnya `ORDER BY (level_6_full_code, assignment_id)` tanpa partisi dan
tanpa skip index — 2,17 juta baris / 238 MiB / 269 granul. Lima hal berikut
sudah diterapkan dan diukur di data itu:

1. **Filter wilayah pakai `startsWith`, bukan `substring`.** Karena kelima
   level wilayah hierarkis (lihat `Validate`), yang terisi selalu membentuk
   *prefix* dari `level_6_full_code` — dan itu kolom pertama sort key.
   ClickHouse menerjemahkan `startsWith(level_6_full_code,'6471030')` jadi
   rentang primary key `['6471030','6471031')` sehingga bisa melompati
   granul; bentuk `substring(...)=...` tidak terbaca index dan memaksa full
   scan. Lihat `Filter.clause` di `internal/points/points.go`.

   | query | `substring()` | `startsWith()` |
   |---|---|---|
   | cluster + filter kabupaten | 2.168.304 baris | 417.792 baris |
   | satu halaman Daftar + filter kecamatan | 2.184.688 baris | 131.072 baris |

2. **Total viewport dihitung dari hasil cluster, bukan query terpisah.**
   Dulu setiap perubahan viewport menjalankan `count()` lalu query
   cluster/points — dua query membaca baris yang **sama persis**. Sekarang
   agregasi grid dijalankan lebih dulu dan totalnya didapat dari
   menjumlahkan `count()` per sel, jadi viewport padat cukup satu query.
   Kalau daftar cluster kena `ClusterLimit` (mustahil di pemakaian normal,
   tapi mungkin di zoom sangat tinggi + bbox sangat luas) penjumlahan itu
   tidak sahih, jadi kodenya jatuh balik ke `count()` sungguhan — lihat
   `Service.Query`. Terukur: viewport padat ~74ms → **~38ms**.

3. **Response di-gzip** (`withGzip` di `internal/api/middleware.go`).
   Diukur pada response `/api/points` berisi 942 titik: 285KB → 51KB
   (**5,8x**); aset statis 3,3–3,8x (leaflet.js 147KB → 43KB). Unduhan PDF
   dan Excel sengaja **tidak** ikut dikompres — `.xlsx` itu zip dan stream
   PDF sudah ter-deflate, jadi mengompres ulang cuma buang CPU.

4. **Debounce peta 250ms → 120ms** (`LOAD_DEBOUNCE_MS` di
   `web/static/app.js` dan `match-map.js`). Diukur dari browser, query
   viewport-nya sendiri cuma 22ms untuk titik individual dan 76ms untuk
   cluster se-provinsi — jadi di angka lama penantian ini **~4x lebih lama
   daripada kerja yang ditunggunya**, dan jadi penyumbang terbesar rasa
   lambat peta. Total yang dirasakan setelah berhenti menggeser: ~315ms →
   ~195ms. 120ms masih cukup untuk menelan rentetan event `moveend`/
   `zoomend` dari satu gerakan drag atau scroll, dan request yang tersusul
   tetap dibatalkan `AbortController`.

5. **Ketiga kolom dari `se2026_match`/`se2026_match_regsosek` lewat
   dictionary ClickHouse**, bukan `IN`-subquery atau `JOIN` — DDL-nya ada
   di `sql/dictionaries.sql`. Sebelumnya setiap query membaca ulang tabel
   sumbernya (89rb + 167rb baris), dan itu biaya terbesar di seluruh
   lapisan database: 43ms dari 80ms satu halaman Daftar, dan 136ms dari
   195ms cluster peta yang difilter flag.

   | satu halaman Daftar, desa terbesar | waktu | baris dibaca |
   |---|---|---|
   | `JOIN` + 2× `IN` penuh | 112ms | 313.288 |
   | dictionary | **21ms** | **57.344** |
   | pembanding: tanpa ketiga kolom itu sama sekali | 19ms | 57.344 |

   Jadi ketiga kolom itu sekarang praktis gratis, dan `read_rows` kembali
   ke sisi kiri saja — tabel sumbernya tidak disentuh lagi saat query.
   Diukur dari browser, satu halaman Daftar di desa terbesar 100ms → 39ms,
   dan tampilan awal tanpa filter 138ms → 84ms.

   Diverifikasi setara sebelum diganti, bukan diasumsikan: `dictHas` vs
   `IN` dan `dictGetString` vs `LEFT JOIN` dibandingkan di **seluruh
   2.168.304 baris** — 0 flag berbeda dan 0 nilai `assignment_id_baru`
   berbeda — lalu lewat aplikasi dicek lagi di 6 kedalaman filter (tanpa
   filter s/d SubSLS) terhadap `IN` penuh, semuanya sama persis. Baris
   berkunci ganda tetap menampilkan kedua nilainya.

   Yang **tidak** membaik: cluster peta berfilter flag tanpa filter wilayah
   (2,17 juta baris kiri) tetap ~255ms — di situ `dictHas` seri dengan `IN`
   (190ms vs 193ms di sisi ClickHouse), karena lookup per baris menyaingi
   satu kali bangun himpunan. Dictionary tidak pernah lebih lambat, tapi
   untungnya baru terasa begitu ada filter wilayah.

   **Konsekuensi operasional:** kedua dictionary itu sekarang dependensi
   keras — kalau tidak ada, `/api/list` dan `/api/points` gagal total, bukan
   sekadar melambat. Karena itu `Service.CheckDictionaries` dijalankan saat
   startup dan menulis log `level=ERROR` yang menyebut nama dictionary,
   tabel sumbernya, dan `sql/dictionaries.sql` kalau salah satu tidak bisa
   dipakai (dicatat, bukan fatal, supaya ClickHouse yang sedang down tidak
   menyebabkan restart loop). `LIFETIME(MIN 300 MAX 600)` berarti flag bisa
   tertinggal maksimal 10 menit dari tabel sumber — cukup karena tabel itu
   diisi per batch.

**Yang sudah diukur dan sengaja TIDAK dikerjakan:** menjalankan `count()` dan
query halaman secara paralel di `Service.List` (keduanya sekarang berurutan).
`count()` cuma 2ms tanpa filter — ClickHouse menjawabnya dari metadata,
`read_rows=1` — dan 6ms di level desa, jadi konkurensi cuma menghemat
beberapa milidetik. Diukur dulu, baru diputuskan tidak dikerjakan.

**Yang sudah dicoba dan sengaja TIDAK dipakai:** minmax skip index di
`(latitude_ppl, longitude_ppl)`. Hipotesisnya karena data terurut kode
wilayah, tiap granul mestinya punya kotak lat/lon yang rapat sehingga bbox
viewport bisa melompati banyak granul. Diuji langsung: index memang terpakai
(78 dari 266 granul), **tapi hasilnya sedikit lebih buruk** — 598.016 →
638.976 baris terbaca, ~50ms → ~66ms, karena granul terpilih dibaca penuh
sementara PREWHERE di lat/lon sebelumnya sudah menyaring lebih hemat.
Index-nya sudah di-`DROP` lagi. Jangan ditambahkan ulang tanpa mengukur.

### Tombol "Terapkan Filter"

Baik di menu Peta maupun Daftar, mengubah dropdown wilayah, dropdown atribut
(Jenis Prelist/Keberadaan Keluarga/Status di Daftar), atau kotak cari nama
**tidak langsung memuat ulang data** — itu baru terjadi begitu tombol
"Terapkan Filter" diklik (di Daftar, menekan Enter di kotak cari juga
sama saja dengan mengklik tombolnya). Ini supaya ganti beberapa filter
sekaligus (misalnya kabupaten/kota lalu kecamatan lalu status) tidak memicu
satu request per klik — cukup satu request begitu semua filter yang
diinginkan sudah dipilih.

Yang TETAP langsung terjadi tanpa menunggu tombol (karena ini soal mengisi
pilihan dropdown, bukan soal memuat data titik/tabelnya): memilih
kabupaten/kota tetap langsung mengisi daftar kecamatan-nya, memilih
kecamatan tetap langsung mengisi daftar desa/kelurahan, dan seterusnya —
cuma peta/tabelnya sendiri yang menunggu. Di menu Peta, auto-zoom ke area
yang difilter juga baru terjadi saat tombol diklik (bukan saat dropdown
diubah); di Daftar, tombol Unduh PDF/Excel juga selalu mengikuti filter
yang **sudah diterapkan** (bukan yang baru dipilih di dropdown tapi belum
diklik Terapkan Filter). Lihat `appliedKabkota` dkk. di `web/static/app.js`
dan `web/static/daftar.js` untuk detail pemisahan "state dropdown yang
sedang dipilih" vs "filter yang benar-benar aktif".

Menggeser/zoom peta secara manual tetap langsung memuat ulang titik di
viewport yang baru (itu navigasi peta biasa, bukan mengubah filter) —
yang ditahan tombol Terapkan Filter cuma perubahan filter wilayah/atribut/
cari nama itu sendiri.

### Filter kabupaten/kota

Dropdown "Kabupaten/Kota" di panel diisi otomatis dari data yang ada (4
digit pertama `level_6_full_code`, kode wilayah BPS/Kemendagri provinsi+
kabupaten/kota), bukan daftar statis — jadi kalau cakupan data bertambah ke
kabupaten/kota lain, opsinya otomatis muncul (nama tampil kalau kodenya ada
di `internal/points/kabkota_names.go`, kalau belum ada di map itu tetap
berfungsi sebagai filter, hanya labelnya berupa kode mentah). Begitu tombol
"Terapkan Filter" diklik, kab/kota yang dipilih otomatis men-zoom peta ke
area itu (bounding box dihitung pakai persentil 1–99% supaya titik dengan
GPS salah/outlier tidak merusak zoom) dan membatasi semua query berikutnya
(`GET /api/points?...&kabkota=6472`).

Dropdown "Kecamatan" di sebelahnya mengikuti (cascading): nonaktif sampai
sebuah kabupaten/kota dipilih, lalu terisi otomatis dari 3 digit berikutnya
di `level_6_full_code` (`GET /api/kecamatan?kabkota=6472`). ClickHouse
sendiri tidak punya tabel referensi nama kecamatan, tapi kalau database
PostGIS opsional (`MAP_*`, lihat bagian "Polygon batas SubSLS & nama
kecamatan/desa" di bawah) tersedia, server melengkapi labelnya dengan nama
asli (mis. "Kec. Sungai Pinang") — kalau tidak, tetap jadi kode apa adanya
("Kec. 061"). Kecamatan tanpa kabupaten/kota ditolak backend (400) karena
kodenya hanya unik di dalam satu kabupaten/kota.

Dropdown "Desa/Kelurahan" mengikuti pola yang sama satu tingkat lagi:
nonaktif sampai kecamatan dipilih, terisi dari 3 digit berikutnya
(`GET /api/desa?kabkota=6472&kecamatan=061`), label juga dilengkapi nama
dari PostGIS kalau tersedia (kalau tidak, "Desa/Kel. 003"), dan ditolak
backend (400) kalau dikirim tanpa kecamatan.

Dropdown "Kode SLS" satu tingkat lagi: nonaktif sampai desa/kelurahan
dipilih, terisi dari 4 digit berikutnya
(`GET /api/sls?kabkota=6472&kecamatan=061&desa=003`), dan ditolak backend
(400) kalau dikirim tanpa desa/kelurahan. Labelnya menampilkan **kode
beserta nama SLS dalam kurung** — "SLS 0001 (RT 001, 170 titik)" — dengan
nama diambil dari kolom `nmsls` di PostGIS (`mapdb.SLSNames`). Berbeda dari
kecamatan/desa yang namanya *menggantikan* kode, di sini kodenya
dipertahankan karena kode SLS itu bagian dari `level_6_full_code` yang
memang dipakai orang. Kalau PostGIS tidak dikonfigurasi atau kodenya tidak
punya nama, labelnya kembali jadi kode saja seperti sebelumnya.

*Catatan:* nama itu milik SLS, bukan SubSLS — diukur di tabel PostGIS,
14.331 dari 14.332 SLS punya tepat satu `nmsls` untuk seluruh SubSLS-nya.
Karena itu namanya dipasang di dropdown Kode SLS (tempat ia membedakan antar
opsi) dan bukan di Kode SubSLS (tempat ia akan berulang sama persis).

Dropdown "Kode SubSLS" adalah level terakhir: nonaktif sampai SLS dipilih,
terisi dari 2 digit terakhir `level_6_full_code`
(`GET /api/subsls?kabkota=6472&kecamatan=061&desa=003&sls=0070`), label
berupa kode ("SubSLS 01"), dan ditolak backend (400) kalau dikirim tanpa
SLS. Dengan ini seluruh 16 digit `level_6_full_code` sudah tercakup sebagai
filter (kabkota 4 + kecamatan 3 + desa 3 + SLS 4 + SubSLS 2).

Reset di level manapun otomatis mengosongkan & men-disable semua level di
bawahnya (ganti kabupaten/kota → kecamatan, desa, SLS, dan SubSLS ikut
ter-reset).

### Jenis peta (OpenStreetMap / Satelit)

Kontrol layer di pojok kiri bawah peta memilih basemap: "Peta Jalan
(OpenStreetMap)" atau "Satelit (Esri)". Satelit pakai **Esri World
Imagery** — tile publik gratis, tidak perlu API key/signup, cukup atribusi
(sudah otomatis ditampilkan di pojok kanan bawah saat basemap ini aktif).
Ini bukan citra terbaru real-time; resolusi & tanggal potretnya bervariasi
per lokasi (detail sampai < 1 tahun untuk kota besar di Esri, tapi bisa umur
beberapa tahun di area terpencil), sama seperti wajarnya layanan satelit
gratis lain.

Kalau nanti perlu sumber lain:
- **EOX Sentinel-2 cloudless** — gratis, resolusi lebih rendah (~10m) tapi
  komposit awan-minim, atribusi wajib, tanpa API key.
- **Mapbox Satellite** — kualitas tinggi, tapi butuh signup + API key
  (gratis sampai ~50.000 pemuatan peta/bulan, berbayar di atas itu).

### Polygon batas SubSLS & nama kecamatan/desa (PostGIS, opsional)

Begitu filter wilayah di menu Peta didrill sampai Kode SubSLS, peta
menggambar garis batas (poligon) area SubSLS itu (merah, semi-transparan)
di atas titik-titiknya — otomatis hilang lagi kalau filter naik ke level
manapun di atasnya. Filter Kecamatan dan Desa/Kelurahan (di menu Peta
maupun Daftar) juga menampilkan **nama wilayahnya**, bukan cuma kodenya
(mis. "Kec. Sungai Pinang" alih-alih "Kec. 061") — ClickHouse sendiri tidak
punya tabel nama untuk kedua level ini, sama seperti dulu, jadi dilengkapi
dari sumber yang sama.

Datanya bukan dari ClickHouse, tapi dari database **PostgreSQL/PostGIS
terpisah**, dikonfigurasi lewat variabel `MAP_*` di `.env` (lihat
`example.env`). Fitur ini **opsional** — kalau `MAP_HOST` kosong/tidak
diisi, atau Postgres-nya tidak bisa dihubungi saat startup, server tetap
jalan normal tanpa polygon dan dropdown kecamatan/desa kembali menampilkan
kode saja (cuma di-log sebagai warning, tidak menghentikan server — lihat
`run()` di `main.go`). Tabel sumbernya `peta_sls_6400_rev`: `idsubsls`
formatnya persis sama dengan `level_6_full_code` di ClickHouse (16 digit),
`wkb_geometry` geometrinya (SRID 4326 / WGS84, langsung dipakai Leaflet
tanpa reproyeksi), `kdkec`/`nmkec` dan `kddesa`/`nmdesa` sumber nama
kecamatan/desa. Lihat `internal/mapdb/`.

### Menu Daftar

Selain menu "Peta", ada menu "Daftar" (nav di paling atas) yang menampilkan
isi tabel `se2026_titik2` sebagai daftar biasa — bukan tampilan peta:

- **Sort by kolom apa saja** — klik header kolom mana pun (Nama, Alamat, ID
  SUBSLS, Jenis Prelist, Nomor Bangunan, Keberadaan Keluarga, Status,
  Assignment ID, Ditemukan di Assignment Baru, Assignment ID Baru,
  Ditemukan di Regsosek) untuk mengurutkan tabel berdasarkan kolom itu; klik lagi
  kolom yang sama untuk membalik arah (naik/turun), klik kolom lain untuk
  pindah kolom urut (default naik). Panah kecil di header cuma muncul di
  kolom yang sedang aktif. Nama kolom di query string (`sortBy=nama`,
  `sortBy=status`, dst.) divalidasi terhadap whitelist
  `listSortColumns` di `internal/points/points.go` sebelum dipakai jadi SQL
  — nilai yang tidak dikenal jatuh balik ke default (`nama`), bukan error.
  Baris dengan nilai kosong di kolom yang diurutkan ikut tampil apa adanya
  (bukan disembunyikan), karena ini daftar data mentah, bukan cuma titik
  yang siap dipetakan.
- **Cari nama** — kotak teks di atas filter wilayah, mencari substring
  (tidak case-sensitive) di kolom `nama_assignment`, digabung dengan filter
  lain lewat `AND` (semuanya harus cocok). Sama seperti filter lain di menu
  ini, ketikan baru diterapkan begitu tombol "Terapkan Filter" diklik (atau
  tekan Enter di kotak ini) — lihat bagian "Tombol Terapkan Filter" di atas.
  Berbeda dari filter lain di menu ini,
  teks pencarian adalah input bebas dari pengguna — jadi tidak divalidasi
  terhadap whitelist, tapi dikirim ke ClickHouse lewat parameter binding
  (`?` placeholder, lihat `Filter.clause()` di
  `internal/points/points.go`), bukan digabung langsung ke teks SQL, supaya
  aman dari SQL injection tanpa perlu escaping manual.
- **Paginasi** lewat `LIMIT`/`OFFSET` di ClickHouse — bukan infinite
  scroll, supaya jumlah data yang ditransfer & di-render tiap saat tetap
  kecil. Ukuran halaman bisa dipilih 25/50/100/200 baris.
- **Filter wilayah** kabupaten/kota → kecamatan → desa/kelurahan → SLS →
  SubSLS, cascading persis seperti di menu Peta, tapi state-nya independen
  (pilih filter di satu menu tidak mengubah filter di menu lainnya) dan
  tidak ada auto-zoom (karena tidak ada peta di sini).
- **Filter atribut** — Jenis Prelist, Keberadaan Keluarga, Status: tiga
  dropdown independen (tidak nge-cascade dan tidak saling bergantung, juga
  tidak bergantung pada filter wilayah). Pilihannya adalah enum tertutup
  dari `internal/points/points.go` (`jenisPrelistValues` dkk.), diserve ke
  frontend lewat `GET /api/filter-options` — jadi frontend tidak perlu
  hardcode daftar nilainya sendiri. Ada opsi "(Kosong)" di tiap dropdown
  untuk memfilter baris yang kolomnya benar-benar kosong (`__EMPTY__` di
  query string — beda dari filter tidak diisi sama sekali, lihat
  `points.EmptyValue`).
- **Tidak difilter oleh validitas koordinat** — beda dari menu Peta, daftar
  ini menampilkan semua baris yang cocok filter, termasuk yang
  `latitude_ppl`/`longitude_ppl`-nya `0` atau kosong, karena tujuannya
  menelusuri data, bukan memetakannya.
- **Unduh PDF & Unduh Excel** — dua tombol di sebelah filter, aktif begitu
  filter wilayah sudah **minimal sampai Desa/Kelurahan**. Boleh dipersempit
  lagi ke Kode SLS atau Kode SubSLS — itu cuma bikin laporannya lebih
  kecil, bukan syarat. Yang tidak boleh cuma lebih luas dari desa: satu
  kecamatan saja sudah ratusan ribu baris, bukan lagi sesuatu yang masuk
  akal jadi satu laporan (aturan ini ditegakkan di server oleh
  `prepareReport` di `internal/api/server.go`, bukan cuma oleh tombol yang
  di-disable di frontend). Filter atribut dan cari nama boleh diisi atau
  tidak, tidak mempengaruhi aktif/tidaknya tombol, tapi tetap ikut
  mempersempit isi laporan kalau diisi, dan urutan barisnya ikut sort kolom
  yang sedang aktif di tabel. Keduanya laporan yang sama persis secara isi
  ("Daftar Hasil Pendataan": keterangan wilayah — nama kab/kota + kode tiap
  level + kode wilayah gabungan; level yang tidak difilter ditulis
  "(Semua)" —, bagian "Filter Tambahan" kalau ada filter atribut atau cari
  nama yang aktif, lalu semua baris yang cocok, bukan cuma satu halaman
  tabel — lihat `ListAll` di `internal/points/points.go`), cuma beda
  format:
  - **PDF** — buat dicetak/dibaca. Kolom `assignment_id` sengaja tidak
    disertakan (bukan info yang relevan untuk dicetak). Kolom ID SUBSLS per
    baris **muncul atau tidak tergantung cakupan**: kalau laporannya tepat
    satu SubSLS, nilainya sama untuk semua baris jadi cukup ditulis sekali
    di keterangan wilayah; kalau cakupannya lebih luas (satu desa atau satu
    SLS), nilainya beda-beda antar baris jadi harus jadi kolom sendiri —
    lihat `columnsFor`/`rowFor` di `internal/pdfreport/`. Digenerate pakai
    `github.com/go-pdf/fpdf` (pure Go, tanpa Chrome/wkhtmltopdf).
  - **Excel (.xlsx)** — buat diolah lebih lanjut (disortir, difilter,
    dicocokkan dengan data lain), jadi kolom `assignment_id` dan ID SUBSLS
    per baris selalu disertakan di sini (tidak kondisional seperti di PDF),
    plus header tabelnya di-freeze dan dikasih AutoFilter bawaan Excel.
    Digenerate pakai `github.com/xuri/excelize/v2` lewat
    `internal/xlsxreport/`.

  **Batas jumlah baris** — `ReportMaxRows` di `internal/points/points.go`
  (sekarang 40.000). Diukur dari data live, satu desa/kelurahan berisi
  ±700 baris di median, ±18.000 di persentil 99, dan 27.146 di yang
  terbesar — jadi angka itu memuat semua desa yang ada dengan sisa ruang,
  sambil tetap membatasi kerusakan kalau suatu saat wilayah yang jauh lebih
  besar lolos ke `ListAll`. Laporan yang benar-benar kena batas ini
  menuliskannya di header ("Catatan: daftar ini dibatasi hingga N baris
  pertama"), bukan memotong diam-diam. Sebagai gambaran beban: desa
  terbesar (27.146 baris) menghasilkan PDF ±5,4 MB dan Excel ±2,2 MB,
  masing-masing ±2 detik.

  Kedua paket format ini berbagi metadata wilayah/filter yang sama lewat
  `report.Region` di `internal/report/` (satu sumber kebenaran soal apa
  yang ditampilkan di "Keterangan Wilayah" dan "Filter Tambahan"), supaya
  PDF dan Excel tidak bisa "berbeda cerita" soal filter apa yang lagi
  aktif.

Endpoint-nya `GET /api/list` (lihat bagian Endpoint di bawah). Query
`ORDER BY ... LIMIT ... OFFSET ...` tanpa filter di atas 4 juta baris
diukur ~1–3 detik di ClickHouse (lihat `internal/points/points.go`) — masih
wajar untuk tampilan tabel yang dilihat manusia, tapi kalau nanti data jauh
lebih besar dan halaman-halaman terakhir (offset sangat dalam) terasa
lambat, itu batasan `LIMIT/OFFSET` yang umum di database kolom manapun;
solusinya persempit dulu pakai filter wilayah sebelum menjelajahi halaman
jauh.

### Menu Daftar Match Regsosek

Menu ketiga (`/reg2022`) menampilkan tabel `dtsen.se2026_match_regsosek` —
keluarga SE2026 yang statusnya tidak ditemukan saat pendataan, tapi
ketemu di data Regsosek 2022. Keterangan itu juga dicetak di layar, di
PDF, dan di Excel supaya salinan yang beredar bisa berdiri sendiri.
166.507 baris / 32 MiB, tabel terpisah dengan kolom sendiri, jadi ditangani
paket sendiri (`internal/regsosek/`) alih-alih digabung ke `internal/points`.

Kolom yang ditampilkan (label UI di kiri, kolom sumber di kanan kalau beda):

| Kolom di layar | Kolom database |
|---|---|
| Nama | `nama_prelist` |
| Nama KK | `nama_kk` |
| ID SubSLS | `level_6_full_code` |
| Match Status | `match_status` |
| Alamat Regsosek | `alamat_gabung_regsosek` |
| Nama Matched Regsosek | `nama_matched_regsosek` |
| Assignment ID | `assignment_id` |

**`no_kk`, `nik_kk` dan `nik_matched_regsosek` sengaja tidak ada di daftar
itu, dan tidak ada di mana pun.** Ketiganya nomor identitas pribadi, jadi
bukan cuma disembunyikan dari tampilan — kolomnya dibuang dari `SELECT`
(lihat `rowColumns` di `internal/regsosek/regsosek.go`), sehingga nomornya
tidak pernah sampai ke browser, tidak ikut ke unduhan PDF/Excel, tidak bisa
diurutkan, dan tidak bisa dipakai sebagai kunci pencarian. `nama_kk` tetap
ada karena itu nama, bukan nomor.

Semua kolom itu bisa diklik untuk sort (naik/turun), sama seperti menu
Daftar.

**Filter:**

- **Wilayah** kabupaten/kota → kecamatan → desa/kelurahan → SLS → SubSLS,
  cascading persis seperti menu lain. Dropdown-nya sengaja memakai ulang
  endpoint `/api/kecamatan`, `/api/desa`, dst. yang sudah ada, bukan bikin
  baru: tabel ini pakai `level_6_full_code` dengan format 16 digit yang
  sama, dan dicek langsung ke data — 10 kabupaten/kota-nya sama persis dan
  **tidak ada satu pun desa yang cuma ada di sini** tapi tidak ada di
  `se2026_titik2`. Konsekuensinya, angka "(N titik)" pada label dropdown
  berasal dari tabel titik, bukan dari tabel ini.
- **Match Status** — dropdown dari 7 nilai yang benar-benar ada di data
  (`matchStatusValues` di `internal/regsosek/regsosek.go`), diserve lewat
  `GET /api/reg2022/filter-options`. Ada opsi "(Kosong)" (`__EMPTY__`)
  seperti filter atribut di menu Daftar.
- **Cari** — satu kotak yang mencari substring (tidak case-sensitive) di
  **dua** kolom nama sekaligus: `nama_prelist` dan `nama_kk`.
  Nomor KK/NIK sengaja bukan kunci cari (lihat catatan di atas). Teksnya dikirim
  lewat parameter binding (satu bind per kolom), bukan digabung ke teks SQL.

Sama seperti menu Daftar, semua filter itu baru berlaku begitu tombol
**Terapkan Filter** diklik (atau Enter di kotak cari) — lihat bagian
"Tombol Terapkan Filter" di atas.

**Kolom Match Status di-highlight**: kolomnya diberi latar berbeda dan tiap
nilai tampil sebagai badge berwarna menurut kekuatan pemadanan — hijau
untuk kecocokan NIK persis, biru untuk kecocokan nama dalam satu SLS, dan
makin hangat (oranye ke merah) seiring bertambahnya digit NIK yang salah
ketik. Lihat `MATCH_COLORS` di `web/static/reg2022.js`.

Kolom `ada_keluarga_label` sempat ditampilkan lalu dihapus: isinya cuma
satu nilai untuk seluruh 166.507 baris ("0. Tidak Ditemukan (STOP)"), jadi
tidak membedakan apa pun.

**Catatan performa:** tabel ini `ORDER BY assignment_id`, bukan
`level_6_full_code`, jadi filter wilayahnya **tidak** kena primary index
(beda dengan `se2026_titik2`, lihat "Catatan performa" di atas) dan selalu
full scan. Di 166 ribu baris / 32 MiB itu cuma beberapa milidetik, jadi
dibiarkan — kodenya tetap ditulis pakai `startsWith` supaya konsisten dan
otomatis ikut cepat kalau tabelnya suatu saat di-sort ulang.


**Unduh PDF & Excel** — dua tombol di baris aksi, sama seperti menu Daftar,
tapi **syaratnya minimal Kecamatan, bukan Desa/Kelurahan**. Alasannya
diukur dari data: tabel ini jauh lebih kecil per wilayah daripada tabel
titik — kecamatan terbesar di sini berisi 7.431 baris (median 970),
sementara desa terbesar di `se2026_titik2` berisi 27.146 — jadi memaksa
level desa (median 57 baris) akan menghasilkan laporan yang terlalu tipis
tanpa alasan. Aturannya ditegakkan di server (`prepareRegsosekReport` di
`internal/api/server.go`), bukan cuma oleh tombol yang di-disable.

Isi kedua format sama, dengan dua beda yang disengaja seperti pada laporan
Daftar: PDF membuang kolom Assignment ID (bukan info yang dibaca dari
kertas) sementara Excel menyertakannya, dan lebar yang tersisa di PDF
dibagi ke kolom nama.
Keduanya memakai ulang blok "Keterangan Wilayah"/"Filter Tambahan" yang
sama lewat `report.Region` — `match_status` menempati slot Status di blok
itu, jadi tidak perlu menambah field yang cuma dipakai satu laporan.
Digenerate lewat `internal/pdfreport/regsosek.go` dan
`internal/xlsxreport/regsosek.go`, yang memakai ulang helper tata letak
paket masing-masing sehingga pemenggalan halaman, pembungkusan baris, dan
gaya tabelnya identik dengan laporan yang sudah ada.

### Menu Peta Match Reg2022

Menu keempat (`/peta-match`) memetakan baris yang sama dengan menu di atas,
memakai koordinat Regsosek-nya (`latitude_regsosek`/`longitude_regsosek`).
Dicek ke data: **seluruh 166.507 baris punya koordinat terpakai** (tidak ada
yang nol atau non-finite, semuanya dalam rentang Kalimantan), jadi tidak ada
predikat validitas koordinat seperti di peta utama.

Tooltip tiap titik menampilkan empat hal: Nama Prelist, Nama KK, ID SubSLS,
dan Alamat. *Catatan:* `alamat_gabung_regsosek` cuma terisi pada 4,5% baris
(7.575 dari 166.507), jadi Alamat sering tampil "-".

Filternya persis sama dengan menu Daftar Match Regsosek (wilayah berjenjang
+ Match Status + cari di empat kolom) dan sama-sama menunggu tombol
Terapkan Filter. Clustering-nya memakai ambang dan ukuran sel yang sama
dengan peta utama (`points.IndividualLimit`, `points.CellSizeForZoom`),
termasuk pola cluster-dulu yang menghitung total dari jumlah per sel
sehingga viewport padat cukup satu query — lihat "Catatan performa" di atas.

Bedanya dari peta utama: extent untuk auto-zoom dihitung per request lewat
`GET /api/match-bounds`, bukan dari bounding box per wilayah yang di-cache
saat startup. Itu karena di sini extent-nya ikut berubah oleh filter Match
Status dan pencarian, bukan cuma oleh wilayah.

### Kolom "Ditemukan di …" (join ke `se2026_match` & `se2026_match_regsosek`)

Menu **Daftar** dan menu **Peta** masing-masing menambahkan dua kolom flag,
dihitung dari `se2026_titik2.assignment_id`:

| Kolom | Sumber | Kunci |
| --- | --- | --- |
| **Ditemukan di Assignment Baru** | `se2026_match` | `assignment_id = assignment_id_tdk` |
| **Ditemukan di Regsosek** | `se2026_match_regsosek` | `assignment_id = assignment_id` |

Di tabel Daftar keduanya tampil sebagai centang (✓) atau strip (–) di kolom
bertint sendiri, ikut bisa di-sort seperti kolom lain, dan ikut terbawa ke
unduhan PDF ("V"/"-") dan Excel ("Ya"/"Tidak"). Di peta keduanya tampil
sebagai dua baris terakhir tooltip. Filternya (Semua / Ya / Tidak) ada di
kedua menu dan ikut menunggu tombol Terapkan Filter.

**Keduanya lewat `dictHas` ke dictionary ClickHouse, bukan `IN` atau
`JOIN`** — lihat butir 5 di "Catatan performa" untuk angkanya dan
`sql/dictionaries.sql` untuk DDL-nya. Pilihan itu juga menyelesaikan soal
kunci ganda dengan sendirinya: kunci di sisi kanan tidak unik
(`se2026_match` punya 89.437 baris untuk 88.636 `assignment_id_tdk`
distinct), sehingga `JOIN` biasa akan menggandakan baris di sisi kiri dan
merusak total serta paginasi. Sumber dictionary-nya sudah di-`GROUP BY`,
dan `dictHas` adalah uji keanggotaan, jadi kunci yang muncul dua kali tetap
menghasilkan satu baris.

Diukur, bukan diperkirakan (`assignment_id` di `se2026_titik2`):

| | Kunci distinct di tabel sumber | Yang ketemu di `se2026_titik2` |
| --- | --- | --- |
| `se2026_match.assignment_id_tdk` | 88.636 | 88.633 (99,997%) |
| `se2026_match_regsosek.assignment_id` | 166.505 | 166.505 (100%) |

Biaya query: tiap flag membangun himpunan kunci (~89rb dan ~167rb), jadi
kolomnya dihitung di query yang sama dengan `ORDER BY` supaya bisa di-sort.
Pada desa terbesar (27rb baris) satu halaman Daftar naik dari 21ms ke
80ms di sisi ClickHouse. Fragmen WHERE-nya cuma ditambahkan kalau filternya benar-benar
dipakai, jadi viewport peta tanpa filter tidak membayar apa pun: diukur dari browser, cluster se-provinsi 76ms tanpa filter. Dengan filter flag menyala biayanya lihat butir 5 di "Catatan performa".

> **Penting untuk menu Peta.** Kedua tabel sumber berisi keluarga yang
> **tidak ditemukan** saat pendataan, sehingga titik koordinatnya di
> `se2026_titik2` hampir tidak pernah terekam. Diukur:
>
> | Kelompok | Baris | Punya `latitude_ppl`/`longitude_ppl` terpakai |
> | --- | --- | --- |
> | Semua `se2026_titik2` | 2.168.304 | 1.238.096 |
> | Ada di `se2026_match` | 88.638 | **1** |
> | Ada di `se2026_match_regsosek` | 166.507 | **21** |
>
> Jadi di menu Peta, filter "Ditemukan di … = Ya" wajar kalau cuma
> memunculkan segelintir titik — itu bukan bug, tapi memang isi datanya.
> Di menu **Daftar** kedua flag tetap penuh (88.638 dan 166.507 baris),
> karena Daftar tidak menyaring baris berdasarkan koordinat. Untuk melihat
> baris-baris ini di atas peta, pakai menu **Peta Match Reg2022**, yang
> memakai `latitude_regsosek`/`longitude_regsosek` dan bukan koordinat
> `se2026_titik2`.


#### Kolom "Assignment ID Baru" (khusus menu Daftar)

Menu **Daftar** menambahkan satu kolom lagi dari `se2026_match`:
`assignment_id_baru`, lewat kunci yang sama
(`assignment_id = assignment_id_tdk`), dibaca lewat `dictGetString` dari
dictionary yang sama dengan flag-nya. Kolomnya cuma ada
di menu Daftar — peta tidak memintanya, dan field-nya `omitempty`
sehingga tidak ikut terkirim di payload `/api/points`.

Tiga hal yang menentukan bentuk query-nya:

1. **Sumber dictionary-nya di-`GROUP BY`.** `assignment_id_tdk` tidak
   unik (89.437 baris untuk 88.636 kunci distinct), jadi memetakan tabel
   mentahnya apa adanya akan membuat 792 kunci saling menimpa. Diukur:
   jumlah baris tetap 2.168.304, sama seperti sebelum kolom ini ada.

2. **Kunci ganda ditampilkan semua, bukan dipilih salah satu.** Dari 792
   kunci ganda itu, **787 punya `assignment_id_baru` yang benar-benar
   berbeda** — jadi `min()`/`argMin()` akan menampilkan nilai yang salah
   pada sekitar separuh baris tersebut. Nilainya digabung dengan koma
   (`groupUniqArray` + `arrayStringConcat`), jadi baris dengan dua
   assignment baru terbaca keduanya. Ini cuma 0,9% dari baris yang cocok.

3. **Flag dan nilainya datang dari dictionary yang sama** (`dict_match_tdk`),
   jadi `se2026_match` cukup dipetakan
   sekali. Hasilnya identik dengan `LEFT JOIN` yang dipakai sebelumnya —
   diuji ke seluruh 2.168.304 baris, 0 selisih.
   Flag-nya dibaca lewat `dictHas`, bukan tes "aid_baru tidak kosong",
   supaya tetap benar kalau kolom itu suatu saat berisi blank.

Biaya: pada desa terbesar (27rb baris) satu halaman Daftar 59ms diukur dari
browser (80ms -> 64ms di sisi ClickHouse setelah butir 5 di "Catatan
performa").
Join-nya tidak merusak primary-key range di sisi kiri — `read_rows` naik
dari 57.344 ke 146.781, dan selisihnya persis 89.437 baris `se2026_match`
sendiri.

Kolom ini ikut ke **unduhan Excel**, tapi **tidak ke PDF**. Alasannya sama
dengan kolom Assignment ID yang sudah ada: PDF Daftar memang tidak memuat
kolom ID sama sekali (bukan info yang dibaca dari kertas), dan kedua set
kolomnya sudah di 275–276mm dari 277mm yang tersedia di A4 landscape —
tidak ada ruang untuk UUID 36 karakter tanpa memangkas Nama/Alamat/Catatan.
### Label nomor bangunan di peta (level SubSLS)

Begitu filter peta diturunkan sampai **SubSLS**, tiap titik individual
mendapat label kecil berisi `nomor_bangunan` di sisi kanan-atasnya —
berjarak 1px dari tepi titik, dengan sisi bawah teks sejajar garis tengah
titik, teks biru berhalo putih (bukan kotak, supaya tidak menutupi peta).
Diimplementasikan sebagai `L.divIcon` non-interaktif per titik di
`web/static/app.js` (`addBangunanLabel`).

Dua penjaga, keduanya dari hasil pengukuran:

1. **Hanya di level SubSLS.** Satu SubSLS berisi 70 titik di median, 229 di
   p99, dan 889 di yang terbesar — jumlah elemen DOM yang wajar. Satu desa
   atau lebih luas akan menghasilkan ribuan label yang saling tumpang
   tindih.

2. **Hanya dari zoom 16 ke atas** (`BANGUNAN_LABEL_MIN_ZOOM`). Di bawah itu
   titik-titiknya terlalu rapat sehingga label tidak lagi menunjuk apa pun.

Label juga dilewati untuk nilai yang bukan nomor bangunan sungguhan: 1.770
baris bernilai `0`, 2 negatif, dan 3 bernilai `2147483647` (sentinel int32).
Titiknya tetap digambar dan tooltip tetap menampilkan nilai mentahnya —
yang dilewati cuma labelnya. Sisanya, 1.236.321 baris (99,86%), punya nilai
wajar, dan 99,7% di antaranya cuma 1–3 digit sehingga labelnya pendek.

**Kalau filter sudah di SubSLS tapi label belum muncul, itu soal zoom.**
Auto-fit ke satu SubSLS mendarat di zoom 18 untuk SubSLS biasa, tapi
**3.219 dari 15.722 SubSLS (20,5%)** titiknya tersebar cukup luas sehingga
auto-fit-nya di bawah zoom 16. Supaya tidak terlihat seperti fitur yang
rusak, panel statistik menampilkan baris "Perbesar peta untuk melihat label
nomor bangunan." tepat pada kondisi itu, dan baris itu hilang begitu
labelnya muncul.

Catatan implementasi: label berada di `leaflet-marker-pane`, yang posisinya
**di atas** canvas tempat titik digambar. Tanpa penanganan, label akan
menelan hover yang dibutuhkan tooltip — persis masalah yang dulu terjadi
dengan polygon SubSLS. Karena itu markernya `interactive: false` *dan*
kelas `.bangunan-label` di-set `pointer-events: none`. Sudah diuji di
browser: dengan 89 label aktif, hover ke titik tetap memunculkan tooltip
lengkap.

### Tampilan & responsif

Warna, jarak, radius, dan bayangan didefinisikan sekali sebagai CSS custom
property di blok `:root` paling atas `web/static/style.css` (`--navy`,
`--orange`, `--border`, `--radius`, dst.), jadi ganti warna brand cukup di
satu tempat. Kontrol form di-style secara struktural
(`.filter-row select`, `.filter-row input`) — bukan dengan mendaftar satu
per satu id-nya — supaya filter baru otomatis ikut gayanya tanpa menambah
CSS. Panah dropdown digambar sendiri (SVG inline) supaya bentuknya sama di
semua OS/browser, dan semua kontrol punya focus ring yang konsisten untuk
navigasi keyboard.

Layout-nya dirancang desktop-first lalu diciutkan lewat dua breakpoint di
bagian "Responsive" paling bawah file yang sama:

- **≤900px (tablet)** — padding diperkecil; grid filter di menu Daftar
  (`repeat(auto-fit, minmax(...))`, bukan flex-wrap) otomatis turun jumlah
  kolomnya, jadi filter selalu rata tidak bergerigi di lebar berapa pun.
- **≤600px (HP)** — filter jadi satu kolom, tinggi kontrol minimal 40px
  supaya nyaman disentuh, dan ukuran font input 16px (di bawah itu iOS
  otomatis nge-zoom halaman saat input difokuskan). Tombol jadi selebar
  layar, dan tabel memakai lebar penuh (padding samping container
  di-cancel) karena itu elemen terlebar di halaman.

Dua penyesuaian khusus HP yang diatur dari JS, bukan CSS:

- **Panel filter di Peta** mulai dalam keadaan tertutup (`app.js`,
  `setPanelCollapsed`) — kalau terbuka, panel selebar layar itu menutupi
  sebagian besar peta. Tombol buka/tutupnya tetap terlihat di header panel.
- **Kartu filter di Daftar** juga mulai tertutup dan punya tombol "Filter"
  sendiri di sebelah judul (`daftar.js`, `setFiltersCollapsed`; tombolnya
  `display: none` di atas 600px). Sembilan kontrol filter yang ditumpuk
  setinggi ±satu layar penuh akan mendorong tabelnya keluar layar kalau
  dibiarkan terbuka. Menekan "Terapkan Filter" di HP otomatis menutup
  kartunya lagi, supaya yang tampil langsung hasil filternya. Di lebar ini
  `#daftar-container` juga di-scroll seperti halaman biasa (di desktop dia
  flex column ber-`overflow: hidden` dengan hanya tabelnya yang scroll
  sendiri) — kalau tidak, isi yang lebih tinggi dari layar jadi tidak bisa
  dijangkau sama sekali, bukan cuma terpotong.

Kontrol bawaan Leaflet juga ditata ulang supaya tidak bertabrakan dengan
panel filter yang menempel di kiri atas: tombol zoom dipindah ke kanan
bawah (default Leaflet kiri atas — persis di bawah panel, jadi selamanya
tertutup), dan pemilih basemap tampil terbuka di desktop tapi menciut jadi
satu ikon di HP.

## Login

Seluruh website ada di balik login. Daftar akunnya dari `.env`, dipisah
koma, tiap entri `username:password`:

```
AUTH_USERS=viewer@bps.go.id:rahasia,mitra:rahasia2
```

Yang memisahkan cuma titik dua **pertama**, jadi password boleh mengandung
titik dua. Password **tidak boleh** mengandung koma — itu pemisah antar
akun, dan password yang mengandungnya akan terpotong diam-diam.

Bentuk lama satu akun (`AUTH_USERNAME` + `AUTH_PASSWORD`) masih diterima
kalau `AUTH_USERS` kosong, supaya deployment yang sudah jalan tidak putus;
`AUTH_USERS` menang kalau keduanya diisi.

Minimal satu akun **wajib** — `config.Load` menolak start kalau tidak ada.
Ini disengaja: kegagalan diam-diam dari password opsional adalah dashboard
yang terbuka untuk siapa saja, dan itu lebih buruk daripada server yang
menolak jalan dengan pesan jelas. Entri yang salah bentuk, username
duplikat, atau password kosong juga ditolak saat startup. Untuk Docker,
`AUTH_USERS` sudah diteruskan lewat `docker-compose.yml`.

**Yang dilindungi:** semuanya kecuali `/login`, `/api/login`, `/api/logout`,
dan `/healthz`. Gerbangnya (`requireAuth` di `internal/api/auth.go`)
dipasang di luar mux, jadi aset statis pun ikut terlindungi — kalau hanya
API yang dijaga, seluruh frontend masih bisa dibaca tanpa login.
`/healthz` sengaja dibiarkan terbuka supaya monitoring tidak perlu
kredensial; isinya cuma "ClickHouse menjawab atau tidak".

**Sesi** berupa cookie bertanda tangan HMAC berisi waktu kedaluwarsa dan
username pemiliknya — `HttpOnly`, `SameSite=Lax`, dan `Secure` otomatis
menyala kalau koneksinya HTTPS (langsung atau lewat `X-Forwarded-Proto`),
jadi deployment HTTP di jaringan kantor tetap jalan. Berlaku 12 jam.

Kuncinya diturunkan dari kredensial akun itu sendiri (`newAuth`), **satu
kunci per akun**, bukan satu secret bersama. Dua sifat lahir dari situ dan
keduanya sudah diuji:

- **Mengganti password satu akun membatalkan sesi akun itu saja.** Diuji:
  setelah password `mitra6400` diubah, sesi `mitra6400` yang sedang
  berjalan langsung 401 sementara sesi `viewer6400@bps.go.id` tetap 200.
  Kalau kuncinya dibagi bersama, menambah akun baru pun akan menendang
  keluar semua orang.
- **Menghapus akun dari `.env` langsung mematikan sesinya**, karena tidak
  ada lagi kunci untuk memverifikasi tokennya.

Tidak ada penyimpanan sesi dan tidak ada secret tambahan yang harus
dirotasi. Ini tidak membocorkan apa pun — yang tahu password toh bisa
login. Alternatifnya (kunci acak saat startup) akan memaksa semua orang
login ulang setiap deploy.

Beberapa hal kecil yang sengaja dibuat begitu:

- Perbandingan username dan password memakai `subtle.ConstantTimeCompare`,
  dan pesan gagalnya tidak membedakan "username salah" dari "password
  salah". Perulangan pemeriksaan akun juga sengaja tidak berhenti begitu
  ketemu — kalau berhenti lebih awal, tebakan yang mengenai akun pertama
  jadi terukur lebih cepat daripada yang mengenai akun terakhir.
- Percobaan login yang gagal ditulis ke log beserta username dan IP —
  passwordnya tidak pernah.
- Parameter `next` (supaya sesi yang habis mengembalikan Anda ke halaman
  yang sedang dibuka) divalidasi server-side oleh `safeNext`: hanya path
  diawali satu garis miring yang diterima, jadi form login tidak bisa
  dipakai melempar orang ke situs lain.
- Halaman login sengaja berdiri sendiri (CSS inline) — kalau ia menautkan
  `style.css`, file itu harus dibuka ke publik atau halamannya tampil
  polos.
- Request API yang kena 401 di tengah pemakaian tidak di-retry; frontend
  langsung mengarahkan ke `/login` sambil membawa halaman asalnya.

## Menjalankan

**Sebelum pertama kali jalan:** buat dua dictionary ClickHouse yang dipakai
kolom "Ditemukan di …" dan "Assignment ID Baru". Tanpa keduanya `/api/list`
dan `/api/points` gagal total, bukan sekadar melambat. DDL beserta
penjelasannya ada di `sql/dictionaries.sql`:

```bash
clickhouse-client --host <host> --database dtsen --multiquery < sql/dictionaries.sql
```

Server juga memeriksanya saat startup dan menulis log `level=ERROR` yang
menyebut nama dictionary dan file ini kalau salah satu tidak bisa dipakai.

Salin `example.env` ke `.env` lalu isi nilai aslinya:

```bash
cp example.env .env
```

```
HOST=192.168.50.252
PORT=9000
DATABASE=dtsen
USERNAME=default
PASSWORD=
```

> **Catatan penting (Windows, kalau jalan langsung tanpa Docker):** jangan
> andalkan environment variable asli untuk key-key ini — Windows sudah
> punya `USERNAME` bawaan (nama akun Windows yang sedang login) yang
> bentrok dengan key `.env` ini. Loader `.env` di project ini sudah
> menangani itu (nilai dari file `.env` selalu menang atas environment
> variable OS), tapi kalau suatu saat migrasi ke loader lain, ini adalah
> jebakan klasik yang perlu diwaspadai. Di dalam container Docker (Linux)
> ini bukan masalah karena `USERNAME` bukan variable bawaan di sana.

Opsional, tambahkan di `.env` (lihat `example.env` untuk contoh lengkap
berkomentar):
- `CH_PROTOCOL=native` (default) atau `http` — pilih protokol koneksi ke
  ClickHouse. Native pakai `PORT=9000`, HTTP biasanya `PORT=8123`.
- `HTTP_ADDR=:8082` — alamat listen web server (default kalau tidak diset: `:8080`; project ini pakai `:8082`, lihat `example.env`).

### Lewat Docker Compose (direkomendasikan untuk deploy)

```bash
cp example.env .env   # isi dengan nilai asli
docker compose up -d --build
```

Peta jalan di `http://localhost:8082`. Ini build multi-stage (`Dockerfile`):
compile binary Go statis, lalu jalankan di image Alpine minimal sebagai
user non-root, dengan healthcheck bawaan yang mem-ping `/healthz`. Frontend
sudah ter-embed di binary saat build, jadi tidak ada file terpisah yang
perlu di-mount.

ClickHouse (`HOST` di `.env`) berjalan di luar compose ini, diakses lewat
LAN — bridge network default Docker biasanya bisa langsung mencapainya.
Kalau gagal connect dari dalam container (mis. firewall dibatasi ke IP host
saja), ada opsi `network_mode: host` yang tinggal di-uncomment di
`docker-compose.yml` (Linux saja, tidak berlaku di Docker Desktop
Windows/Mac).

Perintah lain yang berguna:

```bash
docker compose logs -f app     # lihat log
docker compose down            # stop & hapus container
docker compose up -d --build   # rebuild setelah ganti kode
```

### Langsung dengan Go (tanpa Docker)

Jalankan langsung:

```bash
go run .
```

Atau build binary (frontend ter-embed di dalam binary, jadi cukup satu file
`.exe` untuk deploy — tidak perlu folder `web/` terpisah):

```bash
go build -o bin/se2026-titik-maps.exe .
./bin/se2026-titik-maps.exe
```

Lalu buka `http://localhost:8082` di browser (dari `HTTP_ADDR=:8082` di `.env`; kalau `HTTP_ADDR` tidak diset sama sekali, defaultnya `:8080`).

## Endpoint

Semua endpoint di bawah butuh sesi login kecuali yang ditandai — lihat
bagian "Login" di atas.

- `GET /` — peta (frontend)
- `GET /api/points?minLat=&maxLat=&minLon=&maxLon=&zoom=&kabkota=&kecamatan=&desa=&sls=&subsls=&flagBaru=&flagRegsosek=` — data titik/cluster untuk satu viewport. Titik individual ikut membawa kedua flag "Ditemukan di …" untuk tooltip (filter wilayah semuanya opsional, tapi berjenjang — lihat Validate di `internal/points/points.go`)
- `GET /api/bounds` — extent geografis + total baris valid di seluruh dataset
- `GET /api/kabkota` — daftar kabupaten/kota yang ada di data, dengan jumlah titik & bounding box masing-masing
- `GET /api/kecamatan?kabkota=` — daftar kecamatan di dalam satu kabupaten/kota (parameter wajib); `name` diisi dari PostGIS kalau `MAP_*` dikonfigurasi, kosong kalau tidak
- `GET /api/desa?kabkota=&kecamatan=` — daftar desa/kelurahan di dalam satu kecamatan (kedua parameter wajib); `name` sama seperti di atas
- `GET /api/sls?kabkota=&kecamatan=&desa=` — daftar Kode SLS di dalam satu desa/kelurahan (ketiga parameter wajib); `name` diisi dari kolom `nmsls` di PostGIS kalau `MAP_*` dikonfigurasi, kosong kalau tidak
- `GET /api/subsls?kabkota=&kecamatan=&desa=&sls=` — daftar Kode SubSLS di dalam satu SLS (keempat parameter wajib)
- `GET /api/subsls-polygon?kabkota=&kecamatan=&desa=&sls=&subsls=` — GeoJSON batas SubSLS untuk overlay di peta (kelima parameter wajib); 503 kalau PostGIS tidak dikonfigurasi/tidak terhubung — lihat `internal/mapdb/`
- `GET /api/list?kabkota=&kecamatan=&desa=&sls=&subsls=&jenisPrelist=&keberadaanKeluarga=&status=&flagBaru=&flagRegsosek=&search=&page=&pageSize=&sortBy=&dir=` — satu halaman tabel untuk menu Daftar. `sortBy` salah satu dari `nama` (default), `alamat`, `subsls`, `jenis_prelist`, `nomor_bangunan`, `keberadaan_keluarga`, `status`, `assignment_id`, `ada_assignment_baru`, `ada_regsosek`, `assignment_id_baru` (nilai lain jatuh balik ke `nama`); `dir` `asc` (default) atau `desc`; `pageSize` maks 200. `search` mencari substring nama (tidak case-sensitive), dikirim lewat parameter binding, bukan interpolasi string. Filter atribut (`jenisPrelist`, `keberadaanKeluarga`, `status`) semuanya opsional dan independen dari filter wilayah maupun satu sama lain — nilainya divalidasi terhadap enum tetap di `internal/points/points.go`, pakai `__EMPTY__` untuk memfilter kolom yang kosong. `flagBaru` dan `flagRegsosek` hanya menerima `""` (semua), `"1"` (ada) atau `"0"` (tidak ada) — nilai lain ditolak 400
- `GET /api/list/pdf?kabkota=&kecamatan=&desa=&sls=&subsls=&jenisPrelist=&keberadaanKeluarga=&status=&flagBaru=&flagRegsosek=&search=&sortBy=&dir=` — PDF "Daftar Hasil Pendataan". `kabkota`, `kecamatan` dan `desa` **wajib** (cakupan minimal satu desa/kelurahan; lebih luas dari itu ditolak 400), `sls` dan `subsls` opsional untuk mempersempit. Filter atribut dan `search` ikut mempersempit isi PDF kalau diisi, urutan barisnya ikut `sortBy`/`dir`
- `GET /api/list/xlsx?kabkota=&kecamatan=&desa=&sls=&subsls=&jenisPrelist=&keberadaanKeluarga=&status=&flagBaru=&flagRegsosek=&search=&sortBy=&dir=` — laporan "Daftar Hasil Pendataan" yang sama persis, sebagai workbook Excel (.xlsx) — parameter dan aturan cakupannya identik dengan `/api/list/pdf` (lihat `prepareReport` di `internal/api/server.go`, dipakai bareng oleh kedua handler)
- `GET /api/reg2022?kabkota=&kecamatan=&desa=&sls=&subsls=&matchStatus=&search=&page=&pageSize=&sortBy=&dir=` — satu halaman tabel untuk menu Daftar Reg2022 (tabel `se2026_match_regsosek`). Filter wilayah sama dengan endpoint lain; `matchStatus` divalidasi terhadap 7 nilai tetap di `internal/regsosek/regsosek.go` (pakai `__EMPTY__` untuk kolom kosong); `search` mencari substring di `nama_prelist` dan `nama_kk` saja, lewat parameter binding — nomor KK/NIK tidak dipakai sebagai kunci cari. `sortBy` salah satu dari `nama` (default), `nama_kk`, `subsls`, `match_status`, `alamat_regsosek`, `nama_matched`, `assignment_id`
- `GET /api/reg2022/filter-options` — daftar nilai `match_status` untuk dropdown filter menu Daftar Reg2022
- `GET /api/reg2022/pdf?...` dan `GET /api/reg2022/xlsx?...` — laporan "Daftar Match Regsosek". Parameter filternya sama dengan `/api/reg2022`; `kabkota` dan `kecamatan` **wajib** (cakupan minimal satu kecamatan, lebih luas ditolak 400)
- `GET /api/match-points?minLat=&maxLat=&minLon=&maxLon=&zoom=&kabkota=&...&matchStatus=&search=` — titik/cluster untuk viewport menu Peta Match Reg2022, memakai `latitude_regsosek`/`longitude_regsosek`
- `GET /api/match-bounds?kabkota=&...&matchStatus=&search=` — extent geografis baris yang cocok dengan filter, untuk auto-zoom peta match (dihitung per request karena bergantung pada filter, bukan cuma wilayah)
- `GET /api/filter-options` — daftar nilai enum untuk dropdown filter Jenis Prelist, Keberadaan Keluarga, dan Status (statis, bukan query ke ClickHouse)
- `GET /healthz` — health check (ping ClickHouse); satu dari sedikit endpoint yang tidak butuh login
- `GET /login` — halaman form login (publik)
- `POST /api/login` — memeriksa username/password, menerbitkan cookie sesi; body `username`, `password`, opsional `next`
- `POST /api/logout` — menghapus cookie sesi lalu redirect ke `/login`

## Catatan kualitas data

Ada sebagian kecil baris dengan `latitude_ppl`/`longitude_ppl` yang jelas di
luar wilayah Indonesia (mis. longitude negatif). Baris ini tetap ditampilkan
apa adanya (tidak difilter) selama koordinatnya bukan `0`/`0` atau
non-finite — kalau perlu dibersihkan, itu sebaiknya dilakukan di sisi data
sumber, bukan disembunyikan oleh peta ini.

### Kolom `jumlah_usaha` (dulu `keberadaan_usaha`) — sementara disembunyikan

Tabel `se2026_titik2` menamai kolom ini `jumlah_usaha`, sementara tabel lama
menamainya `keberadaan_usaha`. Isinya sama: **cacahan** usaha di titik itu,
bukan penanda ada/tidak (0/1) — di tabel lama pun nilainya sudah 0–8, jadi
yang berubah cuma namanya, bukan artinya. Di kode, `Point.KeberadaanUsaha`
di-scan dari kolom baru itu dan nama JSON-nya (`keberadaan_usaha`) sengaja
dipertahankan.

Ada ±1.847 baris yang nilainya jauh di luar rentang wajar (maksimum
tercatat 97.351.353) — kelihatannya salah entri/parsing di sumber data.
Karena itu field Go-nya `int32`, bukan `uint8` seperti dulu: `uint8` gagal
men-scan baris-baris tersebut dan akan membuat query-nya error, bukan cuma
menampilkan angka aneh. Sama seperti catatan koordinat di atas, nilainya
ditampilkan apa adanya — pembersihannya sebaiknya di sisi data sumber.

Kolom ini **untuk sementara disembunyikan dari semua tampilan** — tooltip
peta, tabel Daftar, PDF, dan Excel — meski masih diambil dari ClickHouse
seperti biasa (`SELECT`-nya tidak berubah, `Point.KeberadaanUsaha` masih
terisi, cuma tidak dirender di mana pun). Menampilkannya lagi tinggal
menambahkan baris tooltip/kolom tabel yang sudah dihapus; lihat riwayat git
`tooltipHTML` di `web/static/app.js`, `rowHTML` di `web/static/daftar.js`,
header tabel di `web/static/index.html`, dan `subslsColumns`/`wideColumns`/
`rowFor` di `internal/pdfreport/pdfreport.go` serta `columns`/`writeTable`
di `internal/xlsxreport/xlsxreport.go`.

### Kolom `nomor_bangunan` dan `catatan`

**`nomor_bangunan`** menempati posisi yang dulu dipakai `keberadaan_usaha`
di tooltip peta, tabel Daftar, PDF, dan Excel — sortable di Daftar lewat
`sortBy=nomor_bangunan`. Nilainya `int32` (ada beberapa baris bernilai `-1`
dan beberapa lagi persis `2147483647`, kemungkinan sentinel/nilai kosong
dari sumber data — ditampilkan apa adanya, sama seperti `jumlah_usaha`).

**`catatan`** (teks bebas catatan lapangan, sampai ±1.450 karakter) cuma
muncul di **unduhan PDF dan Excel**, bukan di tooltip peta atau tabel
Daftar — kolom ini bisa berisi catatan internal yang tidak dimaksudkan
untuk ditelusuri di layar. Konsekuensinya di kode: `Point.Catatan` cuma
diisi oleh `ListAll` (jalur PDF/Excel), bukan oleh query yang melayani
`/api/points` atau `/api/list` — keduanya sama sekali tidak men-`SELECT`
kolom ini, jadi field itu selalu kosong (dan `omitempty` menyembunyikannya
dari body JSON) di kedua endpoint tersebut. Lihat `listItems` dan parameter
`includeCatatan`-nya di `internal/points/points.go`.

## Struktur project

```
main.go                    entry point, wiring, graceful shutdown, refresher
internal/config/           parsing .env
internal/chdb/             koneksi & pool ClickHouse
internal/mapdb/            koneksi PostGIS opsional + query polygon batas SubSLS
internal/points/           query viewport → individual points / clusters
internal/regsosek/         query tabel se2026_match_regsosek (menu Daftar Match Regsosek & Peta Match Reg2022)
internal/report/           metadata wilayah/filter (Region) dipakai bareng pdfreport & xlsxreport
internal/pdfreport/        generate PDF "Daftar Hasil Pendataan" (pakai go-pdf/fpdf)
internal/xlsxreport/       generate Excel "Daftar Hasil Pendataan" (pakai excelize/v2)
internal/api/              HTTP handlers + logging middleware
web/static/                frontend (Leaflet, di-embed ke binary via go:embed)
  index.html                shell: nav + view Peta + view Daftar
  common.js                 helper bersama (fetch-with-retry, dropdown wilayah cascading)
  app.js                    logika menu Peta
  daftar.js                 logika menu Daftar (tabel, sort, paginasi)
  reg2022.js                logika menu Daftar Match Regsosek
  match-map.js              logika menu Peta Match Reg2022
  nav.js                    switching antar menu
Dockerfile                 build multi-stage → binary statis di image Alpine
docker-compose.yml         menjalankan image di atas, baca env dari .env
example.env                template .env — salin ke .env lalu isi nilai asli
```
