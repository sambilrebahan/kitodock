# Kitodock — Master Plan Rewrite dari LaraEnv

> Dokumen perencanaan penulisan-ulang (rewrite) aplikasi **LaraEnv** menjadi **Kitodock**.
> Disusun: 2026-11-06 · Status: DRAFT v1 · Bahasa: Indonesia

---

## 1. Ringkasan Eksekutif

**LaraEnv** adalah aplikasi desktop Windows (Go + Wails v2 + React) untuk mengelola lingkungan
pengembangan lokal PHP/Laravel/Node/Python: start/stop service (nginx, Apache, Caddy, MySQL,
PostgreSQL, Redis, Mailpit, pool php-fpm), multi-versi PHP & Node.js, virtual host + hosts file +
HTTPS lokal (local CA), project manager dengan deteksi runtime otomatis, terminal terintegrasi
(ConPTY), file editor (Monaco), panel Git, SSH/SFTP, deployment via SSH, cron, queue worker,
subsistem AI coding agents (Claude/Codex/Kimi/OpenCode) + MCP server, dan sinkronisasi cloud
opsional (E2EE). Lisensi Apache-2.0, versi terakhir 0.5.8.

**Kitodock** adalah rewrite LaraEnv dengan identitas, arsitektur yang lebih bersih (memecah
monolit `app.go` 6.230 baris menjadi controller per domain), dan roadmap bertahap. Skala LaraEnv
yang dipelajari:

| Aspek | Angka |
| --- | --- |
| File source (tanpa node_modules/vendor/dist) | ±1.556 |
| Go — lapisan `desktop/` | 64 file, 18.518 baris (produksi 13.079 + test 5.439) |
| Go — `internal/` | 45 paket (terbesar: `agents` ±13.487, `sourcecontrol` ±3.628, `deploy` ±2.862 baris) |
| API Wails (method `App` yang di-bind) | 274 method |
| Event Wails backend → frontend | ±60 nama event |
| Frontend React/TS | 238 file, 33.484 baris; 11 halaman; ±50 komponen |
| Bahasa i18n | en, pt-BR, ru, zh-CN |
| Mode proses dalam 1 binari | GUI, MCP stdio proxy, hook CLI (`--agent-activity/--agent-hook/--agent-event`), `--cron-run`, `--terminal-window`, `--command-bar` |
| Artefak rilis | MSI (WiX 5.0.2), Portable ZIP, NSIS opsional, deploy-tui (bubbletea) |

## 2. Keputusan Dasar

| # | Keputusan | Pilihan | Alasan |
| --- | --- | --- | --- |
| D1 | Tech stack | **Go 1.26 + Wails v2 + React 18 + TypeScript + Vite + Tailwind** | Stack setara referensi; biner kecil, pola arsitektur teruji |
| D2 | Arsitektur runtime | **Layanan lokal** (binary di folder environment, tanpa Docker) | Menghindari dependensi Docker Desktop; menyamai model LaraEnv |
| D3 | Cakupan | **Bertahap MVP-first** — 7 fase (0–6) | Nilai guna lebih cepat; risiko terkendali |
| D4 | Platform | **Windows 10/11 x64 dulu** | Kode Windows-specific LaraEnv (ConPTY, hosts, DPAPI, certutil) tetap relevan; macOS/Linux menyusul |
| D5 | Strategi kode | **Implementasi mandiri** — semua kode kitodock ditulis sendiri; LaraEnv hanya referensi perilaku/arsitektur, TIDAK ada penyalinan kode (audit baris: docs/progress/fase-0.md) | Keaslian kode & kebebasan desain; audit originalitas otomatis tiap fase |
| D6 | Lisensi | **Apache-2.0** untuk kode kitodock; NOTICE mencatat LaraEnv sebagai referensi desain (bukan sumber kode) | Pilihan bebas; atribusi referensi tetap diberikan sebagai praktik jujur |

Naming inti: `Kitodock` (produk) · `kitodock` (repo/module Go) · `KITODOCK_ROOT` (env) ·
`C:\kitodock` (root default) · `.kitodock-portable` (marker) · `Kitodock Local CA` (CA) ·
marker hosts `# kitodock`.

## 3. Struktur Dokumen Planning

| Dokumen | Isi |
| --- | --- |
| [`docs/01-inventaris-laraenv.md`](docs/01-inventaris-laraenv.md) | Inventaris menyeluruh LaraEnv: 45 paket internal, lapisan desktop (274 API + 60 event), 11 halaman frontend + komponen, data/state, build & rilis |
| [`docs/02-arsitektur-kitodock.md`](docs/02-arsitektur-kitodock.md) | Target arsitektur, struktur repo, konvensi (controller/event/DTO/injection), rename map lengkap |
| [`docs/03-roadmap-detail.md`](docs/03-roadmap-detail.md) | Roadmap 7 fase: modul, UI, acceptance criteria, dependensi antar fase |
| [`docs/04-mapping-dan-rename.md`](docs/04-mapping-dan-rename.md) | Peta perilaku-yang-dirujuk → rancangan kitodock sendiri (implementasi mandiri) + rename map |
| [`docs/05-risiko-dan-licensing.md`](docs/05-risiko-dan-licensing.md) | Risk register, kewajiban lisensi & redistribusi binari, daftar perbaikan desain |

## 4. Roadmap Ringkas

