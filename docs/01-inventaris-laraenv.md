# 01 — Inventaris Lengkap LaraEnv

> Sumber: pembacaan menyeluruh repo `laraenv/` (module Go `lara4ever`, produk LaraEnv v0.5.8).
> Dokumen ini adalah "peta isi" LaraEnv yang menjadi dasar perencanaan rewrite Kitodock.

---

## A. Gambaran Umum

| Item | Nilai |
| --- | --- |
| Produk | LaraEnv — "Your local development workspace for Windows" |
| Pembuat | Thayron Arrais (github.com/thayronarrais/laraenv), Apache-2.0 |
| Stack | Go 1.26.10 · Wails v2.12.0 · React 18 + TS + Vite 6 + Tailwind v4 · WebView2 (Chromium ≥111) |
| Platform | Windows 10/11 x64, berjalan **elevated (admin)** — manifest requireAdministrator |
| Root environment | `C:\laraenv` (fallback permanen), atau direktori exe jika ada marker `.laraenv-portable`, atau env `LARAENV_ROOT` |
| Mode proses | GUI utama · `mcp` (stdio proxy) · `--agent-activity` / `--agent-hook` / `--agent-event` (CLI hook) · `--cron-run <id>` · `--terminal-window <seed>` (jendela terminal satelit) · `--command-bar` (palette global) |
| Versi | 0.5.8 (rilis 0.5.4–0.5.8 terdokumentasi) |

---

## B. Lapisan `desktop/` (Wails) — 64 file Go, 18.518 baris

### B.1 Arsitektur proses & entry (`main.go`, 186 baris)
- `main()` men-dispatch 5 mode: MCP stdio proxy (`mcpbridge.RunProxy`), CLI hook aktivitas/hook/event agent, `--cron-run` (Task Scheduler), GUI.
- GUI: `ensureGUIElevation` relaunch via UAC `runas`; deteksi recovery WebView2 (`webviewruntime.RecoveryBrowserFolder`); window utama frameless 1280×760 (min 960×600), single-instance lock `laraenv-main-single-instance`; jendela terminal satelit 980×620 native-frame maximized; command-bar 680×460 frameless always-on-top dengan komunikasi via inbox file di `%TEMP%\laraenv-cmdbar`.
- Embed: `frontend/dist`, `build/windows/icon.ico`, `langs` (katalog i18n).
- **Hanya 1 struct di-bind Wails** (`Bind: []interface{}{app}`) → 274 method `App` = seluruh API frontend.

### B.2 `app.go` (6.230 baris) — "god file"
- Struct `App` ±50 field: semua manager (config, services, php, nodejs, hosts, 3 konfigurator vhost, registry PTY, installer, scanner project, workspaceFiles, sourceControl, ssh+sftp, updater, crons, cloud, deployments+coordinator/executor, devserver, queue, agents, notifications, gitworkspaces, routes, sysmetrics, dll.) + state konkurensi (mutex doctor/runtime/MCP, gate dokumen, flag detached/commandBar).
- `NewApp` (±350 baris): wiring semua dependency via closure yang menangkap `App`/`config.Store` — termasuk perbaikan relokasi root, emitter event, resolver PATH untuk cron/devserver, dll.
- Lifecycle: `startup` (±370 baris: tray, hotkey global, watcher inbox command-bar & attach, sweep vhost semua project, ticker sysmetrics 1,5 dtk, watcher direktori projects, start MCP, dsb.), `shutdown` (beda main/detached), `beforeClose` (minimize-to-tray), `onSecondInstance`.
- DTO Wails: `PhpFpmInfo`, `PeclExtensionInfo`, `SourceWithStatus`, `ServiceInfo`, `PortEntry`, `ProjectCommands`, `ProjectCreateError`, `EditorsInfo`, `TerminalsInfo`, `SSHTestResult`, `PickedKeyFile`, `UpdateCheckResult`, `deployStepInfo`, `ActivityEntry`, `LanguageOverridesPayload`.

### B.3 Area fungsional per file (lapisan desktop)

