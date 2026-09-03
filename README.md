# Peta Titik SE2026

Web map (Go + Leaflet/OpenStreetMap) yang menampilkan seluruh titik pada
tabel ClickHouse `dtsen.se2026_titik` (jutaan baris) secara seamless, tanpa
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
(`GET /api/sls?kabkota=6472&kecamatan=061&desa=003`), label berupa kode
("SLS 0070"), dan ditolak backend (400) kalau dikirim tanpa desa/kelurahan.

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
isi tabel `se2026_titik` sebagai daftar biasa — bukan tampilan peta:

- **Sort by kolom apa saja** — klik header kolom mana pun (Nama, Alamat, ID
  SUBSLS, Jenis Prelist, Keberadaan Usaha, Keberadaan Keluarga, Status,
  Assignment ID) untuk mengurutkan tabel berdasarkan kolom itu; klik lagi
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

## Menjalankan

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

- `GET /` — peta (frontend)
- `GET /api/points?minLat=&maxLat=&minLon=&maxLon=&zoom=&kabkota=&kecamatan=&desa=&sls=&subsls=` — data titik/cluster untuk satu viewport (filter wilayah semuanya opsional, tapi berjenjang — lihat Validate di `internal/points/points.go`)
- `GET /api/bounds` — extent geografis + total baris valid di seluruh dataset
- `GET /api/kabkota` — daftar kabupaten/kota yang ada di data, dengan jumlah titik & bounding box masing-masing
- `GET /api/kecamatan?kabkota=` — daftar kecamatan di dalam satu kabupaten/kota (parameter wajib); `name` diisi dari PostGIS kalau `MAP_*` dikonfigurasi, kosong kalau tidak
- `GET /api/desa?kabkota=&kecamatan=` — daftar desa/kelurahan di dalam satu kecamatan (kedua parameter wajib); `name` sama seperti di atas
- `GET /api/sls?kabkota=&kecamatan=&desa=` — daftar Kode SLS di dalam satu desa/kelurahan (ketiga parameter wajib)
- `GET /api/subsls?kabkota=&kecamatan=&desa=&sls=` — daftar Kode SubSLS di dalam satu SLS (keempat parameter wajib)
- `GET /api/subsls-polygon?kabkota=&kecamatan=&desa=&sls=&subsls=` — GeoJSON batas SubSLS untuk overlay di peta (kelima parameter wajib); 503 kalau PostGIS tidak dikonfigurasi/tidak terhubung — lihat `internal/mapdb/`
- `GET /api/list?kabkota=&kecamatan=&desa=&sls=&subsls=&jenisPrelist=&keberadaanKeluarga=&status=&search=&page=&pageSize=&sortBy=&dir=` — satu halaman tabel untuk menu Daftar. `sortBy` salah satu dari `nama` (default), `alamat`, `subsls`, `jenis_prelist`, `keberadaan_usaha`, `keberadaan_keluarga`, `status`, `assignment_id` (nilai lain jatuh balik ke `nama`); `dir` `asc` (default) atau `desc`; `pageSize` maks 200. `search` mencari substring nama (tidak case-sensitive), dikirim lewat parameter binding, bukan interpolasi string. Filter atribut (`jenisPrelist`, `keberadaanKeluarga`, `status`) semuanya opsional dan independen dari filter wilayah maupun satu sama lain — nilainya divalidasi terhadap enum tetap di `internal/points/points.go`, pakai `__EMPTY__` untuk memfilter kolom yang kosong
- `GET /api/list/pdf?kabkota=&kecamatan=&desa=&sls=&subsls=&jenisPrelist=&keberadaanKeluarga=&status=&search=&sortBy=&dir=` — PDF "Daftar Hasil Pendataan". `kabkota`, `kecamatan` dan `desa` **wajib** (cakupan minimal satu desa/kelurahan; lebih luas dari itu ditolak 400), `sls` dan `subsls` opsional untuk mempersempit. Filter atribut dan `search` ikut mempersempit isi PDF kalau diisi, urutan barisnya ikut `sortBy`/`dir`
- `GET /api/list/xlsx?kabkota=&kecamatan=&desa=&sls=&subsls=&jenisPrelist=&keberadaanKeluarga=&status=&search=&sortBy=&dir=` — laporan "Daftar Hasil Pendataan" yang sama persis, sebagai workbook Excel (.xlsx) — parameter dan aturan cakupannya identik dengan `/api/list/pdf` (lihat `prepareReport` di `internal/api/server.go`, dipakai bareng oleh kedua handler)
- `GET /api/filter-options` — daftar nilai enum untuk dropdown filter Jenis Prelist, Keberadaan Keluarga, dan Status (statis, bukan query ke ClickHouse)
- `GET /healthz` — health check (ping ClickHouse)

## Catatan kualitas data

Ada sebagian kecil baris dengan `latitude_ppl`/`longitude_ppl` yang jelas di
luar wilayah Indonesia (mis. longitude negatif). Baris ini tetap ditampilkan
apa adanya (tidak difilter) selama koordinatnya bukan `0`/`0` atau
non-finite — kalau perlu dibersihkan, itu sebaiknya dilakukan di sisi data
sumber, bukan disembunyikan oleh peta ini.

## Struktur project

```
main.go                    entry point, wiring, graceful shutdown, refresher
internal/config/           parsing .env
internal/chdb/             koneksi & pool ClickHouse
internal/mapdb/            koneksi PostGIS opsional + query polygon batas SubSLS
internal/points/           query viewport → individual points / clusters
internal/report/           metadata wilayah/filter (Region) dipakai bareng pdfreport & xlsxreport
internal/pdfreport/        generate PDF "Daftar Hasil Pendataan" (pakai go-pdf/fpdf)
internal/xlsxreport/       generate Excel "Daftar Hasil Pendataan" (pakai excelize/v2)
internal/api/              HTTP handlers + logging middleware
web/static/                frontend (Leaflet, di-embed ke binary via go:embed)
  index.html                shell: nav + view Peta + view Daftar
  common.js                 helper bersama (fetch-with-retry, dropdown wilayah cascading)
  app.js                    logika menu Peta
  daftar.js                 logika menu Daftar (tabel, sort, paginasi)
  nav.js                    switching antar menu
Dockerfile                 build multi-stage → binary statis di image Alpine
docker-compose.yml         menjalankan image di atas, baca env dari .env
example.env                template .env — salin ke .env lalu isi nilai asli
```
