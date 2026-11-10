# 03 — Roadmap Detail (Fase 0–6)

> Setiap fase: tujuan · modul backend · UI · acceptance criteria (AC) · dependensi.
> Nomor paket merujuk ke `internal/` LaraEnv (lihat `01-inventaris-laraenv.md` §C);
> keputusan port/adaptasi/tulis-ulang ada di `04-mapping-dan-rename.md`.

---

## Fase 0 — Fondasi Proyek

**Tujuan**: kerangka repo + aplikasi shell yang bisa dijalankan, dengan fondasi paket level-0
yang akan dipakai semua fase berikutnya.

**Backend**
- `go.mod` (module `kitodock`), toolchain Go 1.26, Wails v2.12 (dibangun dari module graph),
  `.gitignore`, golangci-lint/vet config.
- `internal/paths` — resolusi root (env `KITODOCK_ROOT` > marker `.kitodock-portable` >
  `C:\kitodock`), 40 accessor direktori. Port langsung + rename.
- `internal/atomicfile` — 2 tier (Write sederhana; ReplaceInRoot dengan os.Root + pin DACL +
  validasi nama reserved). Port langsung (kode paling sensitif, sudah teruji berat).
- `internal/i18n` — katalog JSON, `T` + plural CLDR + interpolasi, error global, override disk,
  deteksi locale OS. Port + katalog awal **id** dan **en**.
- `internal/credentials` — DPAPI wrapper.
- `internal/config` — Store config.json (schema Fase 0: InstallRoot, Language, Stack, Projects
  kosong; schema berkembang per fase), reconcile relokasi, watch fsnotify, encode DPAPI.
- `internal/version`, `internal/notifications` (kerangka store saja), `internal/clipboard`.
- `desktop/main.go` (dispatch mode; hanya GUI aktif), `desktop/app.go` tipis + `events.go`
  (EventBus + konstanta), `desktop/lifecycle.go`, `elevation_windows.go`, `winshow.go`.
- CI GitHub Actions: build wails offline + `go test ./...` + lint.

**Frontend**
- Scaffold Vite+React+TS+Tailwind v4; `main.tsx` → ErrorBoundary → I18nProvider →
  DialogProvider → App; 6 tema `data-theme` + style.css inti (glass, modal, input, scrollbar);
  Sidebar (nav placeholder semua halaman) + WindowControls + frameless drag; halaman kosong
  per nav; Settings minimal (tema + bahasa); lib/i18n.tsx (port).
- Test scaffold Vitest + meta-test katalog i18n.

**AC**
1. `wails dev` & `wails build` sukses; exe jalan non-elevated (shell saja) dan elevated.
2. Ganti tema & bahasa (id/en) langsung efektif; pilihan persist di config.json (atomic).
3. `internal/paths` test: resolusi env/marker/fallback benar; `atomicfile` test port lulus
   (termasuk matriks DACL Windows).
4. CI hijau.
5. EventBus: emit `language:changed` dari backend diterima frontend (bukti jalur event jalan).

**Dep**: — (fase pertama).

---

## Fase 1 — Services & Installer

**Tujuan**: bisa menginstal dan menjalankan stack layanan lokal dari UI.

**Backend**
- `internal/vcruntime` — smoke-test `--version`, deteksi 0xc0000135 & mismatch vs17/vs18, RedistURL.
- `internal/runtimeinventory` — baca versi dari resource exe (GetFileVersionInfo).
- `internal/services` — Manager + Definition (Deps, Exclusive "web", Internal, Replicas,
  FirstRunInit mysqld/initdb/redis-conf, DropAdminToken postgres + DACL grant, Env);
  lifecycle Start/Stop/Restart/Reload/StopAll/KillOrphans; waitAndMaybeRestart backoff
  (internal 500ms→8s, give-up 10 crash <30 dtk); logRing 500 baris; WatchStatus 1,5 dtk;
  TrackedPIDs.
