# Worker Migration (Track 3)

Worker mengambil job dari tabel `migration_jobs`, mengunci database target, memeriksa ulang syaratnya, menjalankan SQL (mode normal atau per batch), lalu mencatat hasilnya ke `migration_executions`. API hanya meng-enqueue job dan langsung membalas `202`; SQL tidak pernah dijalankan di dalam request HTTP.

```
API enqueue → migration_jobs (pending) → worker claim → advisory lock → revalidasi
            → eksekusi (normal / batch + checkpoint) → commit/rollback → status akhir → unlock
```

## Menjalankan

```bash
DATABASE_URL="postgres://user:pass@localhost:5432/wbd" \
DATABASE_CREDENTIALS_KEY="<64 karakter hex, sama dengan API>" \
WORKER_CONCURRENCY=4 \
go run ./cmd/worker
```

| Variabel | Wajib | Keterangan |
|---|---|---|
| `DATABASE_URL` | ya | database platform (sama dengan API) |
| `DATABASE_CREDENTIALS_KEY` | ya | kunci AES untuk membuka DSN database target |
| `WORKER_CONCURRENCY` | tidak | maksimal job paralel (database berbeda), default 4 |

- Log JSON ke **stdout**, span OpenTelemetry (console exporter) ke **stderr**.
- Worker menjalankan `schema.sql` saat start. Aman bersamaan dengan API karena `schema.sql` diawali `pg_advisory_xact_lock(3110, 0)`.
- `Ctrl+C` / `SIGTERM`: worker berhenti mengambil job baru dan menunggu job yang sedang jalan selesai.
- Beberapa proses worker boleh jalan bersamaan.

## Concurrency dan lock

| Mekanisme | Fungsi |
|---|---|
| `FOR UPDATE OF j SKIP LOCKED` saat claim | dua worker tidak pernah mengambil job yang sama |
| `pg_try_advisory_lock(3110, database_id)` | satu database hanya menjalankan satu migration; database lain tetap diproses paralel. Kalau database sibuk, worker langsung mencari job untuk database lain (tidak menunggu) |
| satu koneksi platform khusus per job | koneksi ini memegang lock dari claim sampai status akhir di-commit, termasuk di antara commit batch |
| urutan selesai | status akhir job + execution di-commit **dulu**, baru `pg_advisory_unlock` |

Kode: [`internal/worker/claim.go`](../internal/worker/claim.go), [`internal/worker/worker.go`](../internal/worker/worker.go).

### Pemulihan lock ketika worker gagal

Advisory lock dipegang di level **session**. Kalau proses worker mati (crash, di-kill, mesin mati), koneksinya putus dan PostgreSQL **melepas lock-nya otomatis**. Akibatnya:

1. Job yang tertinggal berstatus `running` tetapi lock database-nya **bisa diambil** pasti tidak sedang dikerjakan siapa pun.
2. Saat start dan setiap 30 detik, worker menjalankan `Recover`: ambil lock database → cek ulang `status = 'running'` dengan `FOR UPDATE` di bawah lock → kembalikan job dan execution ke `pending` (`started_at = NULL`).
3. Saat dijalankan ulang, worker memeriksa **bukti commit** di tabel `_wbd_checkpoints` database target sebelum menjalankan SQL:
   - mode normal: checkpoint `(req_id, 1)` sudah ada → SQL sudah ter-commit sebelum crash, langsung dicatat `success` tanpa dijalankan ulang;
   - mode batch: lanjut dari batch terakhir yang ter-commit.

Job milik worker yang masih hidup tidak pernah diambil alih karena lock-nya masih dipegang. Kode: [`internal/worker/recover.go`](../internal/worker/recover.go).

## Revalidasi sebelum eksekusi

Setelah lock didapat, syarat diperiksa ulang karena kondisi bisa berubah sejak pengajuan disetujui ([`revalidate.go`](../internal/worker/revalidate.go)):

| Cek | Gagal → |
|---|---|
| (a) semua group yang **saat ini** punya akses ke database sudah approve `req_id` | `failed`, ajukan ulang |
| (b) database terblokir: ada `partial`/`partially_reverted` yang belum selesai, atau eksekusi lain `running` (kecuali job ini `retry`/`compensate` untuk root tersebut) | requeue (dicoba lagi 2 detik kemudian) |
| (c) `up`/`forward`: versi di antara versi aktif dan v masih antre | requeue |
| (c) `up`/`forward`: versi di antara masih `pending_review`/`approved`, `up` yang sudah pernah sukses, ada draft terlewati, `forward` untuk migration yang tidak sedang di-rollback | `failed` |
| (c) `rollback`: bukan versi aktif terakhir atau `reverted_execution_id` bukan eksekusi aktif | `failed` |
| (c) `retry`/`compensate`: root tidak punya partial yang belum selesai | `failed` |

