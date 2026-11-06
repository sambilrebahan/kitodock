# 05 — Risiko, Lisensi & Perbaikan Desain

## 1. Risk Register

| # | Risiko | Dampak | Kemungkinan | Mitigasi |
| --- | --- | --- | --- | --- |
| R1 | **Terpotong ke salinan kode LaraEnv** — kontributor masa depan "berbaik hati" menyalin blok kode dari repo referensi | Hukum (turunan Apache-2.0 + pelanggaran aturan proyek) | Rendah (aturan eksplisit) | Strategi implementasi mandiri (PLAN D5): LaraEnv hanya referensi perilaku; audit baris-identik otomatis tiap akhir fase wajib nol komentar/prosa identik (skrip di docs/progress/fase-0.md); NOTICE menyatakan tanpa kode LaraEnv |
| R2 | **Redistribusi binari pihak ketiga** (PHP, nginx, MySQL, PostgreSQL, Redis tporadowski, pgvector, mailpit, composer.phar, PECL ext) | Hukum + rilis diblokir | Sedang | Ikuti pola LaraEnv: `THIRD_PARTY_NOTICES.txt` + `dependencies.json` + teks lisensi per-vendor + audit. Perhatikan khusus: Redis Windows fork (BSD-3 + banyak notice supplemental), imagick (DLL pendamping LGPL/X11), pgvector (build sendiri bila perlu). Keputusan katalog per-item didokumentasikan sebelum masuk installer |
| R3 | WebView2 tidak tersedia / versi rendah | App tak jalan di mesin target | Rendah | Dokumentasikan prasyarat; port `webviewruntime` recovery (Fase 6); pesan error jelas saat startup |
| R4 | Aplikasi berjalan elevated (admin) — salah kill proses / edit hosts berbahaya | Kerusakan sistem user | Sedang | Kebijakan LaraEnv dipertahankan: allow-list exe untuk KillPort/KillPort tree; hanya baris hosts bertanda `# kitodock` yang disentuh; validasi `nginx -t` sebelum restart; rollback transaksional vhost/SSL |
| R5 | Konflik port (80/443/3306/5432/6379/8025/9000-99xx dipakai aplikasi lain) | Service gagal start | Tinggi di mesin developer | Port modal (transparansi pemilik port via netports), pesan konflik eksklusif-group yang jelas, skema port FPM deterministik + stride replica |
| R6 | Provider AI agent berubah cepat (Codex 0.160 protocol, path config, flags CLI) | Fitur agents rusak diam-diam | Tinggi | Test fixture sintetis per provider (pola LaraEnv testdata); gating versi (userAgent); fitur agents selalu opsional/off-default |
| R7 | Sumber unduhan upstream berubah/hotlink-diblokir (Apache Lounge, EnterpriseDB) | Installer gagal | Sedang | Dukung mode `Manual` (unduh sendiri → install-from-file), mirror URL kandidat, validasi konten (magic zip, tolak HTML interstitial) |
| R8 | monolit kambuh — controller baru membesar jadi "app.go kedua" | Maintainability | Sedang | Aturan PR: controller >800 baris → pecah; code owner review; layering internal dijaga (02 §4) |
| R9 | Scope creep (fitur cloud/agent diinginkan lebih awal) | Jadwal meledak | Sedang | Roadmap fase mengikat; perubahan urutan lewat catatan `docs/progress/` + update dokumen |
| R10 | Windows-only syscall tersebar tanpa build tag | Portabilitas masa depan mati | Rendah | Konvensi `_windows.go`/`_other.go` wajib sejak Fase 0 (diikuti dari LaraEnv) |
| R11 | Data user rusak saat transaksi (vhost/SSL/hosts setengah jalan) | Project tak tersaji | Sedang | Semua mutasi serving = snapshot→apply→validate→restart→rollback (pola `applyProjectRouteSet`/`applyProjectSSL` LaraEnv — wajib dijaga dengan test) |
| R12 | Kebocoran secret (PAT git, password SSH) ke log/UI/cloud | Keamanan | Sedang | Redactor di semua jalur output; DPAPI at-rest; PAT tak pernah dipersist di URL; audit MCP tanpa argumen; test redaksi |
| R13 | Kunci DPAPI tak terbaca (porta akun Windows berbeda) | Deployment/password hilang | Rendah | Pola LaraEnv: blob terkunci dipertahankan + `SecretsUnavailable` stub, tidak pernah ditimpa kosong |
| R14 | Shell escape / injeksi argumen di spawn (cron custom command, dev command) | Eksekusi tak diinginkan | Sedang | Cron/devcommand memang by-design shell (`cmd /C`); pastikan hanya dari config user lokal; MCP exec allow-list literal-arg; terminal eksternal via `start` divalidasi path |

## 2. Kewajiban Lisensi (ringkas, bukan nasihat hukum)