- `internal/installer` — Catalog pinned (mulai: nginx, mysql, mailpit, redis, php subset versi
  populer, node LTS, composer embedded; postgres/apache/caddy menyusul di katalog tapi tetap
  didukung), Manager download (UA/Referer per-situs, validasi zip, `.part`+rename, progress
  250ms), extract (zip-slip, flatten, legal-preserve), marker `.kitodock-version` + sentinel,
  BackfillMarkers, EnsureBundledComposer (phar embedded), PECL (xdebug/redis/mongodb/apcu/
  imagick/memcached) bisa masuk Fase 2 bila PHP manager belum siap — **putusan: katalog +
  install dasar di Fase 1, PECL di Fase 2**.
- `internal/tray` — systray menu service + quit + label i18n (project menu menyusul Fase 2).
  *(Realisasi: VcRedistModal tidak jadi modal khusus — URL redistributable mengalir lewat event
  `service:error` + dialog; NotificationBell + GPU engine PDH terealisasi ronde review-final;
  unduhan PECL biner tetap DIJADWALKAN ULANG [distribusi DLL per versi]; lihat
  docs/progress/review-final.md.)*
- `internal/sysmetrics` — PDH CPU + GPU engine + gopsutil mem/disk + vendor.
- `desktop/controllers/services.go`, `installer.go`, `system.go` (metrics, ports via netports —
  netports masuk fase ini juga karena PortsModal), wiring lifecycle: KillOrphans saat startup,
  watcher status/error.

**Frontend**
- Halaman **Downloads** (katalog per kind, progress bytes/kecepatan/ekstrak, manual open-page/
  install-from-file, toggle tampil-dashboard) — port.
- Halaman **Dashboard** (grid service start/stop + all, modal bootstrap stack, modal VC++,
  kartu runtime placeholder→aktif setelah Fase 2, SystemMetrics, ServiceLogsModal, PortsModal).
- Sidebar final; NotificationBell (kerangka).

**AC**
1. Bootstrap recommended stack (nginx+mysql+mailpit) dari UI: unduh → extract → start →
   `service:status` running; log service tampil.
2. MySQL first-run initialize-insecure sukses; Postgres initdb + restricted token (bila
   postgres masuk katalog) sukses; Redis conf digenerate.
3. Konflik web-server (nginx+apache bersamaan) → pesan jelas (Exclusive group), bukan socket error.
4. Kill service membunuh pohon proses; KillOrphans saat startup membersihkan sisa run lama.
5. Kurangi/naikkan versi VC++ simulasi → VcRedistModal muncul dengan URL benar.
6. Tray: start/stop all + quit bekerja; label mengikuti bahasa.
7. PortsModal: daftar port listener + KillPort dengan allow-list exe.
8. `go test` services/installer lulus (port test LaraEnv sebagai baseline).

**Dep**: Fase 0.

---

## Fase 2 — Runtimes, Projects, Vhost & HTTPS

**Tujuan**: inti produk — project `.test` tervalidasi, multi-versi PHP/Node, HTTPS lokal,
dev server ter-proxy. Fase terpadat; boleh dipecah 2a/2b bila perlu.

**Backend**
- `internal/php` — multi-versi + junction `current`, EnsureIni (ext default + self-heal),
  tuning, PortFor `9000+maj*10+min` + ReplicaPort +100/replica + UpstreamName, extensions +
  `php -n -m` statis, EnsureCaCert (curl.se + merge CA lokal) ; PECL (dari Fase 1 katalog).
- `internal/nodejs` — multi-versi + junction.
- `internal/projects` — detector (laravel/wordpress/public/plain + JS framework + package
  manager), Scanner + merge preferensi config, resolusi constraint (`.php-version`,
  composer.json, `.nvmrc`, `.node-version`, engines; grammar Composer/npm subset + lts),
  Creator 8 jenis (laravel composer create-project, wp zip, git clone, vite/next/nuxt/astro,
  blank) + StripPailFromDevScript, PostSetupLaravel (DB create + .env + migrate + npm build),
  watcher fsnotify (lifecycle + runtime manifest).