| Area | File | Isi kunci |
| --- | --- | --- |
| Window/Win32 | `winshow.go`, `elevation_windows.go` | focus-steal via AttachThreadInput+ALT-tap; relaunch UAC |
| Services/stack/installer | `app.go` §services | ListSources, RecommendedStack, BootstrapRecommended, InstallSource/InstallFromFile, Start/StopService, ReloadStack, Start/StopManagedAll, ListServices, tuning nginx/apache/php, GetEnabledServices/SetServiceEnabled |
| PHP & Node | `app.go` §runtimes | `refreshPhpFpmServices` (register `php-fpm:<ver>`, port `9000+maj*10+min`, worker pool +100/replica), ListPhpExtensions, SetPhpExtension, Get/SavePhpIni, ListPeclExtensions/InstallPeclExtension, List/SetActivePhp, List/SetActiveNode |
| Resolusi runtime | `app_runtime.go` (313 brs, fungsi murni) | snapshot serving-checkout, re-apply vhost saat runtime berubah, emit `project:runtime-changed` |
| Cron | `app_runtime_cron.go` + §cron | runner config-driven untuk `--cron-run`; List/Save/Delete/Toggle/RunCronNow; push cloud sync |
| Vhost/hosts/routes | `app.go` §vhost + `app_routes.go` (637 brs) | `applyVhost*` (tulis nginx fatal + apache/caddy best-effort + cert + hosts + restart), `applyProjectRouteSet` transaksional (snapshot→render→validasi `nginx -t`→restart→rollback), API worktree: GetAgentWorktreeRuntime, Start/StopAgentWorktreeDevServer, DetachAgentWorktreeRoutes, RemoveAgentWorktree, ConfigureWorktreeRouting |
| SSL/HTTPS | `app_ssl.go` (250), `app_ssl_acl*.go` (409 total), `app_localtls.go` (93) | toggle SSL transaksional dengan rollback DACL-faithful (SDDL capture, stage file, restore sebelum byte privat ditulis), tunggu port listening via `netports.TreeOwners`; env TLS untuk subprocess (`NODE_EXTRA_CA_CERTS`, `VITE_DEV_SERVER_*`) |
| Doctor | `app_doctor.go` (273), `app_doctor_fixes.go` (500) | DiagnoseProject/DiagnoseAllProjects (4 worker paralel, timeout 45 dtk); ApplyDoctorFix — 10 fix: `env, app_key, composer_dependencies, node_dependencies, storage_link, database, migrations, vhost, hosts, php_fpm` dengan progress per baris + cancel |
| Deployments lokal | `app_deployments.go` (587) | repo+executor interface, redaksi secret, List/Get/Create/Delete/Apply/Deploy/Destroy/Detect/Activity/ReadRunLog/ImportJSON/ImportCloud; event `deployment:<kind>:<plan|log|step|error|done>:<id>` |
| Cloud | `app.go` §cloud (±35 method), `app_cloud_sync_status.go`, `app_cloud_optional.go` | auth (register/login/logout/me/GitHub), billing (checkout stripe/paypal/portal), sync E2EE (SetSyncPassphrase, RekeySync, SyncNow — push/pull SSH hosts+crons+presets), cloud deployments (detect/apply/deploy/destroy/activity) |
| SSH/SFTP | `app.go` §ssh (±30 method) | CRUD host (+sync), Generate/Import/List key, TestSSHConnection, StartPtyForSSH (PTY remote), 13 method SFTP (browser, upload/download + progress + cancel, edit remote via temp file + watch) |
| PTY/terminal | `app.go` §pty + `app_workspace_pty.go` + `terminal_clipboard.go` | StartPty(Kind/ForProject/ForProjectCommand/In/Runtime/ForAgentSession), Write/Resize/StopPty (event `term:data:<id>`/`term:exit:<id>`), Save/LoadTerminalWorkspace, terminal eksternal (PowerShell/CMD/GitBash/Cmder), clipboard bridge native |
| Workspace files | `app_workspace_files.go` (99) | context project|agent → root; List/Read/Check/Save/ReadImage/OpenExternal |
| Dokumen | `app_document_workspace.go` (155), `app_document_shutdown.go` (134) | validasi JSON layout tab ketat (root-key 64-hex, anti path-traversal, cap 1 MiB); gate shutdown dokumen kotor (emit `documents:shutdown-request`, frontend membalas allow/cancel) |
| Source control | `app_source_control.go` (93) | Inspect/Status/Diff/Stage/Unstage/Commit/Push/GetOperation/Cancel/Busy |
| Agents | `app.go` §agents + `agent_activity.go` + `agent_hooks.go` + `agent_events.go` + `agent_context.go` + `app_agent_activity.go` (305) | enable/disable fitur, CRUD sesi (Launch/Resume/Stop/Rename/Archive/MarkRead), hooks install/uninstall, usage, activity collector multi-window (HTTP owner server loopback + token) |
| MCP | `mcp_settings.go` (220), `mcp_backend.go` (456), `mcp_exec.go` (271), `mcp_database.go` (296), `mcp_diagnostics.go` (394), `mcp_context.go` (133) | status/registrasi klien; backend 8 tool-group dengan mutation gate + allow-list exec + DB snapshot cap 512 MiB + timing loopback; konteks Laravel (logs/mail/capture) |
| Lain-lain | `app.go` §misc | ListPorts/KillPort (+reclaim orphan), dev server start/stop/status/logs, queue worker start/stop/restart/args, project CRUD + tags + TLD + proxy port + editor, CreateProject (8 jenis, progress event), editors list, OpenURL, GetSystemMetrics, update check/download/install, i18n (LoadCatalogues merge embedded+disk, overrides, watch bahasa, relabel tray) |