1. **Mengapa TIDAK menyalin kode LaraEnv** (strategi D5): menyalin akan menjadikan kitodock
   turunan Apache-2.0 dengan kewajiban attribution/NOTICE/marka perubahan per-file, dan
   mengunci kebebasan desain. Karena itu LaraEnv hanya referensi perilaku; audit baris-identik
   per fase menjaga batas ini (R1). Repo kitodock sendiri dirilis **Apache-2.0** atas kode
   orisinalnya.
2. **Dependensi Go LaraEnv** semuanya permissive (MIT/BSD/Apache) kecuali perlu cek:
   `ProtonMail/go-crypto` (MIT), `cloudflare/circl` (BSD-3), MS-RL hanya untuk **sumber WiX**
   (bukan kode kita; MS-RL adalah source-delivery obligation WiX, sama seperti LaraEnv: sertakan
   zip sumber di paket MSI). Wails v2 MIT.
3. **Binari yang didistribusikan ulang** (bukan dependensi Go — unduhan runtime):
   PHP (PHP-3.01), nginx (BSD-2), MySQL (GPLv2 **dengan exception** — distribusi zip mentah by
   Oracle diperbolehkan via FLOSS exception? **TIDAK** — MySQL Community adalah GPLv2; LaraEnv
   mendistribusikan link/unduh langsung dari dev.mysql.com dan menyertakan lisensi; kita ikuti
   pola sama: **jangan bundle binary GPL di paket rilis kita; installer mengunduh dari upstream
   saat user meminta**, dan notices disertakan), PostgreSQL (PostgreSQL Licence — permissive),
   Redis Windows (BSD-3 + supplemental), pgvector (PostgreSQL Licence), mailpit (MIT), Composer
   phar (MIT), Caddy (Apache-2.0), PECL ext (bervariasi — php.net license kebanyakan; imagick
   punya DLL pihak ketika LGPL — cek `tools/licenses` LaraEnv sebagai preseden audit).
   → **Aturan praktis Kitodock**: rilis resmi hanya berisi kode kita + aset; runtime diunduh
   on-demand dari upstream; THIRD_PARTY_NOTICES tetap dibangun (pola tools/licenses LaraEnv).
4. **Aset**: jangan salin logo/screenshots LaraEnv (hak cipta pemilik, walau repo Apache-2.0 —
   file media tidak otomatis bebas); ikon Kitodock dibuat sendiri; font JetBrains Mono OFL-1.1
   (bawa notice); Lucide MIT; Monaco MIT + ThirdPartyNotices-nya.

## 3. Perbaikan Desain vs LaraEnv (nilai tambah rewrite)

1. **Pecah monolit**: 274 method pada 1 struct → ±19 controller domain; `app.go` 6.230 → ≤600
   baris composition root. (Motivasi utama proyek ini.)
2. **Konstanta event terpusat** (`desktop/events.go`) menggantikan ±60 string tersebar.
3. **`internal/localca`** dipisah dari `vhost` (SRP; vhost = config render, localca = PKI).
4. **Logging terstruktur** (`slog` + file log ringan) — LaraEnv nyaris tanpa log.
5. **i18n id+en sejak Fase 0** (LaraEnv lahir en/pt-BR).
6. **Struktur file services lebih modular** (manager.go → beberapa file).
7. **Test adapter controller**: fungsi bebas per domain di file terpisah sejak awal (pola sukses
   `app_runtime.go`/`app_routes.go` LaraEnv dijadikan standar, bukan pengecualian).
8. **Tanpa fitur berbayar/plan gate** sejak hari pertama (LaraEnv menanggalkannya belakangan).
9. **Dokumentasi arsitektur hidup** di `docs/` (dokumen ini + progress log per fase).
10. Opsional dievaluasi: metrics ringan (hitungan event emit) untuk debugging — jangan
    over-engineer.

## 4. Keputusan Terbuka (perlu diputuskan sebelum fase terkait)

| Pertanyaan | Kapan wajib putus | Default bila tak diputus |
| --- | --- | --- |
| Repo GitHub target + branding final (nama "Kitodock"?) | Fase 0 | `kitodock`, ikon placeholder |
| Cloud backend ada/tidak | Sebelum Fase 6 | TIDAK ADA — lokal penuh |
| Katalog installer versi mana (per kind) | Fase 1 | Versi stabil terbaru per upstream saat eksekusi |
| Bundle MySQL di-cache offline vs unduh on-demand | Fase 1 | Unduh on-demand (mitigasi R2/GPL) |
| Migrasi data opsional dari LaraEnv | Fase 2 review | Tidak ada |
| Bahasa kedua setelah id | Fase 2 | en (sudah default) |

## 5. Opsi Kompatibilitas Data LaraEnv (bila dibuka ulang)
Jika kelak diinginkan import dari instalasi LaraEnv lama: tool baca `C:\laraenv\config.json`
+ `state\*` (password/deployment DPAPI **tidak** bisa dipindah antar-produk karena prefix/blob —
harus re-entry), konversi ke schema kitodock, re-sweep vhost dengan marker baru. Kerja kecil,
tapi eksplisit — tidak pernah silent.