- `internal/vhost` — VhostSpec; render nginx (3 bentuk: proxy dual-stack ::1 backup + websocket
  upgrade, static, php try_files + fastcgi upstream) + apache (FallbackResource + SetHandler
  proxy:fcgi + mod_rewrite ws) + caddy (`http://` non-TLS, tls reuse cert, php_fastcgi);
  EnsurePhpUpstreams blok terkelola; tuning nginx/apache; **certs**: Root CA 4096 (CN
  `Kitodock Local CA (<root>)`) + leaf 2048 SAN wildcard + trust certutil (auto MSI/portable
  manual via LocalCAStatus/TrustLocalCA); hosts editor marker `# kitodock` + snapshot/restore
  pintar (pecah baris shared).
- `internal/projectroutes` — store + manager (canonical + preview worktree, validasi hostname,
  transaksi commit→apply→rollback store, MigrateTLD, RepairMissingTargets).
- `internal/localtls` — env TLS subprocess (NODE_EXTRA_CA_CERTS, VITE_DEV_SERVER_* dengan cek
  plugin laravel-vite).
- `internal/devserver` — start custom/default command, pickFreePort dari 3000, deteksi port
  dari log 3 prioritas, kill tree + KillPort allowlist `StackExes`.
- `internal/sitedoctor` — versi inti: docroot, resolusi runtime, vendor/node_modules, .env,
  APP_KEY, storage link junction-aware, probe DB (PHP PDO embedded, loopback only),
  migrations status, vhost drift, hosts, php-fpm. Fix engine (`app_doctor_fixes` pattern)
  10 fixID + cancel.
- `internal/relocate`, `internal/netports` (TreeOwners untuk SSL listener wait).
- `desktop/controllers/`: runtimes.go (versi+ekstensi+ini+fpm refresh pool per versi + bind dep
  nginx/apache), projects.go (CRUD/create progress/tags/tld/proxy/commands), vhostssl.go
  (vhost editor + applyProjectSSL transaksional dengan rollback DACL + wait port + routes/
  worktree API), devserver.go (dev server + queue worker + cron **cron/queue bisa digeser ke
  Fase 3 bila jadwal ketat**), doctor.go.
- Startup: sweep vhost semua project, EnsureActive PHP, EnsureCaCert per versi, watcher
  direktori projects → add/remove vhost + event.

**Frontend**
- Halaman **Projects** penuh (port 1.530 baris): browser+detail, quick launch, runtime selector,
  SSL toggle + trust CA flow, proxy toggle, Site Doctor modal + overview, vhost editor Monaco,
  tags, TLD, delete, NewProjectModal 8 jenis, PhpExtensionsModal, DevServerLogs.
- Halaman **Downloads** melengkap (tuning forms masuk Settings).
- **Settings** — tab Services: CA card + tuning nginx/apache/php.
  *(Realisasi: CA card + diagnostik WebView2 ada di panel Diagnostics; PhpExtensionsModal
  TEREALISASI (ini toggle + live load) di ronde review-final; unduhan PECL biner dan tuning
  forms tetap DIJADWALKAN ULANG — lihat docs/progress/review-final.md.)*

**AC**
1. `composer create-project laravel/laravel` dari UI → project terdeteksi laravel → vhost
   `nama.test` dibuat (nginx+apache+caddy sekaligus) + hosts + browser bisa membuka halaman
   Laravel via php-fpm pool versi PHP project.
2. Constraint `~8.3.0||8.4` di composer.json + `.nvmrc 22` → resolver memilih versi terinstal
   yang cocok; jika tak ada → saran InstallVersion + deep-link Downloads; tak pernah silent
   fallback salah versi.
