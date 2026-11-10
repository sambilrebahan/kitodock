# Progress — Review Menyeluruh & Penutupan Celah (pasca-Fase 6)

> Status: **SELESAI** (2026-11-10) · Audit penuh PLAN + roadmap 0–6 terhadap kode nyata,
> diikuti eksekusi seluruh temuan. Audit originalitas: §4.

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

### Dijadwalkan ulang (amend eksplisit di roadmap, dengan alasan)

- **GPU engine metrics** — butuh GPU fisik untuk diverifikasi.
- **PECL instalasi biner + PhpExtensionsModal + tuning forms** — distribusi DLL + pengujian
  terhadap instalasi PHP nyata; `EnsureIni`/ekstensi inti sudah jalan di backend.
- **Floating window detach/reattach + `--terminal-window`** — pasca-1.0; split-tree + pane SSH
  telah memenuhi kebutuhan inti (posisi konsisten dengan fase-3 §5).
- **`internal/gitworkspace`** — tidak dibangun; kebutuhan agents terpenuhi lewat discovery
  transcript + PTY bersama; checkout worktree menyusul bersama preview-worktree penuh.

## 3. Verifikasi

| Pemeriksaan | Hasil |
| --- | --- |
| `go test ./...` (fresh) | ✅ **39 paket hijau** |
| `go vet -unsafeptr=false` | ✅ |
| Frontend vitest + lint + build | ✅ **14/14 test** (2 baru: bare-strings), 0 error lint |
| Binary GUI + CLI (+ cross-compile linux CLI) | ✅ |
| **Audit originalitas (semua file ronde ini)** | **0 komentar prosa identik**; sisa kecocokan baris hanya idiom/struct-tag/import |

## 4. Catatan

1. Pane agent di workspace: `pane.kind = "agent"` — dirender Terminal biasa (aliran
   `term:data` sama), exit PTY tetap membereskan bookkeeping agent via NotifyExit chain.
2. Rute proyek dicerminkan best-effort (gagal buka store ≠ fatal) — preview worktree
   (konsumen berikutnya) tinggal memanggil `CreatePreview` saat fitur git-worktree hadir.
3. `CreateProject` sinkron dengan event progres; frontend menampilkan log live dan
   me-refresh daftar proyek saat `done`.
