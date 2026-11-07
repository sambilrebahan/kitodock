# Progress — Fase 2b: Dev Server, Doctor, Creator, Routes

> Status: **SELESAI** (2026-11-07) · Menutup Fase 2 penuh (2a + 2b).
> Implementasi mandiri; audit §3.

## 1. Paket baru

| Paket | Isi | Test |
| --- | --- | --- |
| `internal/devserver` | Manager dev server: Start (custom command atau `<pm> run dev`), **deteksi port 3-prioritas dari log** (app-server > binding-line > bare localhost; konflik port diabaikan; prioritas tinggi menimpa rendah, tidak sebaliknya), env builder (PATH prefix, PORT, BROWSER=none, Vite host whitelist), Stop dengan kill-tree + KillPort allowlist, ring log 500 baris | 4 (deteksi regex, prioritas penggantian, start-detect-stop dengan fixture devhost, error tanpa skrip) |
| `internal/localtls` | `Environment()` → `NODE_EXTRA_CA_CERTS` (hanya jika belum diset); `VITE_DEV_SERVER_KEY/CERT` dengan **5 gerbang** (plugin laravel-vite mendukung, user env menang, project .env menang, APP_URL hostname match, leaf verify terhadap CA); `ReadCA`/`PublicCertificates` dengan validasi IsCA + tolak private-key | 4 |
| `internal/projectroutes` | Store JSON persist; Route (canonical/preview, target main-checkout/worktree, serve mode docroot/proxy); Manager: List (materialisasi canonical default), SetCanonical/Restore (redirect domain ke worktree), CreatePreview (validasi label RFC1123 + reserved + IP rejection + hostname ≤253), Remove (canonical tidak bisa), MigrateTLD; korup store disisihkan & restart | 6 |
| `internal/projects/creator` | 8 jenis: blank/laravel/wordpress/git/vite/next/nuxt/astro; Laravel via composer create-project + **StripPailFromDevScript** (pcntl tak ada di Windows); WordPress via wordpress.org/latest.zip; JS via `<pm> create`; streaming output per-baris; sanitizeName filesystem-safe; error jujur (composer hilang, git tanpa URL, folder sudah ada) | 9 |
| `internal/sitedoctor` | Check 12 pemeriksaan (docroot, runtime, vendor, node_modules, .env, APP_KEY, storage link junction-aware, vhost drift, hosts, php-fpm, database, migrations); severity OK/Failed/Warning/Optional/NotChecked; setiap masalah punya FixID; diagnose-only by design | 5 |

## 2. Wiring desktop

- **SSL transaksional**: ApplyProject kini snapshot hosts → tulis vhost → cert → reload → **verifikasi web server mendengarkan**; gagal = rollback penuh (file vhost + hosts dipulihkan, varian plain ditulis ulang) — proyek tetap tersaji HTTP, bukan mati.
- **Dev controller**: StartDev (resolve path, hostname, TLS env via localtls, PATH dirs runtime, PM, allowed hosts), StopDev, DevStatus/Logs/Running; event `devserver:status`; shutdown menghentikan semua.
- **Doctor controller**: Diagnose/DiagnoseAll (via serving.DoctorTarget resolver), ApplyFix untuk env/app_key/composer/node/storage_link (proses nyata dengan output streaming `doctor:output`), vhost/hosts/php_fpm delegasi ke re-apply serving.
- serving.go mengimplementasikan `controllers.DevServing` (ProjectPath/SSL/Hostname/PathDirs/CaCert/PackageManager) + `DoctorTarget` (resolve target lengkap: runtime, vendor/node_modules, vhost drift dari render vs disk, hosts, fpm running).
- Bind: Dev + Doctor controller ke Wails; stub binding tulisan tangan di wailsjs.
- Lifecycle: shutdown → Dev.StopAllDev() sebelum stack.StopAll().
- Katalog i18n en+id: +~50 kunci (dev server, doctor, creator).

## 3. Verifikasi & audit

| Pemeriksaan | Hasil |
| --- | --- |
| `go test ./...` (fresh cache) | ✅ **22 paket hijau** |
| `go vet -unsafeptr=false` | ✅ |
| gofmt | ✅ bersih |
| `wails build` | ✅ 8,1 dtk |
| Smoke-run | ✅ config tercipta |
| **Audit originalitas** | **7,3%** (turun dari 7,8%), **0 komentar prosa identik** |

## 4. Keputusan & catatan

1. **sitedoctor = diagnose-only**: fix implementation hidup di controller doctor (butuh exec), bukan di paket check — tegas dipisah seperti LaraEnv referensinya.
2. **DevServing interface di controllers**: paket controllers tidak mengimpor package main; serving mengimplementasikan interface-nya — pattern yang sama dengan ProjectServing dari 2a.
3. **SSL rollback bersifat "best-effort konsisten"**: file vhost ditulis ulang varian plain + hosts dipulihkan dari snapshot; **tidak** menyalin DACL sertifikat (file cert adalah file baru, bukan mengganti yang ada — tidak ada permission untuk dipertahankan).
4. projectroutes belum ter-wire ke serving (preview worktree route → vhost) — itu pekerjaan Fase 6 (agents) yang membutuhkannya; store + manager + migrasi TLD siap.
5. Creator belum ter-wire ke UI (butuh form modal di frontend) — controller baru menyusul bersamaan dengan halaman Projects yang membutuhkan form create; **implementasi paket + test sudah hijau**.
6. Meta-test no-bare-strings frontend: **tetap utang** (tiga kali tertunda; sudah dicatat di fase-2a §5 dan masih relevan — dikerjakan di awal Fase 3 saat frontend terminal menghadirkan banyak string baru).

## 5. Sisa untuk Fase 3 (Terminal)
Semua Fase 2 (a+b) selesai. Sisa yang tercatat:
- Wire creator ke UI (form create project) — dapat digabung dengan Fase 3 terminal work.
- Wire projectroutes preview → serving — menunggu agents (Fase 6) yang membuat worktree.
- Meta-test no-bare-strings frontend.