3. Toggle SSL: cert dibuat, nginx listen 443, port listener diverifikasi via TreeOwners,
   rollback bersih bila gagal (file vhost + cert + hosts + DACL dikembalikan).
4. `npm run dev` (vite) via dev server: port terdeteksi dari log, proxy nginx websocket jalan
   (HMR), stop membunuh seluruh pohon.
5. Site Doctor: diagnose project baru menemukan `.env` hilang + APP_KEY + DB → fix berurutan
   dengan output streaming + re-check.
6. Ganti TLD `.test`→`.local`: semua hostname + vhost + hosts + cert ikut (MigrateTLD).
7. Portable: pindah folder environment → startup self-heal (rebase config + rewrite state JSON
   + re-sweep vhost + rebase cacert).
8. Semua test unit resolver/vhost/routes/doctor lulus.

**Dep**: Fase 1 (installer untuk mendapat PHP/Node; services untuk nginx/db).

---

## Fase 3 — Terminal Workspace, Cron & Queue

**Tujuan**: terminal in-app kelas berat + penjadwalan.

**Backend**
- `internal/pty` — ConPTY (UserExistsError/conpty) + creack/pty unix; Registry `term-N`;
  env `KITODOCK_TERMINAL_ID`/`KITODOCK_AGENT_OWNER`; PATH live dari registry; observer
  aktivitas (seam untuk agents Fase 6).
- `internal/terminal` — terminal eksternal 4 jenis + prefix PATH project.
- `internal/cron` — engine robfig in-proses + persist config + Task Scheduler background mode
  (`schtasks` + `--cron-run` cek jadwal menit-berjalan) + executor kill-tree timeout 5 menit.
- `internal/queueworker` — `artisan queue:work` manager + ring log.
- `desktop/controllers/terminal.go` (StartPty* semua varian local/project/runtime/restore,
  Write/Resize/Stop, workspace persist, clipboard bridge, terminal eksternal, detach/attach
  inbox `%TEMP%\kitodock-attach`, jendela `--terminal-window`), `devserver.go` lengkap
  (cron CRUD/toggle/run-now + queue).
- Lifecycle: `startupDetached` mode.
  *(Realisasi: floating detach/reattach + personality `--terminal-window` TEREALISASI di ronde
  review-final: proses satelit via owner endpoint `term_poll/write/resize` + hub ring PTY;
  lihat docs/progress/review-final.md.)*

**Frontend**
- `components/terminal/` penuh: Terminal.tsx (xterm+fit+webgl+font-wait+clipboard+keys),
  TerminalWorkspace (reducer 22 aksi, floating group drag/8-resize/snap/maximize, tab
  browser-like drag/merge/tear-off/detach-keluar-jendela, split panel + drop-zone 5 tepi,
  spawn deskriptor, persist+hydrate respawn, hotkey Ctrl+T/W), SSHConnectingTimeline stub
  (aktif penuh Fase 5), picker runtime per pane.
- Halaman **Cron**; kontrol queue worker di Projects (sudah disiapkan Fase 2 UI-nya).

**AC**
1. Tab terminal PowerShell/CMD/GitBash (+Cmder bila diinstal): spawn, tulis, resize, exit →
   pane tertutup; env terminal-id terpasang.
2. Split horizontal/vertikal bertingkat; drag pane antar split; drag tab keluar canvas →
   jendela satelit proses baru berisi terminal hidup; attach kembali ke jendela utama.
3. Layout (posisi group/tab/split + spawn deskriptor) persist & respawn setelah restart.
4. Terminal project otomatis prefix PATH php/node project (versi sesuai resolver).
5. Cron `* * * * *` `php artisan schedule:run` berjalan in-proses; background mode
   terdaftar di Task Scheduler `\Kitodock\<id>` dan tetap jalan saat app ditutup; timeout
   membunuh pohon PHP.
6. Queue worker start/stop/restart + args + log ring di UI.
7. Test reducer/dnd/snap/serialize + pty registry lulus.

