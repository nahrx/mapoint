-- Dictionary yang dipakai kolom "Ditemukan di Assignment Baru", "Ditemukan
-- di Regsosek", dan "Assignment ID Baru" di menu Daftar & Peta.
--
-- Aplikasi MEMBUTUHKAN kedua dictionary ini: tanpa mereka setiap query
-- /api/list dan /api/points gagal. Jalankan file ini terhadap database yang
-- sama dengan DATABASE di .env, mis:
--
--   clickhouse-client --host <host> --database dtsen --multiquery < sql/dictionaries.sql
--
-- Kenapa dictionary, bukan JOIN/IN seperti sebelumnya: keduanya membaca
-- ulang tabel sumber di SETIAP query (89rb + 167rb baris), dan itu biaya
-- terbesar di sisi database. Terukur pada satu halaman Daftar di desa
-- terbesar (27rb baris): 112ms dengan JOIN + IN, 21ms dengan dictionary —
-- praktis sama dengan 19ms tanpa kolom-kolom itu sama sekali. Rincian dan
-- angka pembandingnya ada di README, bagian "Catatan performa".
--
-- CATATAN: nama tabel di dalam QUERY ditulis lengkap (dtsen.xxx). Itu
-- bukan hiasan — query sumber dictionary dijalankan tanpa database
-- default, dan klausa DB pada SOURCE hanya berlaku untuk bentuk TABLE,
-- bukan QUERY; tanpa prefiks, dictionary-nya berstatus FAILED. Kalau
-- DATABASE di .env bukan dtsen, ganti prefiks itu di dua tempat.
--
-- CREATE OR REPLACE dipakai (bukan IF NOT EXISTS) supaya file ini aman
-- dijalankan ulang dan perubahannya benar-benar diterapkan — dengan IF NOT
-- EXISTS, definisi lama diam-diam dipertahankan.

-- Kunci "ditemukan di regsosek". Hanya keanggotaan, tidak ada kolom lain
-- yang diambil dari tabel ini. GROUP BY karena assignment_id di sana tidak
-- dijamin unik (166.507 baris untuk 166.505 kunci).
CREATE OR REPLACE DICTIONARY dict_regsosek_key
(
    assignment_id String,
    ada UInt8
)
PRIMARY KEY assignment_id
SOURCE(CLICKHOUSE(QUERY '
    SELECT assignment_id, toUInt8(1) AS ada
    FROM dtsen.se2026_match_regsosek
    GROUP BY assignment_id
'))
-- COMPLEX_KEY_* wajib karena kuncinya String, bukan UInt64.
LAYOUT(COMPLEX_KEY_HASHED())
-- Tabel sumbernya diisi per batch, bukan streaming, jadi refresh berkala
-- sudah cukup. Konsekuensinya flag bisa tertinggal maksimal 10 menit dari
-- tabel sumber.
LIFETIME(MIN 300 MAX 600);

-- Kunci + nilai dari se2026_match. assignment_id_tdk TIDAK unik: 792 kunci
-- muncul lebih dari sekali dan 787 di antaranya punya assignment_id_baru
-- yang benar-benar berbeda. Karena itu sumbernya di-GROUP BY dan nilainya
-- digabung dengan koma, bukan dipilih salah satu — memilih salah satu akan
-- menampilkan nilai yang keliru pada sekitar separuh baris tersebut.
CREATE OR REPLACE DICTIONARY dict_match_tdk
(
    assignment_id_tdk String,
    aid_baru String,
    ada UInt8
)
PRIMARY KEY assignment_id_tdk
SOURCE(CLICKHOUSE(QUERY $$
    SELECT assignment_id_tdk,
           arrayStringConcat(arraySort(groupUniqArray(assignment_id_baru)), ', ') AS aid_baru,
           toUInt8(1) AS ada
    FROM dtsen.se2026_match
    GROUP BY assignment_id_tdk
$$))
LAYOUT(COMPLEX_KEY_HASHED())
LIFETIME(MIN 300 MAX 600);

-- Cek setelah dibuat: status kedua baris harus LOADED.
--   SELECT name, status, element_count, formatReadableSize(bytes_allocated),
--          last_exception
--   FROM system.dictionaries
--   WHERE name IN ('dict_regsosek_key', 'dict_match_tdk');
--
-- Ukuran saat ditulis: 166.505 kunci / 24 MiB dan 88.636 kunci / 34 MiB,
-- masing-masing dimuat dalam <0,5 detik.