### B.4 Katalog event Wails (backend → frontend), ±60 nama

`app:notice` · `install:progress` · `install:installed` · `update:progress` · `bootstrap:done` ·
`php:installed` · `service:error` · `service:status` · `stack:changed` · `sys:metrics` ·
`cron:status` · `project:error` · `project:added` · `project:removed` · `project:ssl-changed` ·
`project:php-changed` · `project:node-changed` · `project:proxy-changed` · `project:tags-changed` ·
`project:tld-changed` · `project:create-progress` · `project:create-error` · `project:create-done` ·
`project:runtime-changed` · `project:paused-changed` · `term:data:<id>` · `term:exit:<id>` ·
`terminal:attach` · `commandcenter:intent` · `ssh:hosts-changed` · `ssh:progress:<opId>` ·
`devserver:status` · `devserver:log` · `queueworker:status` · `queueworker:log` ·
`agent:session-changed` · `agent:hooks-changed` · `agent:feature-changed` · `agent:usage-changed` ·
`agent:activity-changed` · `agent:activity-reset` · `agent:sessions-reset` · `agent:shared-checkout` ·
`sourcecontrol:operation` · `doctor:output` · `documents:shutdown-request` ·
`cloud:sync-status-changed` · `deploypresets:changed` · `deployment:<kind>:<event>:<id>` ·
`language:changed` · `mcp:status-changed` · `mcp:activity`

### B.5 API surface (274 method) — sebaran per domain
cloud/akun/sync ±35 · SSH/SFTP ±30 · agents/aktivitas ±25 · projects/editor/terminal ±30 ·
services/stack/installer ±25 · PTY ±12 · deployments (lokal+cloud+presets) ±25 ·
routes/worktree 13 · SSL/CA ±7 · doctor 4 · workspace docs 9 · source control 10 · MCP 4 ·
cron 5 · i18n 5 · lifecycle/window ±10 · misc (port, metrik, update, clipboard, URL) ±12.

---

## C. Paket `internal/` (45 paket — diverifikasi hitung direktori)

### C.1 Tabel induk (nama · baris kasar · tujuan · dependensi internal)