**Dep**: Fase 0–2 (PATH resolver runtime; UI Projects).

---

## Fase 4 — Files, Editor & Source Control

**Tujuan**: browser file aman + editor dokumen + panel Git.

**Backend**
- `internal/workspacefiles` — ContextRef authority (project|agent), os.Root + identitas NTFS,
  list paginasi cursor + stale detection, Read 2MiB / image (+WebP chunk), Save optimistic
  revision + lock per dokumen, BOM/EOL preserve, validasi path.
- `internal/gitworkspace` — resolusi current/branch/worktree (untuk agents nanti + checkout Git).
  *(Realisasi: paket TEREALISASI di ronde review-final — Worktrees/CurrentBranch/Resolve
  (read-only, 4 test git nyata); menjadi substrate preview-worktree; tab Agents di
  WorkspaceNavigator AKTIF sejak review-final.)*
- `internal/sourcecontrol` — facade git CLI tervalidasi penuh (authority digest + TOCTOU
  re-verify, porcelain=v2 parser, redaksi streaming, operasi async + exclusion per common-dir,
  stage/unstage pathspec-stdin literal, commit index penuh, push eksplisit, diff dua sisi
  dengan EOL/BOM/OID).
- `desktop/controllers/files.go` (workspace files + document workspace strict-JSON + shutdown
  gate via `internal/documentshutdown`), `git.go`.

**Frontend**
- `components/documents/` penuh (api normalized-codes, external store buffer, tabs + preview/
  source, Monaco + ConflictDiff, MarkdownPreview guard-link, WorkspaceFilesPanel tree paginasi,
  DocumentCloseDialog, useDocumentShutdown).
- `components/sourcecontrol/` penuh (panel 2 pohon, stage dialog dirty-buffer, commit/push,
  operasi live, review diff tab).
- Workspace navigator 3-tab (Agents tab placeholder aktif Fase 6; Files+Git aktif).
- Vendoring Monaco (`scripts/vendor-monaco.mjs`) + legal vendor script.

**AC**
1. Browse file project: navigasi, paginasi 500 entri, entri blocked (symlink/non-UTF8)
   ditandai; buka file teks ≤2MiB dengan BOM/EOL terjaga; mixed-EOL → read-only + info.
2. Edit + save: revisi di-cek; konflik eksternal → banner compare + save-merged bekerja.
3. Preview Markdown (GFM, heading anchor, gambar lokal via API, link eksternal diblokir);
   preview gambar PNG/JPEG/GIF/WebP ≤8MiB.
4. Quit/update dengan dokumen kotor → gate menahan proses sampai keputusan user.
5. Git: status 2 lapis benar; stage/unstage file tunggal & banyak; commit dengan hooks/signing
   jalan; push tanpa force; diff review side-by-side; operasi serial per repo; repo tertukar
   saat operasi (simulasi) → error `repository_changed`, bukan mutasi salah repo.
6. Stage file yang buffer-nya kotor → dialog save-and-stage; stage-disk; batal.
7. Test parser status + authority + documents lulus.

**Dep**: Fase 2 (context project), Fase 3 (workspace selection dari terminal pane).

---

## Fase 5 — SSH, SFTP & Deployment

**Tujuan**: kelola host/kunci, terminal & transfer remote, deploy resep bash dengan histori
terenkripsi.

**Backend**
- `internal/ssh` — kunci (MarshalPrivateKey ed25519/RSA/ECDSA), host CRUD + password DPAPI,
  known_hosts TOFU + lock file antar-proses + revokasi + algoritma, Connect stage timeline,
  ProxyJump rekursif/ProxyCommand `%h/%p`, Dial untuk probe, SFTPRegistry (upload/download
  progress + cancel + mtime watch edit-eksternal), Snapshot beku untuk deploy.