Versi aktif dihitung dari `migration_executions` (eksekusi `up`/`forward`/`retry` sukses yang belum dibatalkan rollback/compensate sukses), bukan dari `migrations.status`.

## Mode eksekusi

### Normal (`batch_size = 0`)

Seluruh `sql_snapshot` dijalankan dalam satu transaksi di database target, bersama `INSERT` checkpoint `(req_id, batch_no = 1)`. Semua berhasil → commit; ada yang gagal → rollback total (termasuk checkpoint). Dipakai untuk `up`, `forward`, dan `rollback`.

### Batch (`batch_size > 0`)

Setiap batch adalah transaksi sendiri. Checkpoint batch ditulis di transaksi yang sama dengan perubahan datanya, dengan key `root_id = COALESCE(parent_execution_id, request_id, id)`.

**SQL up** menerima `$1` = `last_cursor` (NULL di batch pertama) dan `$2` = `batch_size`, lalu mengembalikan satu kolom cursor terurut. Worker berhenti saat batch mengembalikan 0 baris.

```sql
WITH batch AS (
    SELECT id FROM items
    WHERE ($1::bigint IS NULL OR id > $1::bigint)
    ORDER BY id LIMIT $2
), upd AS (
    UPDATE items SET status = 'migrated'
    FROM batch WHERE items.id = batch.id
    RETURNING batch.id
)
SELECT id::text AS last_cursor FROM upd ORDER BY id
```

**SQL down** menerima `$1` = `prev_cursor` (NULL untuk batch 1, eksklusif) dan `$2` = `last_cursor` (inklusif):

```sql
UPDATE items SET status = 'new'
WHERE ($1::bigint IS NULL OR id > $1::bigint) AND id <= $2::bigint
```

| Kejadian | Status |
|---|---|
| semua batch selesai | `success` |
| batch ke-n gagal setelah ≥1 batch commit | `partial` (batch sebelumnya tetap committed) |
| batch pertama gagal | `failed` |
| `retry` atas `partial` | lanjut dari checkpoint terakhir tanpa mengulang batch yang sudah commit |
| `rollback`/`compensate` | `sql_down` per batch dari `batch_no` terbesar ke terkecil; checkpoint batch dihapus di transaksi yang sama |
| pembatalan gagal setelah ≥1 batch dibatalkan | `partially_reverted` (lanjutkan dengan `compensate`) |

Progres (`batches_committed`, `rows_processed`, `last_cursor`) diperbarui setelah setiap batch, sehingga SPA dapat memantau job yang sedang berjalan.

## Retry otomatis

Kegagalan sementara (database target tidak bisa dihubungi, koneksi putus `08xxx`, deadlock `40P01`, serialization failure `40001`, lock tidak tersedia `55P03`, server restart) di-retry otomatis sampai `migration_jobs.max_attempts` (default 3), dengan jeda 2s, 4s, 8s, … maksimal 1 menit.

Riwayat bersifat **append-only**: attempt yang gagal ditutup `failed` dan tidak pernah ditimpa; attempt baru dibuat dengan `request_id = req_id` dan `attempt + 1`, sehingga approval tetap berlaku dan checkpoint batch tetap tersambung. Kesalahan SQL (tabel tidak ada, syntax error, constraint) tidak di-retry.

## Logging dan tracing

Semua log berupa JSON (`log/slog`) di stdout. Password, DSN, cookie, dan session identifier disamarkan (`[REDACTED]`), termasuk yang terselip di pesan error.

| Event | Isi utama |
|---|---|
| `http.request` | method, path, route, status, durasi |
| `sql.query.start` / `.end` | query, parameter (disamarkan untuk kolom rahasia), durasi, jumlah baris, `sqlstate`, `actor`, `db` (`platform`/`target`) |
| `sql.begin` / `sql.commit` / `sql.rollback` | dicatat terpisah dari query biasa |
| `migration.attempt.start` / `.end` | `database_id`, `job_id`, `migration_id`, `execution_id`, `req_id`, `version`, `operation`, `attempt`, `actor`, `status`, `sql_snapshot`, durasi, batch/baris, info retry |
| `migration.batch_committed` / `migration.batch_reverted` | nomor batch, jumlah baris, cursor |
| `migration.requeued`, `worker.crash_recovered` | alasan penundaan / pemulihan |

`actor` diambil dari session login di API, dan dari `migration_executions.requested_by` di worker. Setiap log di dalam span membawa `trace_id` dan `span_id`.

