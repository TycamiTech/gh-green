# gh-green

> **Automated GitHub Activity Simulator via REST API with Zero External Dependencies.**

`gh-green` adalah CLI tool berbasis bahasa Go (Golang) yang dirancang untuk melakukan automated commit ke repository GitHub menggunakan GitHub REST API langsung tanpa memerlukan instalasi binary `git` lokal atau proses cloning repository. Tool ini dilengkapi simulasi perilaku manusia yang natural (anti-bot pattern) agar riwayat kontribusi tampak organik.

---

## Fitur Utama

- **Zero External Dependencies**: Menggunakan 100% Go standard library (`net/http`, `crypto/rand`, `encoding/json`, `encoding/base64`, `bufio`, `flag`, dll).
- **Built-in `.env` Parser**: Membaca file konfigurasi `.env` secara native tanpa memerlukan library pihak ketiga.
- **Simulasi Perilaku Manusia (Anti-Bot Pattern)**:
  - **Skip Probability**: Peluang libur/istirahat acak harian (`SKIP_PROBABILITY`) untuk mensimulasikan hari off / bolong yang alami di grafik kontribusi.
  - **Initial Jitter Delay**: Jeda acak (1 hingga `MAX_JITTER_MINUTES` menit) sebelum commit pertama, mencegah pola eksekusi di menit yang sama setiap hari saat dijalankan via cron.
  - **Random Commit Count**: Menghasilkan 1 hingga `MAX_COMMITS` commit per sesi eksekusi.
  - **Inter-Commit Pause**: Jeda acak antara 2 hingga 12 menit di antara setiap commit dalam sesi multi-commit.
- **Dynamic SHA Syncing**: Re-fetch `sha` file terbaru dari GitHub sebelum tiap commit untuk mencegah HTTP 409 Conflict.
- **100% Go Linguist Code Payload**: Menghasilkan file kode Go yang valid secara sintaksis (`internal/telemetry/state.go`) dengan runtime snapshot, build hash acak, counter iterasi bertingkat, dan stempel waktu RFC3339.
- **Realistic Conventional Commits**: 40+ variasi commit message bergaya *Conventional Commits* (`feat`, `fix`, `refactor`, `perf`, `chore`, `docs`, `test`) bernuansa backend dan system engineering.
- **Structured Logging**: Log konsol berstempel waktu ISO 8601 / RFC3339 dengan tag level (`[INFO]`, `[SUCCESS]`, `[WARN]`, `[ERROR]`, `[DRY-RUN]`).

---

## Konfigurasi Environment Variable

Salin `.env.example` menjadi `.env` lalu sesuaikan nilainya:

```bash
cp .env.example .env
```

| Variable | Wajib | Default | Deskripsi |
| :--- | :---: | :---: | :--- |
| `GH_TOKEN` | **Ya** | - | Personal Access Token (PAT) GitHub dengan permission `Contents: Read and write`. |
| `GH_OWNER` | **Ya** | - | Username atau nama organisasi pemilik repository target. |
| `GH_REPO` | **Ya** | - | Nama repository target di GitHub. |
| `GH_BRANCH` | Tidak | `main` | Branch target untuk commit. |
| `GH_TARGET_FILE` | Tidak | `internal/telemetry/state.go` | Path file yang di-update di repository (terdeteksi sebagai Go 100%). |
| `SKIP_PROBABILITY` | Tidak | `15` | Peluang (0 - 100%) untuk skip/libur commit hari ini. |
| `MAX_COMMITS` | Tidak | `5` | Batas maksimal jumlah commit per eksekusi (1 s/d `MAX_COMMITS`). |
| `MAX_JITTER_MINUTES`| Tidak | `90` | Batas maksimal random delay (menit) sebelum commit pertama dimulai. |

> **Catatan Token**: Disarankan menggunakan **Fine-grained Personal Access Token** GitHub dengan scoped repository tertentu dan izin:
> - **Repository permissions** -> **Contents** -> `Access: Read and write`.

---

## Panduan Penggunaan CLI Flags

```
Usage of gh-green v1.0.0:

Flags:
  --now
        Bypass probability skip dan initial jitter delay, langsung eksekusi 1 commit instan (cocok untuk verifikasi & testing).
  --commits <int>
        Paksa jumlah commit tertentu (mengabaikan pemilihan acak 1..MAX_COMMITS).
  --dry-run
        Simulasikan seluruh alur, delay, payload, dan log di terminal tanpa mengirim HTTP PUT ke GitHub API.
  --env <path>
        Tentukan custom path ke file .env (default: ".env").
```

### Contoh Perintah:

#### 1. Uji Coba Konfigurasi Tanpa Menulis ke GitHub (`--dry-run`)
Simulasikan alur kerja lengkap tanpa mengubah repository:
```bash
./gh-green --dry-run
```

#### 2. Eksekusi 1 Commit Langsung untuk Testing (`--now`)
Melewati skip probability dan jitter delay untuk langsung memverifikasi token dan koneksi GitHub API:
```bash
./gh-green --now
```