- `internal/deploy` — ApplyConfig (+PAT splice, remote DB access), RunDetection ±24 probe,
  BuildPlan (apt/sury/clone-preserve/vhost/fpm/db-scripts/certbot/autopull), preset engine
  `{token}` + 3 builtin embedded + database scripts embedded + NormalizeDatabaseAccess.
- `internal/deploycoord`, `internal/deployments` (index+envelope DPAPI, run ring 100×4MiB,
  recovery interrupted, import bundle).
- `desktop/controllers/ssh.go`, `deployments.go` (event `deployment:<kind>:<event>:<id>`,
  redactor secret, coordinator gate).

**Frontend**
- Halaman **SSH** (kartu host + tag + recent + modal host/keygen/import + test timeline).
- Keluarga **SFTP** (SFTPModal dual-pane 758 brs + FileEditor + ExternalEditSession).
- Halaman **Deployments** (mode lokal default; detect/apply/deploy/destroy timeline langkah;
  ApplyConfigModal Monaco + presets + preview script; run history; import JSON).
- Integrasi terminal: `StartPtyForSSH` (pane SSH + timeline koneksi).

**AC**
1. Generate/import kunci; host password tersimpan DPAPI (verifikasi blob di config).
2. Koneksi pertama host → TOFU prompt; kunci berubah → koneksi ditolak dengan pesan.
3. Terminal SSH di pane in-app (PTY remote `xterm-256color`) + resize WindowChange.
4. SFTP: browse, mkdir, rename, delete, upload/download dengan progress + cancel; edit file
   remote via temp + watch mtime → reupload.
5. Deploy preset `laravel-production` (id tanpa prefix `builtin:`, lihat fase-5.md §5.7) ke VPS
   uji (sudo -n): detection → plan streaming → step status; gagal di tengah → histori
   `interrupted`/`error` + log terenkripsi terbaca. *(Verifikasi tanpa VPS nyata: engine via
   fake-runner test + transport via server SSH in-process — fase-5.md §6.)*
6. Dua mutasi deploy bersamaan → satu ditolak `ErrBusy`.
7. ProxyJump 2 lapis diuji dengan server in-process (bastion direct-tcpip nyata); ProxyCommand
   diuji pada substitusi %h/%p + adaptor net.Conn (e2e helper eksternal menyusul bila perlu).
8. Import bundle JSON: idempoten, konflik host dipetakan, tak ada eksekusi skrip.

**Dep**: Fase 3 (PTY), Fase 4 (Monaco untuk editor config), Fase 2 (presets memakai resolusi project opsional).

---

## Fase 6 — Agents, MCP, Command Center, Cloud & Pelengkap

**Tujuan**: paritas fitur penuh LaraEnv.

**Backend**
- `internal/agents` (paket terbesar): katalog 4 provider + versi probe; invocation + session-id
  resume; Manager launch/resume/adopsi hook; hooks config per provider (settings.json/hooks.json/
  config.toml/plugin.js) + lifecycle transient (`--agent-event`) + fence activity-run; signal
  spool + drain 250ms; session discovery per provider (jsonl / app-server JSON-RPC / index /
  `session list`); activity subsystem (store tree 500 node, router multi-window, owner HTTP
  loopback token, coordinator batch, protokol Codex 0.160 thread/list); usage (Anthropic/Codex
  OAuth API).
- `internal/mcpbridge` (go-sdk resmi; 8 tool `kitodock_*`, loopback + bearer + origin + cap +
  audit; stdio proxy) + `internal/mcpclient` (registrasi Claude/Codex/Cursor + backup).
- `internal/applicationcontext` (provider Laravel embedded + capture + logs + mail Mailpit).
- `internal/cloud` (opsional, bisa dijadwal ulang): session discipline, E2EE Argon2id+AES-GCM,
  sync resource, rekey, engine_compat, billing — **hanya jika backend cloud akan dipakai**;
  default Kitodock: lokal penuh tanpa akun (posisi LaraEnv saat ini).
  *(Realisasi: DIJADWALKAN ULANG — tidak diimplementasi di fase 6, produk lokal penuh;
  lihat fase-6.md §6.1.)*
