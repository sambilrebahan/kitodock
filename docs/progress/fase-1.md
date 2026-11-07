# Progress — Fase 1: Services & Installer

> Status: **SELESAI** (2026-11-07, re-audit 2026-11-07) · Implementasi
> mandiri penuh; LaraEnv hanya referensi perilaku. Audit originalitas §4.

## 0. Re-audit DoD (2026-11-07, setelah penutupan)

Pemilik meminta pengecekan ulang sebelum Fase 2. Temuan dan penutupannya:

| Temuan audit | Penutupan |
| --- | --- |
| CI di GitHub belum dikonfirmasi | ✅ Actions run #1 & #2 (tip 2db8d26) **success** — sekaligus menutup item tertunda Fase 0 |
| Label tray di-mutasi dari goroutine watcher | ✅ Diverifikasi aman: `systray.MenuItem.update` berkunci global dan terdokumentasi goroutine-safe |
| **Belum ada layanan nyata melalui supervisor** (live nginx lama memakai exec langsung) | ✅ Test live baru `desktop/stack_live_test.go`: redis & mailpit **install → Start (first-run) → listener terbukti via netports → Stop → port bebas, nol proses bocor** |
| Stop bisa diam-diam gagal (mailpit run pertama menyisakan orphan) | ✅ `stopLocked` diperkuat: killTree → tunggu exit per-replika (3 dtk) → fallback `killImage` milik-sendiri → tunggu lagi; suite services 2× run hijau |
| CDN MySQL menolak UA "kitodock" (403; curl/Go-default lolos — WAF per-nama) | ✅ Field `PlainUA` pada Source: host tersebut dikirim tanpa UA kustom |
| **Bug basedir MySQL**: `dirOf(exe)` = `\mysql\bin` → basedir/datadir salah (errmsg.sys under `bin\`, data di `bin\data`) | ✅ Root dihitung dua tingkat dari exe; data directory kini tercipta di `\mysql\data` — dibuktikan live |
| Modal VC++ tanpa tombol unduh (kunci `vc.download` nganggur) | ✅ Alert kini memakai `confirmLabel: vc.download` → `BrowserOpenURL(redist)` |
| Judul alert gagal-install pakai template cacat | ✅ Judul jadi `<id> <version>`; kunci mati `downloads.failed` dihapus dari kedua katalog |
| Test live mengecek "port 3306 listening" tanpa kepemilikan | ✅ Kepemilikan PID diperiksa: **mesin ini menjalankan MySQL layanan-sistem di :3306** → serving-MySQL SKIP jujur (inisialisasi first-run tetap TERBUKTI: data dir dibuat mysqld kita); konflik port vs layanan sistem = utang Fase 2 |

Hasil akhir re-audit: 14 paket go test ✅ · vet ✅ · frontend 12/12+lint+build ✅ · wails build ✅ · smoke ✅ · nol proses bocor (nginx/redis/mailpit) · audit originalitas **8,6%, 0 komentar identik**.

## 1. Yang dibangun

### Paket internal baru (semua kode orisinal)
| Paket | Isi | Test |
| --- | --- | --- |
| `runtimeinventory` | Versi binari dari resource PE (`GetFileVersionInfo`), cache per-path, klasifikasi prerelease dari flag VS_FF | 3 (termasuk baca exe test sendiri) |
| `vcruntime` | Probe pra-start `<exe> --version` → klasifikasi `Missing` (DLL hilang 0xC0000135 / redist basi via pesan linker); URL aka.ms vs17/vs18; frase produk tetap bahasa Inggris (kontrak UI) | 6 dengan fixture `testdata/probe` |
| `netports` | Tabel TCP listener langsung dari `GetExtendedTcpTable` (IPv4+IPv6, hanya LISTEN), nama proses via Toolhelp32, `KillPort` dengan allowlist nama-exe wajib | 4 (listener fixture nyata: temuan, tolak tanpa allowlist, singkirkan asing, izinkan milik) |
| `services` | Supervisor generik: Definition ber-fungsi-lazy, grup eksklusif ("web"), dependency start/stop kaskade dengan cek dependen lain, first-run Setup sekali, probe VC++ opsional (`SmokeProbe`), pool Replicas, auto-restart backoff 2× (500ms→8s, menyerah setelah 8 kematian cepat) hanya untuk layanan `Internal`, ring log 500 baris, KillStrays pembersih sisa run lama | 9 — siklus hidup+log, konflik grup menyebut penghalang, binary hilang = keadaan bukan error, setup tepat sekali, dependensi ikut naik dan turun saat dependen terakhir berhenti, crash-loop menyerah + dilaporkan, layanan user tidak dihidupkan ulang diam-diam, tree-kill membunuh anak, stray tersapu |
| `installer` | Katalog pin tervalidasi URL (nginx 1.28.0, caddy 2.10.2, mysql 8.0.43, postgres 17.6 Manual, redis 5.0.14.1, mailpit 1.33.0, composer 2.8.9 Direct+wrapper, php 8.5.12/8.4.25/8.3.36, node 24.22.2), unduh `.part`+ticker 250ms+hitung-kecepatan, validasi magic `PK`, cache %TEMP%\kitodock-cache, ekstrak anti zip-slip/absolut/drive-letter (menolak, bukan menulis-ulang), Flatten satu direktori pembungkus, marker `.kitodock-version`, sumber Manual menuntut arsip di cache, dialog arsip via Wails | 13 + 1 live |
| `sysmetrics` | CPU via PDH (`\Processor(_Total)\% Processor Time`, query persisten + priming), RAM via `GlobalMemoryStatusEx`, disk via `GetDiskFreeSpaceEx` — tanpa dependensi pihak ketiga | 1 (kewajaran nilai) |

### Lapisan desktop
- `stack.go`: definisi nyata nginx/caddy/mysql/postgres/redis/mailpit — path lazy via `paths`, grup web eksklusif, first-run: mysqld `--initialize-insecure`, initdb trust+UTF8, redis.windows.conf (port 6379 bind 127.0.0.1), Caddyfile `import sites/*`; `runSetupStep` dengan timeout 180 dtk dan log mengalir ke ring.
- `tray.go`: systray (energye/systray) — judul layanan = aksi ("Hentikan MySQL"), start/stop semua, tampilkan, keluar; `RelabelTray` saat bahasa/status berubah.
- `lifecycle.go`: startup menyapu stray, watcher status 1,5 dtk → event + relabel tray, ticker sys:metrics 1,5 dtk; `beforeClose` menyembunyikan ke tray kecuali flag quitting; keluar dari tray menghentikan semua layanan lalu menutup.
- `main.go`/elevation: relaunch UAC `runas` aktif (env `KITODOCK_SKIP_ELEVATE` untuk CI/dev), embed icon tray.
- `controllers/stack.go`: `Installer` (ListSources+keadaan, InstallSource/FromArchive async dengan event, OpenSourcePage) dan `Services` (kartu dasbor difilter Stack.Enabled, Start/Stop/StartVisible/StopVisible, log, toggle tampil, BootstrapRecommended nginx+mysql+mailpit async, error terlokalisasi `errors.services.*`).
- `controllers/system.go`: `ListPorts`, `KillPort` (allowlist gambar-proses stack tetap), `GetSystemMetrics`.
- `controllers.InstallCatalogues` menjadi fungsi bebas — method ter-bind membuka panggilan yang memutasikan state global bahasa dari JS.

### Frontend
- **Dashboard nyata**: header + start/stop semua, kartu starter stack (bootstrap sekali klik), grid kartu layanan (titik status, versi dari resource PE, tombol start/stop + log), panel metrik CPU/RAM/disk bergaya bar, modal log auto-refresh 1,2 dtk, modal VC++ via `service:error` ber-redist.
- **Libs/Tools nyata**: grup per Kind, kartu sumber (terpasang / pasang dengan progress dua fase / Manual: buka halaman + pasang dari berkas), toggle tampil-di-dasbor, event progress selesai membersihkan sendiri.
- **PortsModal** dari titlebar: tabel port/addr/PID/proses refresh 3 dtk, bebaskan port dengan konfirmasi bahaya.
- Katalog i18n en+id bertambah ~90 kunci (layanan, dasbor, unduhan, log, port, tray, error layanan).

## 2. Verifikasi (semua dijalankan)

| Pemeriksaan | Hasil |
| --- | --- |
| `go test ./...` — 13 paket | ✅ |
| `go vet -unsafeptr=false ./...` | ✅ bersih |
| vitest 12/12 · eslint · `tsc && vite build` | ✅ |
| `wails build` → Kitodock.exe | ✅ (11 dtk) |
| Smoke-run (SKIP_ELEVATE + root sementara) | ✅ config tercipta, WebView2 jalan, tidak ada proses layanan bocor |
| **Live chain nginx** (`KITODOCK_LIVE=1`) | ✅ unduh pin 1.28.0 → ekstrak flatten → start dengan config scratch-port → **HTTP 200 dari nginx sungguhan** (0,96 dtk) |
| Audit originalitas | ✅ §4 |

Catatan live: port 80 di mesin ini terkunci http.sys/WinNAT (10013) — test live memakai port scratch dengan config minimal; perilaku di app tetap :80 via config bawaan, dan eksklusi port seperti itu akan ditangani panel Port / deteksi Fase 2.

## 3. Keputusan & penyimpangan

1. **SmokeProbe opsional, default mati**: probe `--version` universal ternyata salah untuk binari yang mengabaikan flag (hang 5 dtk). Hanya definisi dengan `SmokeProbe` yang mem-probe (dipakai nanti untuk pool PHP Fase 2).
2. **Dua deadlock/race ketemu test, bukan produksi**: (a) restart crash-loop tidak pernah mengganti instance mati dari map running; (b) `stopLocked` menghapus flag stop sebelum handler exit terbangun. Keduanya ditambal dengan urutan hapus-instance → keputusan restart, dan flag stop bertahan sampai Start berikutnya.
3. **MySQL 8.0 (bukan 9.x)**: pin 8.0.43 terverifikasi 200 dan tanpa perubahan autentikasi besar; 9.x bisa masuk pin menyusul. PostgreSQL tetap Manual (EDB memblokir unduhan skrip).
4. **GPU dibolehkan menunggu**: sysmetrics Fase 1 = CPU+RAM+disk (native murni); penghitung "GPU Engine" PDH menyusul bersama kartu GPU di dasbor.
5. **BeforeClose = sembunyi ke tray**; keluar sejati lewat tray/flag — sesuai roadmap.
6. `wailsjs` stub diperbarui tangan (Services/Installer/System baru); `wails build` menimpa dengan binding asli — terverifikasi lagi pasca-build.
7. Live test butuh env sejak init (`paths` resolve root di init): pola `KITODOCK_LIVE=1 KITODOCK_ROOT=$(mktemp -d) go test ... -run Live` — dicatat di header test; CI belum menjalankannya (opsi: tambah step env — keputusan pemilik repo).

## 4. Audit originalitas
337 baris non-trivial identik dari ±3.810 baris kitodock (**8,8%**), **0 komentar/prosa identik**. Sisa tumpang tindih tetap terklasifikasi import/framework/idiom satu-baris/tag skema (pola Fase 0, proporsional turun).

## 5. Utang yang dicatat
- Fase 2: pool php-fpm per versi (ReplicaArgs sudah tersedia), vhost+CA+hosts (butuh elevasi jalan penuh), resolusi constraint, Site Doctor.
- Meta-test no-bare-strings frontend (utang Fase 0) belum ditulis — makin tertinggal, makin banyak string yang harus di-sweep; kerjakan awal Fase 2.
- GPU engine counter + kartu GPU dasbor.
- CI step untuk live test (keputusan: jalan atau tidak di runner).