| Paket | ±Baris | Tujuan | Dep internal |
| --- | --- | --- | --- |
| `paths` | 579 | Sumber tunggal semua lokasi disk (root, 40 accessor direktori) | — |
| `atomicfile` | 1.663 | Penulisan file crash-safe 2 tier (rename atomik; os.Root + pin DACL + validasi nama reserved Windows) | — |
| `localtls` | 477 | Env TLS untuk subprocess (NODE_EXTRA_CA_CERTS, VITE_DEV_SERVER_*) tanpa sentuh trust mesin | — |
| `runtimeinventory` | 223 | Identitas runtime dari **resource versi exe** (bukan nama folder) | — |
| `webviewruntime` | 689 | Recovery WebView2 (scan EdgeWebView, validasi PE + Authenticode Microsoft) | — |
| `applicationcontext` | 1.327 | Bukti runtime Laravel read-only untuk MCP (provider PHP embedded, capture JSONL, log, mail Mailpit) | — |
| `i18n` | 1.807 | Katalog JSON flat-code, plural CLDR, override disk, error ter-i18n global | — |
| `credentials` | 157 | DPAPI Protect/Unprotect (prefix `dpapi:v1:`) | — |
| `config` | 2.199 | `config.json` seluruh preferensi (Stack, Projects, Services tuning, Cloud, CommandCenter, Crons raw) + relokasi root + encode password DPAPI | atomicfile, credentials, i18n, paths |
| `vcruntime` | 571 | Deteksi redist VC++ hilang/usang (0xc0000135, mismatch vs17/vs18) | i18n |
| `netports` | 409 | Win32 GetExtendedTcpTable: port→PID, TreeOwners, KillPort dengan allow-list exe | i18n |
| `relocate` | 197 | Rebase path absolut dalam JSON state setelah environment dipindah | paths |
| `nodejs` | 127 | Multi-versi Node (`nodejs\<ver>`, junction `current`) | i18n |
| `devserver` | 1.250 | Jalankan `npm run dev` terkelola; deteksi port dari log 3 prioritas; kill tree + KillPort | i18n, netports |
| `projectroutes` | 1.036 | Tabel routing hostname→target (canonical + worktree preview) persist `state/project-routes.json` | atomicfile, i18n |
| `php` | 1.278 | Multi-versi PHP, php.ini (ext default + self-heal + tuning), ekstensi (+`php -n -m` statis), port FPM, CA cert (curl.se + merge CA lokal) | atomicfile, i18n, localtls |
| `services` | 1.932 | Supervisor 8 service + pool php-fpm; exclusive web; first-run init (mysqld --initialize-insecure, initdb, conf redis); auto-restart backoff; restricted token postgres; KillOrphans | config, i18n, paths, vcruntime |
| `vhost` | 2.977 | Render config 3 server (nginx/apache/caddy) dari 1 VhostSpec; upstream php terkelola; local Root CA 4096 + leaf 2048 + trust certutil; hosts editor marker `# lara4ever` | atomicfile, config, i18n, paths |
| `installer` | 3.164 | Katalog pinned 40+ sumber (nginx, caddy, mailpit, postgres Manual, pgvector, apache Manual, mysql, PHP 5.6–8.6beta NTS, python, node, redis, composer phar embedded, cmder); cache, progress, zip-slip guard, legal retention; PECL 6 ekstensi; marker/sentinel install-state | i18n, paths, runtimeinventory, vcruntime |
| `projects` | 3.108 | Deteksi jenis (laravel/wordpress/public/plain + framework JS + package manager); resolusi constraint PHP (`.php-version`, composer.json) & Node (`.nvmrc`, engines) dengan grammar Composer/npm subset; creator 8 jenis (composer create-project, create-*, git clone, WP zip); post-setup (DB create, .env patch, migrate --seed, npm build); watcher fsnotify | config, i18n, installer, paths, php, vcruntime |
| `terminal` | 220 | Buka terminal eksternal (PowerShell/CMD/GitBash/Cmder) di folder project + prefix PATH | i18n |
| `pty` | 756 | Registry PTY: ConPTY (UserExistsError/conpty) Windows, creack/pty Unix; env `LARAENV_TERMINAL_ID`/`LARAENV_AGENT_OWNER`; PATH live dari registry Windows | — |
| `gitworkspace` | 834 | Resolusi workspace git (current/branch/worktree) via git CLI; containment anti path-escape | i18n |
| `sourcecontrol` | 3.628 | Facade Git tervalidasi: `--porcelain=v2 -z`, authority os.Root + identity digest repo (anti TOCTOU), redaksi URL userinfo streaming, pathspec literal via stdin, operasi async + mutual exclusion per common-dir | workspacefiles |
| `ssh` | 2.871 | x/crypto/ssh: kunci (ed25519/RSA/ECDSA OpenSSH), host CRUD, known_hosts TOFU + lock inter-proses + revokasi, ProxyJump rekursif/ProxyCommand, PTY remote, SFTP registry + progress, snapshot beku-utk-deploy | config, i18n |
| `deploy` | 2.862 | Engine provisioning bash remote: ApplyConfig (domain+env prefix, PAT splice), Detection ±24 probe, BuildPlan 1.037 brs (apt, sury PHP, clone preserve .env/storage, vhost, FPM pool, DB access scripts UFW, certbot, autopull cron), preset `{token}` + 3 builtin embedded, DB scripts embedded, RunApply streaming | config, i18n |
| `deploycoord` | 84 | Mutex single-flight mutasi deploy | — |
| `deployments` | 1.595 | Store lokal terenkripsi DPAPI (index + envelope, run 100×4MiB, recovery `interrupted`, import bundle) | atomicfile |
| `cron` | 1.068 | robfig/cron in-proses + Task Scheduler (`schtasks /Create ... --cron-run <id>` heartbeat per-menit, cek jadwal saat invoke); eksekusi `cmd /C` PATH-augmented, timeout 5 mnt kill-tree | i18n |
| `queueworker` | 559 | `php artisan queue:work` per project; ring 500 baris; kill tree | i18n |
| `agents` | 13.487 | Subsistem AI agent terbesar: katalog 4 provider (claude/codex/kimi/opencode + reject shim .ps1/.cmd), invocation per provider (session-id cold resume), manager launch/resume/adopsi hook di terminal mana pun, hooks config per provider (settings.json/hooks.json/config.toml/plugin.js) + lifecycle transient, signal spool JSON validasi regex, discovery sesi (jsonl Claude / app-server JSON-RPC Codex / session_index Kimi / `session list` OpenCode), activity multi-window (HTTP loopback + token + fence PID+creation-time), usage (API Anthropic/Codex OAuth) | pty, i18n, atomicfile |
| `mcpbridge` | 1.513 | MCP server loopback (go-sdk resmi): streamable HTTP + bearer 32B + origin check + cap; 8 tool `laraenv_*` dengan schema action; audit tanpa argumen; stdio proxy `kitodock mcp` | — |
| `mcpclient` | 437 | Registrasi MCP ke Claude/Codex/Cursor (JSON/TOML, backup bertanggal, tulis atomik 0600) | atomicfile |
| `editor` | 238 | Deteksi & buka VS Code/Cursor/PhpPyrm/Sublime/custom | i18n |
| `workspacefiles` | 2.196 | Akses file tervalidasi: ContextRef (bukan path caller), os.Root + identitas NTFS file-ID, list paginasi cursor, read 2MiB, save optimistic-revision + lock per-dokumen, WebP chunk validation, BOM/EOL preserve | atomicfile |
| `tray` | 616 | Systray (energye/systray): menu service start/stop/all, project + submenu tag, buka folder/editor/terminal/browser, quit; relabel i18n | — |
| `hotkey` | 267 | 1 hotkey global Win32 RegisterHotKey (thread lock OS, rebind) | i18n |
| `updater` | 977 | Self-update GitHub Releases: asset MSI+exe + sha256 sidecar (wajib portable), apply via `run-update.cmd`/`run-portable-update.cmd`, tolak installer-over-portable | i18n, paths |
| `version` | 7 | `var Version` via ldflags | — |
| `cloud` | 2.253 | Klien backend Laravel: session discipline (generasi + invalidasi), sync resource E2EE AES-256-GCM + Argon2id (m=64MB,t=3,p=4), rekey, 409 retry, engine_compat alias deploy, billing, GitHub OAuth, quote | config, deploy, i18n |
| `sysmetrics` | 295 | CPU via PDH (\Processor Information), GPU engine counters, mem/disk gopsutil, vendor via PowerShell | — |
| `sitedoctor` | 2.683 | Pemeriksaan project diagnose-only: docroot, resolusi runtime, vendor, node_modules, .env strict-parser, APP_KEY, storage link (junction-aware), wp-config, probe DB via PHP PDO script embedded (klasifikasi SQLSTATE, hanya loopback + blok DB sistem), migrations `migrate:status`, vhost drift, hosts, php-fpm | projects |
| `notifications` | 745 | Pusat notifikasi + toast OS: diff sesi agent (needs-input/done/error), threshold usage 80/95%, rate-limit 1/menit | agents, i18n, atomicfile |
| `clipboard` | 593 | Clipboard text native user32 tanpa cgo | — |
| `documentshutdown` | 309 | Gate ACK dokumen kotor sebelum quit/update (event ke frontend, satu pending) | — |

