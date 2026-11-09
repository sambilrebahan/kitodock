# Progress — Fase 6: Agents, MCP, Command Center, Cloud & Pelengkap

> Status: **SELESAI** (2026-11-10) · Implementasi mandiri; audit §5.
> Cloud sync **dijadwalkan ulang** (bukan diimplementasi) sesuai opsi eksplisit roadmap — lihat §6.

## 1. Paket baru

| Paket | Isi | Test |
| --- | --- | --- |
| `internal/agents` | Katalog 4 provider (Claude Code, Codex CLI, Gemini CLI, Copilot CLI) + probe versi (timeout + `WaitDelay` — wrapper .cmd npm menahan stdout saat di-kill); Manager sesi (launch via seam `Spawner`, resume, rename, archive, delete, persist sessions.json, PTY id tak pernah bertahan lintas restart); discovery transcript JSONL per provider (id/cwd/first-user-msg defensif, cap 50); hooks: settings.json (marker `kitodock-managed`, idempoten, backup sekali + restore), config.toml (`notify` baris dengan marker), copilot jujur "no hook support"; spool event (`--agent-event` CLI personality menulis satu baris JSON lalu keluar) + ActivityLog (ring 500, JSONL durable, drain 250 ms batch 100, link provider-session → sesi kitodock) | **8** |
| `internal/ownerhttp` | Endpoint kontrol loopback 127.0.0.1:0 + bearer 32-byte random; POST /call {method,payload,source}; **audit tanpa argumen** (method, ok, kelas error 40 kar., source) — payload tidak pernah masuk log; ReadAudit newest-first | **1** |
| `internal/mcpbridge` | MCP stdio (JSON-RPC 2.0 baris-per-baris; initialize/tools list+call/ping; MCP `2024-11-05`); **9 tools `kitodock_*`**: project_list/open, service_list/start/stop, **exec (confirm-gated)**, db_list, **context (confirm-gated)**, mail_list; gate confirm ditegakkan DI BRIDGE sebelum menyentuh GUI; OwnerClient HTTP ke loopback | **3** — end-to-end: Serve stdin-pipe → initialize → tools/list (9) → gate tanpa confirm (balasan kering) → call nyata → error method |
| `internal/mcpclient` | Registrasi/deregistrasi server ke **Claude Code** (~/.claude.json), **Codex** (config.toml `[mcp_servers.kitodock]` + env flattened), **Cursor** (mcp.json); backup pra-registrasi sekali — file yang sudah berisi registrasi tidak pernah di-backup (bug ditemukan test: backup dari state ter-registrasi membuat unregister jadi re-register) | **3** |
| `internal/applicationcontext` | Capture Laravel (`route:list --json` + `about --json` via runner injectable; error tercatat di snapshot, tetap ditulis); log JSONL per proyek (`state/agents/context/<nama>.jsonl`, ReadLog cap); Mailpit `/api/v1/messages` (from/to/subject/tag dari local-part penerima) | **3** |
| `internal/updater` | Checker rilis GitHub (`releases/latest`), IsNewer per segmen titik, FindAsset by suffix; DownloadAndVerify: sidecar `.sha256`, cek ukuran, SHA-256 hex | **3** (getter stub, mismatch digest, size) |
| `internal/webviewruntime` | Inspeksi registrasi WebView2 (HKLM WOW6432Node → HKCU), versi/per-user/hint recovery | — (inspeksi registry murni) |
| `internal/deploy` (+) | **Registry preset builtin bersama** (git-pull-update, laravel-production, static-site) — dipakai controller desktop **dan** CLI | (tercakup) |

## 2. Wiring desktop

