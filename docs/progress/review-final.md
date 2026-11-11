# Progress — Review Menyeluruh & Penutupan Celah (pasca-Fase 6)

> Status: **SELESAI** (2026-11-10, dua ronde) · Audit penuh PLAN + roadmap 0–6 terhadap
> kode nyata, diikuti eksekusi seluruh temuan — ronde 2 mengimplementasi ulang semua yang
> semula ditunda. Audit originalitas: §4.

## 1. Metode

Dibaca ulang: `PLAN.md` (keputusan D1–D6, DoD), kelima dokumen planning, seluruh AC Fase 0–6,
dan setiap catatan utang/deviasi di `docs/progress/fase-0..6.md`. Setiap butir dicocokkan ke
kode lewat pemeriksaan langsung (grep binding/controller/UI/pemanggil), bukan dari ingatan.

## 2. Temuan dan penutuannya

### 🔴 Kritis

| Temuan | Penutupan |
| --- | --- |
| **`--cron-run` tidak didispatch `main.go`** — Task Scheduler background cron (AC F3 #5) memanggil binari dengan flag itu; sebelumnya akan membuka GUI penuh tiap menit alih-alih menjalankan job. `cron.RunOnceFromConfig` tersedia tapi yatim | ✅ Personality `--cron-run <jobID>` ditambahkan di main.go (sebelum elevasi); memanggil `cron.RunOnceFromConfig(paths.ConfigFile(), id)` |

### 🟠 Backend tanpa UI / tidak ter-wire

| Temuan | Penutupan |
| --- | --- |
| Creator 8 jenis tanpa form (AC F2 #1 via UI) | ✅ `CreateProject` API + `NewProjectModal` (pemilih jenis dari backend, git URL, package manager, log progres via event `project:create`) |
| `LocalCAStatus`/`TrustLocalCA` yatim — tab Settings Services tidak pernah dibangun | ✅ Panel **Diagnostics** di Settings: kartu CA (status + tombol trust) + kartu WebView2 |
| `projectroutes` yatim (utang 2b "Fase 6" tak diambil) | ✅ serving membuka `state/project-routes.json`; `ApplyProject` → `SetCanonical`, `RemoveProject` → lupa semua rute proyek; `ListProjectRoutes` diekspos |
| vhost editor Monaco (UI F2) tidak ada | ✅ `GetProjectVhost`/`SaveProjectVhost` + modal editor (nginx.conf, simpan → reload web server; hint bahwa re-apply meregenerasi) |
| NotificationBell (F1) tidak ada; `internal/notifications` yatim sejak F0 | ✅ Controller Notifications (list/unread/mark/mark-all + rate-limit 1 menit per kunci) + hook `service:error` dari Services + komponen bell di Sidebar (badge, dropdown) |
| Tab Agents di WorkspaceNavigator (F4 "aktif F6") tidak ada | ✅ Tab ketiga: launch provider untuk proyek terpilih → **PTY agent diadopsi sebagai pane workspace** (outputnya tampil di samping navigator), daftar sesi, feed aktivitas |
| Clipboard bridge terminal (F3) — API sudah ada di System, tapi keybinding xterm tidak | ✅ Ctrl+Shift+C (salih seleksi) / Ctrl+Shift+V (tempel) di Terminal + RemoteTerminal via `attachCustomKeyEventHandler` |
| `WebViewRuntimeState` tanpa pemanggil | ✅ Kartu diagnostik WebView2 di Settings |

### 🟡 Minor / quick wins

| Temuan | Penutupan |
| --- | --- |
| Tema persist di localStorage, bukan config.json (AC F0 #2) | ✅ Field `Config.Theme` + `System.GetTheme/SetTheme`; Settings sinkron (config = sumber kebenaran lintas proses, localStorage jadi fallback pra-hidrasi) |
| Config golangci-lint (item F0) tidak ada | ✅ `.golangci.yml` minimal (govet/gofmt/staticcheck/ineffassign; test dikecualikan) |
| Meta-test no-bare-strings — dijanjikan 3× sejak F0 | ✅ Vitest: teks JSX + atribut literal berbahasa Inggris gagal test; **hanya 2 pelanggar ditemukan di seluruh codebase** (satu nama produk → whitelist proper noun; satu string nyata → dimigrasi `workspace.externalLinkBlocked`) |
| PLAN.md baris 96–98 — catatan fase 5 terpotong/urutan salah | ✅ Diperbaiki |
| deviasi VcRedistModal (F1) tak terdokumentasi | ✅ Diamendemen di roadmap (payload `service:error` + dialog = setara fungsional) |

### Ronda kedua: yang semula dijadwalkan ulang, kini diimplementasi

> Update 2026-11-10 (lanjutan): pemilik meminta semua temuan diperbaiki, termasuk yang
> ditunda. Keempatnya dieksekusi di commit lanjutan.

| Temuan | Implementasi |
| --- | --- |
| **GPU engine metrics** | `sysmetrics/gpu_windows.go`: PDH "GPU Engine(*)\\Utilization Percentage" via counter *array* (jumlah semua engine, cap 100); mesin tanpa counter family → latch sekali → 0 selamanya (bukan error stream); bar GPU di Dashboard |
| **PhpExtensionsModal** | `php/extensions.go`: daftar ekstensi (entri php.ini apa adanya + DLL ter-ship yang tak tercantum), toggle entri (enable aktifkan/append; disable beri komentar — riwayat terlihat), tolak nama tanpa DLL, merge live `php -m`; modal di Projects per seri PHP proyek. **Unduhan PECL biner tetap ditunda** (distribusi DLL per versi — dicatat roadmap) |
| **Floating window detach/reattach + `--terminal-window`** | `termHub` ring 512 KiB/PTY dengan seq global (pump PTY lokal + SSH keduanya mengisi); endpoint owner `term_poll/write/resize` (bearer); personality `--terminal-window <ptyId>` = jendela Wails kedua (WindowInfo role probe → komponen `DetachedTerminal` xterm + polling 80 ms via fetch); kredensial lewat env, bukan argv; tombol detach per pane + chip reattach di workspace; PTY tetap hidup di proses utama, pane bisa kembali kapan pun |
| **`internal/gitworkspace`** | `worktree list --porcelain` (path dinativisasi, refs/heads/ di-strip), `CurrentBranch`, `Resolve` (walk-up ke worktree root), `IsRepository`; 4 test dengan repositori + worktree git nyata |

### Ronde ketiga: dua penundaan terakhir diimplementasi

> Update lanjutan: "selesaikan yang belum" — PECL biner dan tuning forms tuntas, plus
> satu celah produksi yang ditemukan riset tuning.

- **PECL biner** — `php/pecl.go`: deteksi flavor build seri (TS/NTS + arch via `php -i`),
  pilih rilis terbaru yang menyediakan flavor itu (listing direktori releases php.net),
  unduh zip → ekstrak `php_<ext>.dll` ke `ext\` → aktifkan entri ini. Katalog: xdebug,
  redis, mongodb, apcu, imagick, memcached. Seam jaringan & probe di-inject; test dengan
  server in-memory (rilis tanpa flavor dilewati, zip tanpa DLL ditolak, versi diurutkan
  numerik 6.0.10 > 6.0.2). UI: seksi "Install from PECL" di modal ekstensi.
- **Tuning forms** — panel "Server tuning" di Settings: nginx (worker_processes/connections,
  client_max_body_size, keepalive_timeout, sendfile) + PHP aktif (memory/upload/post/
  max_execution_time/max_input_time/opcache.enable); validasi per keluarga nilai; nginx
  di-reload setelah set.
- **Celah produksi ditemukan & ditutup**: nginx.conf stock dari zip **tidak pernah
  memuat `sites/*`** — vhost proyek tertulis tapi tak termuat (live test fase 2 memakai
  conf sendiri sehingga lolos). `internal/webtuning.EnsureManagedConf` kini dimigrasikan
  di `syncUpstreams`: stock → conf terkelola ber-marker (worker_processes auto, events,
  http + mime.types + `include sites/*.conf`); conf terkelola tak pernah ditimpa.
- Apache tetap tanpa tuning form — apache memang belum terdaftar sebagai service
  (renderer + sites dir saja); dicatat apa adanya.

## 3. Verifikasi

| Pemeriksaan | Hasil |
| --- | --- |
| `go test ./...` (fresh) | ✅ **41 paket hijau** (webtuning baru) |
| `go vet -unsafeptr=false` | ✅ |
| Frontend vitest + lint + build | ✅ **14/14 test** (2 baru: bare-strings), 0 error lint |
| Binary GUI + CLI (+ cross-compile linux CLI) | ✅ |
| **Audit originalitas (semua file ronde ini)** | **0 komentar prosa identik**; sisa kecocokan baris hanya idiom/struct-tag/import |

### Ronde keempat: audit "menu tampil dan bisa dipakai" lintas-lapisan

Pemeriksaan otomatis: setiap stub binding ↔ metode Go ↔ controller ter-bind di main.go,
lalu setiap import frontend ↔ stub, dan setiap langganan event frontend ↔ emitter backend.

| Temuan | Penutupan |
| --- | --- |
| **Controller Notifications tidak ter-bind di main.go** — bell tampil tapi semua panggilannya mati diam-diam di runtime | ✅ `app.Notifications` masuk Bind list |
| **Controller Dev & Doctor tanpa satu pun pemakai frontend** — Site Doctor dan dev server (AC F2 #4/#5) hanya hidup di backend | ✅ Tombol **Site Doctor** per kartu proyek → modal diagnosa (temuan + severity + tombol Fix per FixID + re-check setelah fix); tombol **dev server** per kartu (start/stop, hijau saat berjalan, status di-refresh dari `DevRunning`) |
| Event `app:notice` tanpa pelanggan (notis backend tak terlihat siapa pun) | ✅ NotificationBell melanggankan dan memasukkannya ke inbox via `PushNotification` |

Setelah perbaikan: binding audit **0 masalah**; 9 id halaman Sidebar = App = katalog palette.

### Ronde kelima: audit eksternal menyeluruh + perbaikan penuh

Sebuah audit independen (2026-11-10, HEAD `3e26250`) menemukan 5 Kritis, 13 Tinggi,
18 Sedang, 15 Rendah. Semua diperbaiki dalam lima commit berturut-turut:

| Gelombang | Commit | Cakupan |
| --- | --- | --- |
| G1 Keamanan | `b1d17d0` | K1 deploy RCE, K2 symlink escape, K3 MCP gate bypass + per-source tokens, H5 path traversal, H6 knownhosts panic, H13 PECL injection |
| G2 Fitur mati | `ebae269` | K5 detached window crash, H1 serving events, H2 CreateProject, H3 SSL revert, H4 config deep-clone, H8 doctor argv, H12 notif ID, M2 TLD |
| G3 Data-loss | `ecf2b8f` | K4 mcpclient, H9 form keys, H10 dirty reset, M1 vhost content restore, M11 dialog key, M14 nginx backup |
| G4 Services+UI | `adfc2d3` | H7 restart/stop + scoped kill, H11 ptyId, M3 shutdown drain, M4 mutex, M10 setActiveTab, M12 debounce |
| G5 Medium | `474f949` | M5 git errors, M6 .env, M7 cron nil, M8 CLI redact, M13 ring GC, M15 gitworkspace, L6-L8, L10-L11 |

**Yang tetap terbuka (disengaja):**
- L4: `log/slog` + rotasi log — belum dibangun (logging struktur penuh)
- L5: pipeline rilis GitHub Actions (MSI/portable/sidecar SHA-256) — manual saat ini
- L12: dua `unsafe.Pointer` di clipboard membuat `go vet` default merah; CI sudah pakai `-unsafeptr=false`
- L13: `go test ./...` ikut mengompilasi `node_modules/flatted` — noise, bukan kegagalan
- L14: URL PECL belum diverifikasi hidup terhadap php.net build service

## 4. Catatan

1. Pane agent di workspace: `pane.kind = "agent"` — dirender Terminal biasa (aliran
   `term:data` sama), exit PTY tetap membereskan bookkeeping agent via NotifyExit chain.
2. Rute proyek dicerminkan best-effort (gagal buka store ≠ fatal) — preview worktree
   (konsumen berikutnya) tinggal memanggil `CreatePreview` saat fitur git-worktree hadir.
3. `CreateProject` sinkron dengan event progres; frontend menampilkan log live dan
   me-refresh daftar proyek saat `done`.