### C.2 Graf dependensi berlapis (tanpa siklus)
```
Layer 0 (daun): paths, atomicfile, localtls, runtimeinventory, webviewruntime,
                applicationcontext, i18n, credentials, pty, terminal*, tray, hotkey,
                sysmetrics, clipboard, documentshutdown, deploycoord, version, editor,
                netports, vcruntime, mcpbridge
Layer 1:        config, nodejs, devserver(netports), projectroutes, php(localtls), cron,
                queueworker, gitworkspace, updater(paths), relocate(paths), mcpclient(atomicfile)
Layer 2:        services(config,vcruntime), vhost(config), installer(runtimeinventory,vcruntime),
                workspacefiles(atomicfile), ssh(config)
Layer 3:        projects(config,installer,php,vcruntime), sourcecontrol(workspacefiles),
                deploy(config), agents(pty)
Layer 4:        cloud(config,deploy), sitedoctor(projects), notifications(agents),
                deployments(atomicfile [+credentials runtime])
Titik komposisi: app.go (menyuntik resolver/apply/emitter antar paket)
```

### C.3 Teknik Windows yang dipakai (harus dipertahankan/di-port)
- `CREATE_NO_WINDOW` pada semua spawn; kill via `taskkill /F /T /PID` (+ `/IM` fallback).
- ConPTY (pseudoconsole) untuk terminal; drain 100ms pasca-exit (keanehan conhost).
- `MoveFileEx(REPLACE_EXISTING|WRITE_THROUGH)` rename atomik; `NtCreateFile` + `OBJ_DONT_REPARSE` + os.Root untuk anti symlink/traversal; SDDL/DACL capture-restore.
- DPAPI (CryptProtectData) untuk password SSH & payload deployment.
- `GetExtendedTcpTable` (iphlpapi) untuk pemetaan port→PID; Toolhelp32 snapshot untuk nama proses/pohon.
- `certutil -addstore/-store ROOT` untuk trust CA; `schtasks` untuk cron background; `RegisterHotKey` + GetMessageW untuk hotkey; registry (SYSTEM\...\Environment + HKCU) untuk PATH live.
- Restricted token (CreateRestrictedToken, disable Administrators) untuk postgres + DACL user grant.
- Authenticode (WinVerifyTrust) untuk kandidat recovery WebView2.
- Manifest requireAdministrator + relaunch `runas`.

