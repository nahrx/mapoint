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

### Filter kabupaten/kota

Dropdown "Kabupaten/Kota" di panel diisi otomatis dari data yang ada (4
digit pertama `level_6_full_code`, kode wilayah BPS/Kemendagri provinsi+
kabupaten/kota), bukan daftar statis — jadi kalau cakupan data bertambah ke
kabupaten/kota lain, opsinya otomatis muncul (nama tampil kalau kodenya ada
di `internal/points/kabkota_names.go`, kalau belum ada di map itu tetap
berfungsi sebagai filter, hanya labelnya berupa kode mentah). Memilih satu
kab/kota otomatis zoom ke area itu (bounding box dihitung pakai persentil
1–99% supaya titik dengan GPS salah/outlier tidak merusak zoom) dan
membatasi semua query berikutnya (`GET /api/points?...&kabkota=6472`).

Dropdown "Kecamatan" di sebelahnya mengikuti (cascading): nonaktif sampai
sebuah kabupaten/kota dipilih, lalu terisi otomatis dari 3 digit berikutnya
di `level_6_full_code` (`GET /api/kecamatan?kabkota=6472`). Tidak ada tabel
referensi nama kecamatan di database, jadi labelnya berupa kode ("Kec.
061") apa adanya — tidak ditebak-tebak namanya. Kecamatan tanpa
kabupaten/kota ditolak backend (400) karena kodenya hanya unik di dalam satu
kabupaten/kota.

Dropdown "Desa/Kelurahan" mengikuti pola yang sama satu tingkat lagi:
nonaktif sampai kecamatan dipilih, terisi dari 3 digit berikutnya
(`GET /api/desa?kabkota=6472&kecamatan=061`), label berupa kode ("Desa/Kel.
003"), dan ditolak backend (400) kalau dikirim tanpa kecamatan.

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

### Menu Daftar

Selain menu "Peta", ada menu "Daftar" (nav di paling atas) yang menampilkan
isi tabel `se2026_titik` sebagai daftar biasa — bukan tampilan peta:

- **Sort by nama** (`ORDER BY nama_assignment`), klik header kolom "Nama"
  untuk membalik arah (naik/turun). Baris dengan nama kosong ikut tampil
  apa adanya (muncul duluan saat urutan naik) — bukan disembunyikan,
  karena ini daftar data mentah, bukan cuma titik yang siap dipetakan.
- **Paginasi** lewat `LIMIT`/`OFFSET` di ClickHouse — bukan infinite
  scroll, supaya jumlah data yang ditransfer & di-render tiap saat tetap
  kecil. Ukuran halaman bisa dipilih 25/50/100/200 baris.
- **Filter wilayah** kabupaten/kota → kecamatan → desa/kelurahan → SLS →
  SubSLS, cascading persis seperti di menu Peta, tapi state-nya independen
  (pilih filter di satu menu tidak mengubah filter di menu lainnya) dan
  tidak ada auto-zoom (karena tidak ada peta di sini).
- **Tidak difilter oleh validitas koordinat** — beda dari menu Peta, daftar
  ini menampilkan semua baris yang cocok filter wilayah, termasuk yang
  `latitude_ppl`/`longitude_ppl`-nya `0` atau kosong, karena tujuannya
  menelusuri data, bukan memetakannya.
- **Unduh PDF** — tombol di sebelah filter, aktif hanya kalau filter sudah
  lengkap sampai Kode SubSLS (di titik itu `level_6_full_code` sudah pas
  16 digit / satu wilayah spesifik, cocok jadi satu laporan). PDF berjudul
  "Daftar Hasil Pendataan", berisi keterangan wilayah (nama kab/kota +
  kode tiap level + kode wilayah 16 digit) lalu tabel semua baris di SubSLS
  itu (bukan cuma satu halaman tabel — lihat `ListAll` di
  `internal/points/points.go`, dibatasi `ReportMaxRows=5000` sebagai jaring
  pengaman). Kolom `assignment_id` sengaja tidak disertakan (bukan info
  yang relevan untuk dicetak), begitu juga kolom ID SUBSLS per baris —
  karena nilainya sama persis untuk semua baris dalam satu SubSLS, itu
  cukup ditulis sekali di keterangan wilayah. PDF digenerate di server
  pakai `github.com/go-pdf/fpdf` (pure Go, tanpa Chrome/wkhtmltopdf) lewat
  `internal/pdfreport/`.

Endpoint-nya `GET /api/list` (lihat bagian Endpoint di bawah). Query
`ORDER BY ... LIMIT ... OFFSET ...` tanpa filter di atas 4 juta baris
diukur ~1–3 detik di ClickHouse (lihat `internal/points/points.go`) — masih
wajar untuk tampilan tabel yang dilihat manusia, tapi kalau nanti data jauh
lebih besar dan halaman-halaman terakhir (offset sangat dalam) terasa
lambat, itu batasan `LIMIT/OFFSET` yang umum di database kolom manapun;
solusinya persempit dulu pakai filter wilayah sebelum menjelajahi halaman
jauh.

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
- `GET /api/kecamatan?kabkota=` — daftar kecamatan di dalam satu kabupaten/kota (parameter wajib)
- `GET /api/desa?kabkota=&kecamatan=` — daftar desa/kelurahan di dalam satu kecamatan (kedua parameter wajib)
- `GET /api/sls?kabkota=&kecamatan=&desa=` — daftar Kode SLS di dalam satu desa/kelurahan (ketiga parameter wajib)
- `GET /api/subsls?kabkota=&kecamatan=&desa=&sls=` — daftar Kode SubSLS di dalam satu SLS (keempat parameter wajib)
- `GET /api/list?kabkota=&kecamatan=&desa=&sls=&subsls=&page=&pageSize=&dir=` — satu halaman tabel untuk menu Daftar, urut nama (`dir=asc` default atau `desc`), `pageSize` maks 200
- `GET /api/list/pdf?kabkota=&kecamatan=&desa=&sls=&subsls=&dir=` — PDF "Daftar Hasil Pendataan" untuk satu SubSLS (kelima parameter wajib)
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
internal/points/           query viewport → individual points / clusters
internal/pdfreport/        generate PDF "Daftar Hasil Pendataan" (pakai go-pdf/fpdf)
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