#### 3. Simulasi Dry-Run dengan Jumlah Commit Tertentu
```bash
./gh-green --dry-run --commits 3 --now
```

#### 4. Eksekusi dengan File `.env` Kustom
```bash
./gh-green --env /etc/gh-green/.env --now
```

---

## Kompilasi & Build

### 1. Build untuk Linux Server (amd64 Stripped Binary)
Menghasilkan binary independen tanpa dependensi library C (`CGO_ENABLED=0`) dengan ukuran minimal:

Menggunakan script:
```bash
chmod +x build.sh
./build.sh
```

Atau menggunakan `Makefile`:
```bash
make build-linux
```

Atau perintah langsung:
```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o gh-green main.go
```

### 2. Build untuk OS Lokal
```bash
make build
# atau
go build -o gh-green main.go
```

### 3. Menjalankan Unit Tests
```bash
make test
# atau
go test -v ./...
```

---

## Automasi Server Menggunakan Linux Crontab

Jalankan `gh-green` sekali setiap hari di server VPS / Linux Anda. Biarkan fitur bawaan `MAX_JITTER_MINUTES` dan `SKIP_PROBABILITY` bekerja otomatis untuk mengacak waktu eksekusi riil dan hari libur.

### Langkah Setup:

1. Letakkan binary `gh-green` dan file `.env` di folder server (misal `/opt/gh-green`):
   ```bash
   sudo mkdir -p /opt/gh-green /var/log/gh-green
   sudo cp gh-green /opt/gh-green/
   sudo cp .env /opt/gh-green/.env
   sudo chmod 600 /opt/gh-green/.env
   sudo chmod +x /opt/gh-green/gh-green
   ```

2. Buka crontab:
   ```bash
   crontab -e
   ```

3. Tambahkan salah satu konfigurasi jadwal berikut:

#### Opsi A: Dijalankan Setiap Pagi Pukul 08:00
*(Dengan `MAX_JITTER_MINUTES=90`, commit pertama akan terjadi secara acak antara pukul 08:01 s/d 09:30)*
```cron
0 8 * * * cd /opt/gh-green && ./gh-green >> /var/log/gh-green/gh-green.log 2>&1
```

#### Opsi B: Dijalankan Setiap Hari Pukul 13:00 (Siang Hari Kerja)
```cron
0 13 * * * cd /opt/gh-green && ./gh-green >> /var/log/gh-green/gh-green.log 2>&1
```

#### Opsi C: Menggunakan Custom Path Flag `--env`
```cron
0 9 * * * /opt/gh-green/gh-green --env /opt/gh-green/.env >> /var/log/gh-green/gh-green.log 2>&1
```

### Memeriksa Log Crontab:
```bash
tail -f /var/log/gh-green/gh-green.log
```

---

## Contoh Output Log

```log
[2026-10-29T20:14:57+07:00] [INFO] Initializing gh-green v1.0.0...
[2026-10-29T20:14:57+07:00] [INFO] Loaded 7 variables from .env
[2026-10-29T20:14:57+07:00] [INFO] Configuration target: username/my-service on branch "main" (File: internal/telemetry/state.go)
[2026-10-29T20:14:57+07:00] [INFO] Evaluating daily skip probability: 15% configured (Dice roll: 78)
[2026-10-29T20:14:57+07:00] [INFO] Waiting initial jitter delay of 34m12s before starting commit routine...
[2026-10-29T20:49:09+07:00] [INFO] Initial jitter completed. Resuming commit sequence.
[2026-10-29T20:49:09+07:00] [INFO] Random commit count selected: 2 commit(s) (1..5)
[2026-10-29T20:49:09+07:00] [INFO] --- Processing Commit [1/2] ---
[2026-10-29T20:49:10+07:00] [INFO] Target file exists in repository. Current SHA: a7b8c9d0e1f2... (Size: 1458 bytes)
[2026-10-29T20:49:10+07:00] [INFO] Generated payload iteration #24 (1460 bytes)
[2026-10-29T20:49:10+07:00] [INFO] Selected commit message: "refactor(pipeline): decouple ingestion dispatcher from worker pool"
[2026-10-29T20:49:11+07:00] [SUCCESS] Commit [1/2] successfully pushed!
[2026-10-29T20:49:11+07:00] [SUCCESS]   Commit SHA: 3d5e7f1...
[2026-10-29T20:49:11+07:00] [SUCCESS]   Commit URL: https://github.com/username/my-service/commit/3d5e7f1...
[2026-10-29T20:49:11+07:00] [INFO] Pausing for 6m45s before next commit to simulate natural developer rhythm...
[2026-10-29T20:55:56+07:00] [INFO] Pause complete. Proceeding with next commit.
[2026-10-29T20:55:56+07:00] [INFO] --- Processing Commit [2/2] ---
[2026-10-29T20:55:57+07:00] [SUCCESS] Commit [2/2] successfully pushed!
[2026-10-29T20:55:57+07:00] [SUCCESS] All 2 scheduled commit(s) successfully completed.
```

---

## Lisensi

Didistribusikan di bawah Lisensi MIT. Bebas digunakan untuk automasi dan pemeliharaan aktivitas repository.

