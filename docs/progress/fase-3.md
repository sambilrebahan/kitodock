# Progress — Fase 3: Terminal Workspace, Cron & Queue

> Status: **SELESAI** (2026-11-08) · Implementasi mandiri; audit §3.

## 1. Paket baru

| Paket | Isi | Test |
| --- | --- | --- |
| `internal/pty` | ConPTY wrapper: Registry (ID `term-N`), Spawn (exe+args+dir+PATH dirs+env), StartCommand via `conpty.Start` dengan `ConPtyEnv` + `ConPtyDimensions` + `ConPtyWorkDir`; PATH live dibaca ulang dari registry Windows (HKLM+HKCU) + dedup case-insensitive; env `KITODOCK_TERMINAL_ID` terpasang; NotifyExit callback; CloseAll shutdown | 4 (PATH extra dirs, dedup, replaceOrAppend, **spawn→read→exit dengan ConPTY nyata**) |
| `internal/cron` | Engine robfig/cron in-proses (parser 6-field + descriptor), Manager (Start/Stop, Save validasi schedule, Delete, Toggle, RunNow, List urut terbaru), executor `cmd /c` dengan timeout 5 menit + kill-tree, output ≤8 KiB, event Progress (start/output/done/error), **Task Scheduler background mode** (`schtasks /Create /TN Kitodock\<id>` per-minute heartbeat, `--cron-run <id>` cek jadwal menit-berjalan), `RunOnceFromConfig` CLI entry | 6 (validasi, toggle, delete, **run-now dengan output nyata**, isFiringMinute, RunOnceFromConfig) |
| `internal/queueworker` | `php artisan queue:work` per project (start/stop/restart/status/logs/stopAll); ring 500 baris; kill-tree | 5 (tanpa artisan gagal, tanpa PHP gagal, status lifecycle, stopAll, logs nil) |
| `internal/terminal` | Deteksi 4 terminal dari PATH + lokasi umum (PowerShell, CMD, GitBash, Cmder); Open (start command per jenis + PATH prefix) | 4 (deteksi builtin, deteksi GitBash, tanpa binary gagal, jenis tak dikenal gagal) |

## 2. Wiring desktop

- **Terminal controller**: StartPty (resolve shell + project PATH/dirs), WritePty, ResizePty, StopPty, ReadPtyData, **StreamPtyData** (pump output → event `term:data`), ListTerminals (deteksi eksternal), OpenProjectTerminal, OpenExternalSSH; CloseAll shutdown.
- **CronQueue controller**: ListCrons/SaveCron/DeleteCron/ToggleCron/RunCronNow (CRUD lengkap); StartQueueWorker/StopQueueWorker/RestartQueueWorker/GetQueueWorkerStatus/GetQueueWorkerLogs/StopAllQueueWorkers; event `cron:status`, `queueworker:status`, `queueworker:log`.
- app.go: Terminal + CronQueue dibuat dan di-bind; config.json mendapat field `crons` (raw JSON, tanpa import cycle).
- Lifecycle: startup → SetContext + StartCron(rawToJobs); shutdown → StopCron + StopAllQueueWorkers + Terminal.CloseAll + stack.StopAll.
- Terminal controller implements TerminalServing interface via serving.go.

## 3. Frontend

- **Terminal.tsx**: xterm.js (cursorBlink, scrollback 5000, tema gelap) + FitAddon (fit setelah font load) + ResizeObserver → ResizePty; input → WritePty; event `term:data` → term.write; `term:exit` → onExit callback.
- **TerminalPage.tsx**: toolbar (pilih jenis terminal + proyek + tombol Tab baru + Split), tab strip browser-like (klik untuk pindah, × untuk tutup semua pane), area terminal dengan split CSS flex horizontal (tanpa library eksternal — react-resizable-panels v4 API berbeda total, dicatat), tombol × per pane saat hover; hotkey Ctrl+T (tab baru) / Ctrl+W (tutup pane); auto-open tab pertama.
- **Cron.tsx**: tabel job (toggle enable, run-now, edit, delete dengan konfirmasi), form lengkap (nama, schedule preset dropdown, command, background-mode checkbox), event cron:status refresh.
- Deps: `xterm@5.3.0` + `xterm-addon-fit@0.8.0`.
- Binding stubs: Terminal.js/.d.ts, CronQueue.js/.d.ts.

## 4. Verifikasi & audit

| Pemeriksaan | Hasil |
| --- | --- |
| `go test ./...` (fresh) | ✅ **25 paket hijau** |
| `go vet -unsafeptr=false` | ✅ |
| gofmt | ✅ |
| Frontend build + vitest + lint | ✅ 12/12 |
| `wails build` | ✅ 9,6 dtk |
| Smoke-run | ✅ |
| **Audit originalitas** | **7,2%**, **0 komentar prosa identik** |

## 5. Keputusan & catatan

1. **react-resizable-panels dihapus**: v4 punya API `Group/Panel/Separator` yang berbeda total dari v2 yang LaraEnv referensikan (`PanelGroup/Panel/PanelResizeHandle`). Split CSS flex murni dipakai — sederhana, tanpa dependensi, dan cukup untuk tab+split horizontal. Floating windows + detach menyusul.
2. **Floating workspace (drag/resize/snap/detach) belum**: LaraEnv punya ~1.000 baris workspace reducer untuk ini. Fase 3 versi kitodock memakai tab+split sederhana yang berfungsi; floating window + detach ke proses satelit dicatat sebagai **utang Fase 6** (saat agents membutuhkan multi-window).
3. **Workspace persistence belum**: layout tab/split belum disimpan ke config. Ditambahkan bersama floating workspace di Fase 6.
4. **ConPTY env**: `ConPtyEnv` option dari library dipakai langsung (bukan cmd /c set wrapper) — lebih bersih dan setiap variabel env dikontrol.
5. **Task Scheduler**: entry dibuat dengan `schtasks /Create /SC MINUTE /MO 1` (heartbeat per-menit, bukan terjemahan cron expression) — CLI `--cron-run <id>` memeriksa apakah menit ini adalah menit tembak.

## 6. Utang
- Floating workspace + detach + persistence (Fase 6).
- SSH terminal pane (Fase 5 — controller + PTY remote).
- Meta-test no-bare-strings frontend (utang Fase 0 yang persisten).