| Fase | Nama | Isi utama | Hasil yang bisa dicoba |
| --- | --- | --- | --- |
| 0 | **Fondasi** | Repo, module, CI, lint, i18n skeleton, `paths`/`atomicfile`/`config`/`credentials`, shell Wails (sidebar/tema/dialog) | Aplikasi kosong bisa `wails dev` & build, tema + bahasa jalan |
| 1 | **Services & Installer** | `services`, `installer` (katalog pinned), `vcruntime`, `runtimeinventory`, `tray`, `sysmetrics`, halaman Downloads + Dashboard | Install nginx/MySQL/PHP dari UI, start/stop, tray, metrik |
| 2 | **Runtimes & Projects** | `php`, `nodejs`, `projects` (deteksi+resolusi semver), `vhost` (3 server), `localtls`, `projectroutes`, `devserver`, Site Doctor inti, halaman Projects | Project `.test` tervalidasi + HTTPS lokal + dev server ter-proxy |
| 3 | **Terminal Workspace** | `pty` (ConPTY), workspace terminal mengambang (group/tab/split/detach-reattach), `terminal` eksternal, `cron` (+Task Scheduler), `queueworker` | Terminal in-app penuh fitur, cron & queue worker jalan |
| 4 | **Files, Editor, Git** | `workspacefiles` (hardened), Monaco + preview Markdown/gambar + konflik, `sourcecontrol`, `gitworkspace`, document shutdown gate | Browser file + editor + panel Git (stage/commit/push) |
| 5 | **SSH & Deployment** | `ssh` (known_hosts TOFU, proxy jump), SFTP browser, `deploy` + presets, `deployments` (DPAPI), `deploycoord`, halaman SSH + Deployments | Kelola host/kunci, deploy resep bash ke VPS dengan histori |
| 6 | **Agents, MCP, Cloud++** | `agents` (4 provider + hooks + activity multi-window), `mcpbridge`/`mcpclient`, command center + command bar, `cloud` opsional (E2EE), `updater`, `notifications`, deploy-tui, i18n penuh (id+en minimal) | Paritas fitur penuh dengan LaraEnv |

Urutan fase dipilih agar setiap fase menghasilkan nilai guna mandiri dan dependensinya sudah
tersedia (mis. Terminal butuh `paths`+`config` dari Fase 0; Site Doctor butuh runtime+vhost dari
Fase 2).

> **Progress**: Fase 0 **selesai** 2026-11-06 — lihat [`docs/progress/fase-0.md`](docs/progress/fase-0.md)
> (verifikasi, keputusan, penyimpangan, dan langkah manual tersisa).
> **Fase 1 selesai** 2026-11-07 — lihat [`docs/progress/fase-1.md`](docs/progress/fase-1.md)
> (services+installer+tray+dasbor; live chain nginx terverifikasi; audit 8,8%/0 komentar identik).
> **Fase 2a selesai** 2026-11-07 — lihat [`docs/progress/fase-2a.md`](docs/progress/fase-2a.md)
> (php/node multi-versi, deteksi+mesin constraint, vhost 3 server+CA+hosts, halaman Projects;
> live test PHP-via-nginx; audit 7,8%/0 komentar).
> **Fase 2b selesai** 2026-11-07 — lihat [`docs/progress/fase-2b.md`](docs/progress/fase-2b.md)
> (devserver+localtls, projectroutes, creator 8 jenis, sitedoctor, SSL transaksional;
> 22 paket hijau; audit 7,3%/0 komentar). **Fase 2 PENUH: SELESAI.**
> **Fase 3 selesai** 2026-11-08 — lihat [docs/progress/fase-3.md](docs/progress/fase-3.md)
> (ConPTY+terminal workspace tab/split, cron+Task Scheduler, queue worker; 25 paket hijau; audit 7,2%/0 komentar).

## 5. Estimasi Effort Relatif

Angka kasar relatif (bukan komitmen waktu) dengan asumsi 1 pengembang penuh waktu yang sudah
membaca planning ini:

| Fase | Effort relatif | Catatan risiko |
| --- | --- | --- |
| 0 | 5% | Cepat, banyak boilerplate |
| 1 | 12% | Redist VC++, first-run init DB, kill-orphan |
| 2 | 20% | Jantung produk: resolusi constraint, 3 renderer vhost, transaksi SSL |
| 3 | 15% | ConPTY + drag/drop UI kompleks; detach multi-proses |
| 4 | 15% | Hardening FS + conflict flow + Git facade |
| 5 | 15% | SSH/x/crypto + bash plan builder + DPAPI store |
| 6 | 18% | Subsistem agents terbesar; MCP SDK; cloud opsional bisa diundur |

## 6. Definition of Done Global (per fase)

1. Semua acceptance criteria fase terpenuhi (lihat `03-roadmap-detail.md`).
2. `go test ./...` hijau; `go vet` bersih; frontend `tsc && vite build`, `vitest`, `eslint` hijau.
3. CI (GitHub Actions) hijau: build offline + test + lint + pengecekan i18n.
4. Dokumen fase di `docs/progress/` diperbarui (catatan keputusan implementasi).

## 7. Cara Memakai Dokumen Ini

- Mulai eksekusi dari **Fase 0** di `03-roadmap-detail.md`; jangan lompat ke fase berikutnya
  sebelum DoD fase aktif terpenuhi.
- Saat implementasi modul, baca entri modulnya di `01-inventaris-laraenv.md` (perilaku apa yang
  ada di LaraEnv) lalu keputusannya di `04-mapping-dan-rename.md` (rancangan kitodock sendiri
  untuk perilaku setara — kode ditulis sendiri, bukan disalin).
- Setiap keputusan yang menyimpang dari planning dicatat di `docs/progress/` agar dokumen tetap
  jadi sumber kebenaran.
- **Aturan keaslian kode**: LaraEnv adalah referensi, bukan sumber salinan. Setiap akhir fase,
  jalankan audit baris-identik (lihat skrip di `docs/progress/fase-0.md`) dan pastikan nol
  komentar/prosa identik; sisa tumpang tindih hanya boleh berupa import, bentuk panggilan API
  pihak ketiga, dan idiom bahasa satu-baris.