- `internal/updater` (GitHub releases, sidecar sha256, cmd update MSI/portable).
- `internal/notifications` penuh (diff sesi + threshold usage + toast + prefs).
- `internal/webviewruntime` (recovery WebView2).
- Controller `agents.go`, `mcp.go`, `cloud.go`, `commandcenter.go`; command-bar window +
  hotkey global (`internal/hotkey`); `cmd/deploy-tui` (port bubbletea).
  *(Realisasi: agents/mcp/commandcenter/updates controller ada; palette Ctrl+K in-app dulu —
  jendela terpisah + hotkey global menyusul; deploy-tui direalisasi sebagai CLI flags
  non-interaktif `cmd/kitodock-deploy` berbagi engine+preset yang sama; cloud.go tidak ada
  karena cloud dijadwalkan ulang — fase-6.md §6.)*

**Frontend**
- `components/agents/` penuh + halaman Agents; AgentLauncherMenu; usage bar; auto-switch.
- Command center: palette Ctrl+K + builtins + launcher (`<project> <provider>`, autocomplete
  artisan/npm, `ssh:<host>`) + CommandBarWindow proses terpisah + Settings rebind/hotkey.
- Settings: tab Experimental (MCP + agent toggles), Updates; Account (bila cloud aktif).
- i18n: katalog id+en komplet; pt-BR/ru/zh-CN menyusul komunitas.

**AC**
1. Launch claude/codex di worktree project dari AgentLauncherMenu; sesi muncul di navigator;
   resume setelah restart; stop; rename; archive; unread badge.
2. Hook terpasang di config provider (4 provider) dan bersih dilepas; subagent-start/turn-stop
   terlihat di AgentMapDialog; aktivitas lintas jendela (detached) konsisten via owner server.
3. MCP: aktifkan di Settings → registrasi Claude/Codex/Cursor (backup dibuat) → dari Claude
   Code bisa `kitodock_project list`, `kitodock_service start nginx`, `kitodock_exec artisan
  migrate` (confirm), `kitodock_db list`; audit tercatat tanpa argumen.
4. `kitodock_context` + integrasi Laravel 2-file (confirm) → capture JSONL terbaca; mail
   ter-tag project muncul di mail_list.
5. Command palette: Ctrl+K global (hotkey) memunculkan command-bar proses terpisah; intent
   dieksekusi di jendela utama (buka project/terminal/service/navigasi).
6. Updater: cek rilis GitHub, unduh + verifikasi sha256, apply MSI (relaunch) / portable swap.
7. Notifications: sesi agent butuh input → toast (rate-limit) + inbox; 80/95% usage.
8. deploy CLI (`kitodock-deploy`) build windows (linux menyusul di CI non-windows) dan bisa
   provision VPS dengan resep sama. *(TUI interaktif bubbletea menyusul di atas CLI yang sama.)*

**Dep**: Fase 3–5 (PTY untuk sesi, routing worktree dari Fase 2, SFTP/deploy untuk TUI).

---

## Peta Dependensi Antar Fase

```
0 → 1 → 2 → 3 → 4 → 5 → 6
        └──────→ 4 (butuh project ctx)
1 → 3? TIDAK — terminal hanya butuh 0+2 (resolver runtime)
5 butuh 3 (PTY SSH) & 4 (Monaco)
6 butuh 2 (routes/worktree), 3 (PTY), 4 (files untuk agents navigator), 5 (deploy TUI)
```

## Definisi "Paritas" (closure proyek)
Kitodock dinyatakan **paritas fungsional** bila seluruh AC Fase 0–6 hijau, dengan dua pengecualian
yang boleh ditunda dan dicatat: (a) `internal/cloud` + halaman Account (bila memutuskan tanpa
cloud), (b) katalog bahasa pt-BR/ru/zh-CN.
