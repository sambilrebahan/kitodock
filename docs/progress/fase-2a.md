# Progress — Fase 2a: Runtimes, Detection, Serving

> Status: **SELESAI** (2026-11-07) · Bagian pertama Fase 2 sesuai izin
> pecah 2a/2b di roadmap. Implementasi mandiri; audit di §3.

## 1. Yang dibangun

| Paket / lapisan | Isi | Bukti |
| --- | --- | --- |
| `internal/php` | Manager multi-versi (junction `current`, urut numerik, versi dari resource PE), `EnsureIni` (extension_dir absolut + ekstensi terbundel aktif, hanya bila DLL-nya ada, idempoten), skema port FPM `9000+maj*10+min` + stride worker 100, nama upstream `php_8_4` | 5 test |
| `internal/nodejs` | Manager multi-versi mirror php (junction, versi dari PE, CompareVersions numerik) | 3 test |
| `internal/projects` | Deteksi (laravel = artisan+laravel/framework; wordpress = wp-config/wp-load; framework JS + package manager dari package.json/lockfile), **mesin constraint** semver-subset jujur: op `^ ~ >= <= > < != =` + `||`, semantik sumber (composer "8.3"=eksak vs .php-version "8.3"=seri), status resolved/missing/unmanaged/invalid/incompatible + saran InstallVersion, validasi override manual tak boleh bertentangan; Scanner merge preferensi SSL | 10 test |
| `internal/vhost` | Spec satu → **3 renderer** (nginx: proxy dual-stack ::1 backup + websocket upgrade / PHP try_files+fastcgi_pass upstream / static; apache FallbackResource+proxy:fcgi; caddy `http://` eksplisit anti-ACME, tls reuse cert), **upstream PHP terkelola** blok sentinel idempoten di dalam http{}, **CA lokal** RSA-4096 20 thn + leaf 2048 SAN host+wildcard+loopback (rantai diverifikasi x509, leaf korup diganti otomatis), **hosts editor** marker `# kitodock` hanya sentuh baris bertanda (alias tetangga selamat), trust certutil + status | 9 test |
| `desktop/serving.go` | Pool php-cgi per versi terdaftar sebagai service `Internal` (4 worker, restart-diri), sync blok upstream nginx, ApplyProject (render 3 file vhost atomik + rollback, hosts, leaf cert saat SSL, jalankan pool, reload web server), RemoveProject, **Sweep startup** self-heal | via live |
| `desktop/controllers/projects.go` | ListProjects (merge scan+preferensi+resolusi; saran install; masalah override), SetProjectSSL/Php (re-apply), ListPhpSeries, SetProjectsTLD (migrasi: remove-semua → persist → apply-semua), OpenProjectURL, Rescan, kartu CA (status+trust) | build |
| Frontend | Halaman **Projects nyata**: kartu per proyek (badge jenis/framework/PM, versi PHP ter-resolve + pin 📌, masalah constraint + tautan tips install), toggle SSL, buka URL, TLD inline dengan konfirmasi migrasi, rescan per proyek; stub binding Projects; +30 kunci i18n en/id | 12/12 vitest, build, lint |

## 2. Verifikasi

- `go test ./...` **17 paket hijau** · `go vet -unsafeptr=false` bersih · gofmt bersih.
- Frontend vitest 12/12 · eslint · `tsc && vite build`.
- `wails build` → `Kitodock.exe` (9,5 dtk) · smoke-run root sementara: config ✓, nol proses bocor.
- **Live test mahkota** (`TestLiveServePhpThroughNginx`, `KITODOCK_LIVE=1`): unduh PHP 8.4.25 + nginx dari katalog → EnsureIni → pool php-cgi 2 worker di bawah supervisor → konfig nginx via renderer vhost + `EnsurePhpUpstreams` → **HTTP mengembalikan `KITODOCK-PHP-8.4.25` dari PHP sungguhan** (1,7 dtk). Bug ditemukan & diperbaiki selama test: penulisan upstream ganda di test sendiri (alur produksi EnsurePhpUpstreams adalah jalur tunggal).

## 3. Audit originalitas
432 baris non-trivial identik dari ±5.500 (**7,8%**, turun dari 8,6) — seluruhnya import/idiom/framework. **Komentar prosa identik: 0** (satu doc-comment `Projects` tertangkap audit dan langsung ditulis ulang).

## 4. Keputusan & catatan

1. **Node dipakai untuk deteksi/penampilan versi di kartu proyek; proxy dev-server (mode KindProxy di renderer sudah siap) menyusul bersama devserver di 2b.**
2. Sweep startup menjalankan RegisterPhpPools — pool php-cgi hanya hidup ketika ada proyek PHP yang disajikan (perilaku `ensureFpmRunning`); tidak ada pool nganggur.
3. Hosts editor hanya menulis baris bermarker — satu langkah elevasi berfungsi karena app memang minta admin sejak Fase 1.
4. Override manual divalidasi terhadap constraint proyek (incompatible = ditolak dengan penjelasan, bukan diterima diam-diam).
5. CA: leaf korup/kedaluwarsa/tanpa-kunci diganti otomatis; CA tanpa kunci **ditolak** (tidak pernah diregenerasi diam-diam di atas sertifikat yang ada).

## 5. Sisa Fase 2 (masuk 2b, sesuai roadmap)
- `devserver` (npm run dev, deteksi port dari log 3-prioritas, proxy websocket) + proyek JS memakai KindProxy.
- `projectroutes` (canonical + preview worktree) — diperlukan agen Fase 6 juga.
- Creator proyek 8 jenis + PostSetupLaravel + Site Doctor inti.
- `localtls` (NODE_EXTRA_CA_CERTS / VITE_DEV_SERVER_*) bersama devserver.
- Meta-test no-bare-strings frontend (utang Fase 0 — tetap tercatat).
- Tuning forms nginx/apache/php di Settings.
