## Session

| | Nilai |
|---|---|
| Idle timeout | 30 menit |
| Absolute expiration | 24 jam sejak login |
| Rotasi berkala | 15 menit |
| Grace period ID lama | 60 detik |

ID session rotasi kalau login, user dijadikan Platform Admin, user masuk group, akses group ke database diberikan atau berubah, dan tiap 15 menit. Absolute Expiration tidak terpengaruh.

## Database Viewer

User biasa buka `/databases` buat lihat database yang bisa diakses lewat group-nya. Dari situ bisa lihat schema, table/view, kolom, constraint, index, dan isi data (50 baris per halaman, urut primary key kalau ada). Semua query viewer jalan di transaction read-only dengan `statement_timeout` 10 detik.

Kalau database target ga bisa dihubungi, response-nya 502 tanpa nampilin detail koneksi. User tanpa akses ke database dapat 403 (termasuk Platform Admin, karena admin bukan tingkat akses database).

## Migration

| | Aturan |
|---|---|
| Lihat riwayat & detail | View Only atau View + Edit |
| Buat, ubah, hapus, jalankan | View + Edit |
| Versi | Nomor urut per database (v1, v2, ...) |
| Pending | Maksimal satu migration yang belum sukses per database |
| Setelah sukses | Tidak bisa diubah atau dihapus |

- Riwayat linear: migration baru cuma bisa dibuat kalau migration sebelumnya sudah sukses, jadi urutan versi = urutan eksekusi.
- Migration yang gagal tetap tercatat (status `failed` + pesan error) sampai diperbaiki lalu dijalankan ulang dengan versi yang sama, atau dihapus.
- SQL dijalankan dalam satu transaction di database target dengan `statement_timeout` 60 detik. Kalau semua statement berhasil di-commit, kalau ada yang gagal di-rollback semua.
- SQL tidak boleh berisi `BEGIN`/`COMMIT`/`ROLLBACK`/`END` karena transaction-nya diatur sistem. Statement yang memang tidak bisa jalan di dalam transaction (misal `CREATE INDEX CONCURRENTLY`) akan gagal dan di-rollback.
- Yang dicatat: versi, nama, SQL, pembuat, pelaksana, waktu eksekusi, status, dan error terakhir.