---

## D. Frontend React (`desktop/frontend/src`, 238 file, 33.484 baris)

### D.1 Kerangka
- React 18 + TS strict + Vite 6 (dev port 5173 strict, fs.allow untuk glob i18n `../langs/*.json`).
- **Tanpa router** — semua halaman di-mount, toggle `display:none` (keep-alive).
- **Tanpa state library** — Context (i18n, dialog) + useReducer (terminal workspace) + class external store + useSyncExternalStore (dokumen, source control) + localStorage prefs.
- Tailwind v4 via PostCSS dengan config v3-kompat (palet sRGB hex); 6 tema gelap (nebula/aurora/sunset/crimson/oceanic/midnight) via `data-theme` CSS vars; glassmorphism classes.
- Binding: `../wailsjs/go/main/App` (generated) + `runtime` (EventsOn/Quit/WindowMinimise...). ±150 method dipakai.
- Test: Vitest 4 + Testing Library + jsdom; 88 file test kolokol; meta-test anti string tak-terterjemahkan + validasi katalog.

### D.2 Tiga kepribadian jendela (`App.tsx`)
`MainApp` (shell penuh) · `CommandBarWindow` (palette Spotlight proses terpisah; intent via `SendCommandIntent` lalu quit) · `DetachedTerminalWindow` (terminal satelit + navigator agents + attach kembali).

### D.3 Halaman (11)
| Halaman | Baris | Isi |
| --- | --- | --- |
| Dashboard | 394 | Grid service + start/stop all + bootstrap stack modal + modal VC++; kartu runtime PHP/Node; SystemMetrics; PhpFpmStatus |
| Projects | 1.530 | Master-detail; quick launch; dev server split-button + proxy toggle + logs; queue worker; project scripts; runtime selector; Site Doctor; vhost editor; SSL + trust CA; TLD; tags; delete (drop DB opsional); modal 8 jenis create |
| SSH | 130 | Kartu host + tag prod + recent localStorage + pencarian `/` + grid/list + modal host/keygen/import |
| Cron | 385 | Tabel job + toggle/run-now/log/edit + 6 preset + filter project |
| Deployments | 1.405 | Dual sumber lokal/cloud; detect/apply/deploy/destroy dengan timeline langkah streaming; modal ApplyConfig Monaco; run history; import cloud→lokal; presets |
| Downloads | 419 | Katalog installer per kind+versi; progress bytes/kecepatan/ekstrak; manual open-page/install-from-file; toggle tampil dashboard |
| Settings | 975 | Tab: umum+tampilan+bahasa+legal; layanan (CA card, tuning nginx/apache/php); command center; eksperimental (MCP, agent sessions/hooks, reset metadata); update; akun |
| Account | 703 | Cloud opt-in; login/register/GitHub; kartu akun; sync passphrase/rekey/now; plan & checkout stripe/paypal; base URL override |
| Updates | 248 | Cek silent, kartu versi, download progress, install & restart |
| Agents | 475 | Navigator: tab scope (aktif/recent/arsip), pohon project→lokasi→sesi, aksi per sesi (buka/resume/rename/stop/arsip/worktree dev server/detach routes/remove), auto-switch |
| Terminal (workspace) | ±1018+ | Bukan halaman biasa — workspace jendela mengambang (lihat D.4) |