- **controllers/agents.go**: providers+probe, launcher per proyek (PTY bersama terminal workspace — `Terminal.Registry()` diekspos; exit di-chain: `term:exit` + bookkeeping agent), resume, rename/archive/delete, hooks status/install/remove (command = `os.Executable() --agent-event <provider>`), discovery, activity, event `agents:activity` / `agents:session`; drain loop start di startup, stop di shutdown.
- **controllers/mcp.go**: EnableMCP (owner server + audit `state/agents/mcp-audit.jsonl` + registrasi 3 klien dengan env OWNER_URL/TOKEN + persist `config.MCPEnabled`), DisableMCP, MCPStatus, dispatcher 9 method ke `MCPDeps`; `RunProjectExec` allowlist (php/artisan/composer/npm/npx/pnpm/yarn/node/git), output cap 64 KiB. Auto-enable saat startup bila preferensi on.
- **desktop/mcpdeps.go**: MCPDeps over serving+stack: projects (nama+URL HTTPS+kind), services snapshot, start/stop, exec di folder proyek, db_list via `mysql.exe -u root -N -e "SHOW DATABASES"` (harus running), context capture (php runtime proyek) + append log, mail (Mailpit :8025), navigate → event.
- **controllers/commandcenter.go**: catalog dinamis (pages, projects×{open,terminal}, services×{start,stop}, ssh hosts, agents) + RunCommand (page/project → navigate; service → stack; terminal/ssh/agent → event `commandcenter:run` yang dieksekusi shell karena state pane milik frontend).
- **controllers/updates.go**: CheckUpdates/DownloadUpdate (staged ke temp)/ApplyUpdate (MSI `msiexec /i`; portable → path staged + hint, menolak self-replace exe berjalan) + WebViewRuntimeState.
- **main.go personalities** (sebelum elevasi): `--mcp-serve` (bridge stdio via KITODOCK_OWNER_URL/TOKEN) dan `--agent-event <provider> [payload|stdin]` (ekstraksi session_id/hint defensif, cap 16 KiB, spool lalu exit).
- config: field `MCPEnabled`; paths: `AgentsDir()`.

## 3. Frontend

- **Agents.tsx**: launcher proyek+provider (disabled bila CLI tak terpasang, tooltip versi), toggle hooks per provider (badge on/off/unsupported), daftar sesi (dot running, resume provider-session, rename, archive/unarchive + filter, delete confirm), feed aktivitas live (`agents:activity` prepend).
- **CommandPalette.tsx**: Ctrl+K global (App.tsx), filter substring+subsequence, navigasi arrow/enter/esc, grup+hint; page/project dieksekusi shell langsung, lainnya ke backend; event `commandcenter:navigate` dari backend (mis. MCP `project_open`) juga ditaati.
- **Settings.tsx**: panel **Experimental (MCP)** — toggle on/off, URL endpoint, badge registrasi per klien, audit trail (tanpa argumen); panel **Updates** — check → versi, download+verify → apply (MSI/portable hint).
- Bindings stubs Agents/MCP/Updates/CommandCenter + models (agents.*, AgentHookStatus/Command/MCPStatus/MCPClientStatus/UpdateCheck); i18n en+id: `agents` (24), `commandPalette` (2), `settings.mcp` (6), `settings.updates` (8).

## 4. CLI

- **`cmd/kitodock-deploy`** — binary headless satu file: `-detect` (laporan probe), apply (plan generik / `-preset` / `-list-presets`), `-destroy [-drop-db]`; koneksi via host tersimpan (hosts.json + kunci + known_hosts TOFU) **atau** `-address` + `KITODOCK_SSH_PASSWORD` (mode alamat = trust-first seperti ssh pertama); output streaming per baris **melewati redactor PAT/db-password** yang sama dengan desktop. Smoke: `-list-presets` terverifikasi.

## 5. Verifikasi & audit

| Pemeriksaan | Hasil |
| --- | --- |
| `go test` (fresh, -count=1) | ✅ **38 paket hijau** (agents 8, ownerhttp 1, mcpbridge 3, mcpclient 3, applicationcontext 3, updater 3, + lainnya) |
| `go vet -unsafeptr=false` (setara CI) | ✅ |
| gofmt | ✅ |
| Frontend build + vitest + lint | ✅ 12/12 test, 0 error |
| Binary: `kitodock.exe` (39,8 MB) + `kitodock-deploy.exe` (8 MB) | ✅ |
| **Audit originalitas (36 file fase-6)** | **0 komentar prosa identik** (3 hit = direktif compiler `//go:embed`); 116 baris kode >30 kar. cocok semuanya non-ekspresif: struct tag, idiom stdlib (`strings.Split`, `scanner.Buffer`, `context.WithTimeout`), baris import, class utilitas UI |