Trace context (`traceparent` W3C) disimpan di `migration_jobs.traceparent` saat enqueue dan dilanjutkan oleh worker, sehingga satu trace mencakup seluruh alur (diambil dari [`samples/worker-spans.jsonl`](samples/worker-spans.jsonl)):

```
POST /api/databases/{id}/executions/{eid}/enqueue      ← span request API
└── worker.job
    └── worker.lock.held                               ← masa advisory lock dipegang
        └── migration.execute
            ├── worker.revalidate
            ├── sql.query  (siapkan _wbd_checkpoints, baca progres)
            ├── migration.batch #1 → sql.begin, sql.query, sql.query (checkpoint), sql.commit
            ├── migration.batch #2 → …
            ├── migration.batch #3 → …
            └── migration.batch #4 → sql.begin, sql.query (0 baris), sql.rollback
```

Contoh output lengkap: [`samples/worker-logs.jsonl`](samples/worker-logs.jsonl) dan [`samples/worker-spans.jsonl`](samples/worker-spans.jsonl). Cuplikan log akhir attempt:

```json
{"level":"INFO","msg":"migration.attempt.end","database_id":1,"job_id":2,"migration_id":1,"execution_id":1,"req_id":1,"version":1,"operation":"up","attempt":1,"actor":"ani","status":"success","sql_snapshot":"WITH batch AS (...)","duration_ms":80.598,"batches_committed":3,"rows_processed":25,"trace_id":"e265d96b0dd954d36992cfd52def0d27","span_id":"02df081e4967fed5"}
```

## Benchmark data besar

Dataset: tabel `items` berisi **2.000.000 baris** (`id bigint PRIMARY KEY, status text, payload text`). PostgreSQL 17 di Docker Desktop (WSL2), diukur 26 November 2026.

| Skenario | Hasil |
|---|---|
| `UPDATE items SET status = 'migrated'` satu query tanpa batch | **16,0 detik** untuk 2.000.000 baris |
| dataset yang sama lewat worker mode batch, `batch_size = 50000` | **56,0 detik**, 40 batch, 2.000.000 baris, status `success` |

Analisis:

- Syarat spek terpenuhi: UPDATE tanpa batch butuh jauh lebih dari 1 detik.
- Total waktu mode batch lebih lama (overhead commit + checkpoint per batch dan pencarian cursor per batch). Yang didapat sebagai gantinya: setiap transaksi hanya sekitar 1,4 detik dan 50.000 baris, sehingga lock baris dan WAL per transaksi kecil; progres terlihat di SPA selama proses; kalau gagal di tengah, batch yang sudah commit tidak hilang dan bisa dilanjutkan (`retry`) atau dibatalkan per batch (`compensate`).
- Data tidak pernah dimuat ke memori worker; worker hanya membaca kolom cursor dari batch yang sedang berjalan.

Reproduksi:

```bash
WBD_BENCH_ROWS=2000000 WBD_BENCH_BATCH=50000 \
TEST_DATABASE_URL="postgres://wbd:wbd@localhost:54329/wbd?sslmode=disable" \
go test ./internal/worker -run TestLargeDatasetBenchmark -v -timeout 30m
```

## Testing

Unit test (tanpa database):

```bash
go test ./internal/telemetry ./internal/worker
```

Integration test butuh PostgreSQL. Dengan Docker:

```bash
docker run -d --name wbd-test-pg -e POSTGRES_USER=wbd -e POSTGRES_PASSWORD=wbd -e POSTGRES_DB=wbd -p 54329:5432 postgres:17-alpine
TEST_DATABASE_URL="postgres://wbd:wbd@localhost:54329/wbd?sslmode=disable" go test ./internal/worker -v
```

Setiap test membuat schema acak (`wrk_…` untuk platform, `tgt_…` untuk target) lalu menghapusnya. Cakupan:

| Area | Test |
|---|---|
| claim, lock, requeue, loop | `claim_test.go` |
| mode normal, bukti commit, log attempt | `execute_test.go` |
| mode batch, partial, retry dari checkpoint | `batch_test.go` |
| rollback/compensate batch, partially_reverted | `reverse_test.go` |
| revalidasi approval, blocking, urutan versi | `revalidate_test.go` |
| retry otomatis append-only, backoff | `retry_test.go` |
| crash recovery | `recover_test.go` |
| trace API → queue → worker | `trace_test.go` |
| redaksi log, QueryTracer, HTTP middleware | `internal/telemetry/*_test.go` |

Contoh output log & span dibuat ulang dengan:

```bash
WBD_SAMPLE_DIR=$PWD/docs/samples TEST_DATABASE_URL=... go test ./internal/worker -run TestSampleTelemetry
```