### D.4 Subsistem terminal (`components/terminal/`)
- `Terminal.tsx`: 1 instance xterm.js per pane (FitAddon + WebglAddon fallback DOM), tema Tabby, scrollback 5000, tunggu font load, clipboard bridge, remap Ctrl+Backspace per shell.
- `TerminalWorkspace.tsx`: reducer 22 aksi; kanvas **group mengambang** drag/resize 8-handle/snap/maximize; **tab** strip browser-like (rename, reorder, merge, tear-off, drag keluar jendela = detach proses baru); **split** via react-resizable-panels + drop-zone 5 tepi; spawn local/project/ssh/agent; persist layout 500ms debounce + hydrate respawn; attach kembali; hotkey Ctrl+T/W; picker PHP/Node per pane; badge MAIN/PREVIEW route.
- Pendukung: `workspaceReducer`, `types` (Workspace/Group/Tab/Pane/SpawnDescriptor/SnapState), `serialize`, `snap`, `dnd` (MIME + hit-test), `openTerminals`, `adoptedLeaf`, `SSHConnectingTimeline` (tahap dial→handshake→session→pty→shell).

### D.5 Dokumen & editor (`components/documents/`)
Monaco **divendorkan lokal** (`/vs`, anti-CDN); wrapper API tervalidasi; store eksternal buffer (revision, dirty, conflict); tab strip campur dokumen+review Git; Preview/Source; `MarkdownPreview` (react-markdown+GFM, gambar via API base64, guard link); `ConflictDiff` (DiffEditor side-by-side/inline + save-merged); polling 2 dtk; `useDocumentShutdown` jembatan gate close; dialog tutup kotor (portal focus-trap).

### D.6 Source control (`components/sourcecontrol/`)
Panel Git: header checkout (branch/upstream/ahead-behind), 2 pohon perubahan (Staged=index vs Changes=worktree), stage/unstage dengan dialog "save & stage" bila buffer kotor, commit seluruh index, push eksplisit (remote/branch/upstream, tanpa force/tags), kartu operasi live + output, review diff tab Monaco, expected-state guard, polling 2 dtk.

### D.7 Agents UI (`components/agents/`)
Tipe & kontrak sesi; activity API tervalidasi (lease per window); model hitung + pohon subagent; `AgentMapDialog`; `useAgentMap` (2 lease); auto-switch (reducer + queue + deteksi aktivitas mengetik PTY 3 dtk hening); `AgentLauncherMenu` (provider, git mode, worktree+routing canonical/preview hostname, folder konteks tambahan, cross-project search); usage bar; onboarding; filter; glyph/icon provider.

### D.8 Workspace chrome & command center
- `workspace/`: navigator sidebar 3-tab (Agents/Files/Git) ARIA, preferensi localStorage, resize.
- `commandCenter/`: tipe intent (navigate/terminal/service/openTerminal/openFolder/openEditor/openBrowser/newProject/ssh/openAgents/launchAgent/focusTerminal/focusAgentSession/resumeAgentSession + target native|xterm); `builtins` (navigasi, service, create 8, per-project, terminal terbuka); `launcher` (`<project> <provider>` agent, `<project> <cmd>` + autocomplete artisan/npm, `ssh:<host>`); parse hotkey kanonik; `CommandPalette` (Ctrl+K, token-AND, Tab complete, Shift+Enter altRun, backdrop solid anti-bug WebView2); `CommandCenterSettings` (rebind + CRUD shortcut custom).

### D.9 Komponen root lain (±25)
Sidebar (8 nav, collapse 56px, versi, update dot, akun GitHub avatar, donate) · WindowControls · ErrorBoundary · DialogProvider (Promise confirm/alert/prompt queue) · PortalMenu · GlassCard/FilterChip/TypeBadge/JSBadge · LanguageSelector (status override disk + needs-review) · LegalNotices · NotificationBell/Inbox · ServiceCard/ServiceIcon/SystemMetrics/PhpFpmStatus · ServiceLogsModal · PortsModal (+KillPort) · modal Projects (New 8 jenis, Delete, VhostEditor Monaco, SiteDoctor+Overview streaming, Tags, DevServerLogs, QueueParams) · ProjectRuntimeSelector · **PhpExtensionsModal 510 brs** (toggle ekstensi, PECL + progress DLL, editor php.ini) · modal SSH (Host, KeyGen, ImportKey) · **SFTPModal 758 brs** (dual-pane, upload/download progress+cancel, edit remote/temp-watch) + SFTPFileEditorModal + SFTPExternalEditSession · DeployPresetsModal · DeployScriptPreview.

---

## E. Data & State on-disk