## 6. Keputusan, penyimpangan & utang

1. **Cloud sync: dijadwalkan ulang, tidak diimplementasi** — roadmap sendiri menandainya opsional ("hanya jika backend cloud akan dipakai; default Kitodock: lokal penuh tanpa akun"). Tidak ada kode cloud; posisi produk = lokal penuh.
2. **deploy-tui → CLI flags non-interaktif** (bukan port bubbletea): engine, preset, TOFU, redactor identik dengan desktop; UI TUI bisa menyusul tanpa menyentuh internal. Build windows terverifikasi; build linux teoretis (GOOS linux) — CI runner windows.
3. **Command palette in-app Ctrl+K** — jendela command-bar proses terpisah + hotkey **global** (RegisterHotKey) dari roadmap belum: hotkey global menambah permukaan win32 dan second-window personality; nilai guna inti (katalog + intent + navigasi lintas proses via event) sudah tercapai in-app. Tercatat utang.
4. **MCP exec confirm-gate** = argumen `confirm:true` eksplisit di tool schema (di-TEGAKKAN di bridge sebelum jaringan), bukan dialog approval interaktif GUI (pending-request flow) — lebih sederhana, auditable, dan tetap aman; deviasi dicatat.
5. **Copilot CLI: tanpa hooks** — jujur dilaporkan `hookStyle: none`; katalog tetap 4 provider (launch + discovery ada).
6. **Bug ditemukan test**: backup mcpclient dari state-sudah-teregistrasi → unregister malah me-restore registrasi; deadlock `persistLocked→List` di agents.Manager; wrapper .cmd menahan stdout probe (fix `WaitDelay`). Semua fix + regresi test.
7. **db_list** butuh mysql running + `bin\mysql.exe`; exec allowlist ketat — keduanya kesengajaan keamanan, bukan keterbatasan.
8. **Usage tracking provider** (Anthropic/Codex OAuth API) + **notifications diff sesi/threshold usage** dari roadmap: belum (butuh akun billing pihak ketiga; tidak ada akun di konteks lokal). Tercatat utang non-blocker.
9. Preset registry dipindah ke `internal/deploy` agar CLI dan desktop berbagi satu sumber kebenaran.

## 7. AC scorecard (jujur)

| # | AC | Status |
| --- | --- | --- |
| 1 | Launch claude/codex di proyek, resume setelah restart, stop, rename, archive | ✅ (badge unread menyusul bersama usage) |
| 2 | Hook terpasang/bersih dilepas; aktivitas terlihat | ✅ 3 gaya config + copilot jujur; AgentMapDialog lintas jendela = feed aktivitas in-app (jendela detached menyusul bersama #3) |
| 3 | MCP: enable → registrasi → project list/service start/exec(confirm)/db list; audit tanpa argumen | ✅ (teruji end-to-end level bridge+owner; live dari Claude Code = manual) |
| 4 | kitodock_context + capture JSONL + mail ter-tag | ✅ (capture artisan butuh proyek Laravel nyata = manual) |
| 5 | Command palette Ctrl+K global, intent dieksekusi | ⚠️ Ctrl+K in-app penuh; hotkey global + jendela terpisah menyusul (§6.3) |
| 6 | Updater: cek rilis, unduh+sha256, apply MSI/portable | ✅ (rilis GitHub nyata = manual; logika teruji) |
| 7 | Notifications sesi-input/usage | ❌ utang (§6.8) |
| 8 | deploy-tui provision VPS resep sama | ⚠️ CLI bukan TUI; resep+engine sama persis (§6.2) |