| Lokasi | Isi |
| --- | --- |
| `<root>\config.json` | Seluruh preferensi (InstallRoot, Language, Stack.Enabled, Projects.*, Services tuning, Cloud, CommandCenter, Crons raw, Terminal/DocumentWorkspace layout) — atomic + preserve-perm, password SSH DPAPI |
| `<root>\state\` | `project-routes.json`, `mcp-activity.json`, `mcp-descriptor` (DPAPI), `mcp-snapshots\`, `deployments\` (index + run terenkripsi), store sesi agent, notifikasi |
| `<root>\langs\` | Override katalog i18n disk (merge saat load) |
| `<root>\www\` | Root project default |
| `<root>\php\<series>\` · `nodejs\<ver>\` · `nginx|apache|caddy|mysql|postgres|redis|mailpit|python|cmder|composer\` | Instalasi runtime/service |
| `%TEMP%\laraenv-cache` · `%TEMP%\laraenv-cmdbar` · `%TEMP%\laraenv-attach` | Cache installer · inbox command-bar · inbox attach terminal |
| `C:\Windows\System32\drivers\etc\hosts` | Entri marker `# lara4ever` |
| Sertifikat | `<nginx|apache>\certs\` + `lara4ever-rootCA.crt/.key` (CN "LaraEnv Local CA (<root>)") |
| Task Scheduler | `LaraEnv\<jobID>` (cron background) |
| Provider eksternal | `~/.claude/settings.json`, `$CODEX_HOME/hooks.json`, `$KIMI_CODE_HOME/config.toml`, opencode plugin, `~/.claude.json` + `~/.cursor/mcp.json` (MCP) |

---

## F. Build, Rilis, CI & Tooling

- **Toolchain**: Go 1.26.10, Node ≥24, Wails 2.12.0 **dibangun dari module graph repo** (bukan `go install @version`), WebView2 terpisah, WiX 5.0.2 + ekstensi UI/Util (hash dipin di `tools/licenses/policy.json`), PowerShell 7, .NET SDK 8 (MSI).
- **`build-release.ps1`**: validasi notice → build wails (`-webview2 browser`, ldflags versi) → artwork staging → `wix build` MSI → portable ZIP → sidecar SHA-256 semua artefak; direktori output wajib kosong.
- **WiX** (`laraenv.wxs`): perMachine, MajorUpgrade anti-downgrade, payload legal (LICENSE/NOTICE/THIRD_PARTY_NOTICES/dependencies.json/**wix source zip MS-RL**), auto-launch custom action. **NSIS** opsional (`project.nsi`) tanpa bootstrapper WebView2.
- **Portable**: marker `.laraenv-portable`; README larang ekstraksi ke `C:\LaraEnv`.
- **CI `desktop-checks.yml`** (windows-2025): provisioning offline (mod verify, npm ci ignore-scripts, build wails CLI), verifikasi pin WiX (unduh-inspeksi, tanpa eksekusi), `build-desktop.mjs` offline, test+lint frontend, `node --test` tooling, `go test ./...`, cross-compile deploy-tui (win+linux), harness screenshot tanpa browser, `git diff --exit-code` guard file generated/lockfile. **`langs.yml`**: validasi katalog.
- **`tools/`**: `ci/`, `langs/` (check/pull/push-en), `licenses/` (generator notices + policy runtime 40 sumber + 41 PECL + composer phar parser), `screenshots/` (Playwright + fake backend + frame CSS + GIF), `source-snapshot/` (export ZIP deterministik + manifest), `rootprobe/` (diagnostik root).
- **`cmd/deploy-tui`** (707 brs): bubbletea+huh; provision VPS Linux end-to-end memakai `internal/deploy`+`internal/ssh` yang sama; deteksi→plan→eksekusi streaming; sudo -n; tanpa rollback.
- **Legal**: Apache-2.0 + NOTICE + THIRD_PARTY_NOTICES ±2,6MB + audit `docs/opensource-audit/2026-10-21/` (11 dokumen); perbaikan ZIP pgvector/redis/imagick terdokumentasi.

## G. Fitur per Rilis (ringkas)
- **0.5.4**: workspace sidebar (Agents/Files/Git), editor file + preview, Git panel, clipboard bridge, MCP lokal opsional (8 tool), DB remote access deploy.
- **0.5.5**: redesign Projects (list+search+tag+paginasi+detail), deteksi otomatis PHP/Node/PM, Site Doctor.
- **0.5.6**: project scripts package.json, custom command, dev controls semua project JS, queue worker, Agent Map.
- **0.5.7**: known_hosts TOFU + DPAPI password, HTTPS lintas runtime (CA PHP/Node/Vite), tool `laraenv_context` + integrasi Laravel 2-file + guidelines, reliabilitas startup (WebView2 recovery, config atomik).
- **0.5.8**: perbaikan cron timeout kill-tree penuh.
