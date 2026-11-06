# 02 — Arsitektur Target Kitodock

> Prinsip: **pertahankan apa yang terbukti di LaraEnv, perbaiki apa yang menyakitkan.**
> Penyakit utama LaraEnv: `app.go` 6.230 baris (god file) + struct `App` 50 field yang
> mencampur 15+ domain. Kitodock memecahnya menjadi controller per domain dengan
> composition root yang tipis.

---

## 1. Prinsip Arsitektur

1. **Satu binari, banyak mode** (sama seperti LaraEnv): GUI, `mcp`, hook CLI, `--cron-run`,
   `--terminal-window`, `--command-bar`. Mode non-GUI tidak boleh menginisialisasi manager berat.
2. **`internal/*` murni, tanpa dependensi Wails.** Semua event keluar lewat callback emitter yang
   disuntikkan (pola LaraEnv yang sudah benar — dipertahankan).
3. **Composition root tipis** (`desktop/app.go` target ≤ 600 baris): hanya membuat config,
   emitter bus, dan controller; tidak berisi logika domain.
4. **Controller per domain** (`desktop/controllers/`): satu struct per domain, method-methodnya
   yang di-bind ke Wails. Controller boleh saling memanggil **hanya lewat interface kecil** yang
   didefinisikan di paket penerima (seam), bukan lewat struct `App` global.
5. **Layering `internal/` dijaga bebas siklus** (lihat §4). Aturan: paket hanya boleh mengimpor
   paket di layer yang lebih rendah.
6. **Windows-specific di belakang build tag / file `_windows.go` + `_other.go`** (pola LaraEnv).
7. **Semua penulisan state = atomic + preserve-permission** (atomicfile), semua spawn =
   `CREATE_NO_WINDOW`, semua kill = kill-tree.
8. **Frontend keep-alive tanpa router** (pola LaraEnv) + external store untuk subsistem berat
   (terminal, dokumen, source control, agents) — terbukti scale di sana.

## 2. Struktur Repo Target

```
kitodock/
├── PLAN.md, README.md, LICENSE, NOTICE, THIRD_PARTY_NOTICES.txt
├── go.mod                       # module github.com/<user>/kitodock
├── cmd/
│   ├── kitodock/                # (opsional di akhir) CLI pembantu; entry utama tetap desktop/
│   └── deploy-tui/              # TUI deployment standalone (bubbletea) — Fase 6
├── desktop/                     # Wails app
│   ├── main.go                  # dispatch mode proses (≤ 200 baris)
│   ├── app.go                   # composition root TIPIS (≤ 600 baris)
│   ├── events.go                # EventBus: wrapper runtime.EventsEmit + log + konstanta nama event
│   ├── lifecycle.go             # startup/shutdown/beforeClose per mode
│   ├── winshow.go, elevation_*.go, ca_prompt*.go
│   ├── controllers/             # SATU controller per domain (method = Wails API)
│   │   ├── services.go          # ServicesCtrl  (~25 method)
│   │   ├── installer.go         # InstallerCtrl (downloads, bootstrap, sources)
│   │   ├── runtimes.go          # RuntimesCtrl (php/node/python versions, ext, ini, fpm)
│   │   ├── projects.go          # ProjectsCtrl (CRUD, create, tags, tld, commands)
│   │   ├── vhostssl.go          # VhostCtrl (vhost editor, SSL, CA, routes/worktree)
│   │   ├── devserver.go         # DevCtrl (dev server, queue worker, cron)
│   │   ├── doctor.go            # DoctorCtrl (diagnose + fixes + cancel)
│   │   ├── terminal.go          # TerminalCtrl (pty lifecycle, workspace persist, clipboard)
│   │   ├── files.go             # FilesCtrl (workspace files, document workspace, shutdown gate)
│   │   ├── git.go               # GitCtrl (source control panel)
│   │   ├── ssh.go               # SSHCtrl (hosts, keys, test, sftp)
│   │   ├── deployments.go       # DeployCtrl (lokal + presets)
│   │   ├── agents.go            # AgentsCtrl (sessions, hooks, usage, activity)
│   │   ├── mcp.go               # MCPCtrl (status, register, enable)
│   │   ├── cloud.go             # CloudCtrl (auth, sync, billing) — opsional
│   │   ├── commandcenter.go     # CommandCtrl (config CC, intent inbox)
│   │   ├── system.go            # SystemCtrl (ports, metrics, update, url, i18n, notifications)
│   │   └── dto.go               # DTO bersama lintas controller (sedikit; DTO domain di controller masing-masing)
│   ├── frontend/                # React (lihat §6)
│   ├── langs/                   # katalog i18n (id, en, + pt-BR/ru/zh-CN menyusul)
│   └── build/windows/           # icon, manifest, installer (wix/nsis), portable marker, pgvector script
├── internal/                    # 45 paket — layout & layering sama dengan LaraEnv (lihat §4)
├── tools/                       # ci, langs, licenses, screenshots (menyusul kebutuhan)
└── docs/                        # planning ini + progress + panduan dev
```

Perbedaan penting vs LaraEnv: folder `desktop/controllers/` baru; `app.go` hanya wiring;
`events.go` memusatkan nama event sebagai konstanta (di LaraEnv nama event tersebar string di
60 lokasi).

## 3. Lapisan Desktop: Cara Kerja Baru

### 3.1 Wiring (pola)
```go
// desktop/app.go (sketsa)
func NewApp() (*App, error) {
    cfg := config.NewStore(paths.ConfigFile())
    bus := NewEventBus()                    // emit ke Wails runtime + log debug
    ctrls := &Controllers{}
    svc := services.NewManager(cfg, ...)
    ctrls.Services = controllers.NewServices(bus, cfg, svc, ...)
    // ... setiap controller menerima DEPENDENSI yang ia butuhkan, bukan App global
    return &App{cfg: cfg, bus: bus, ctrls: ctrls}, nil
}
// desktop/main.go
wails.Run(&options.App{Bind: []interface{}{
    app, // hanya lifecycle/window methods
    app.Ctrls.Services, app.Ctrls.Installer, /* ... semua controller */ }})
```
> Wails v2 mendukung multi-struct bind. Semua method publik semua controller = API frontend
> (menggantikan 274 method App LaraEnv; target surface setara, terbagi ±19 controller).

### 3.2 EventBus
- `Emit(name string, data any)` → `runtime.EventsEmit` + trace log (mudah debug).
- Konstanta: `EvServiceStatus = "service:status"`, dst. (nama event **identik** dengan LaraEnv
  agar pola frontend bisa diangkat hampir 1:1 — lihat 01 §B.4).

### 3.3 Injection seams yang dipertahankan dari LaraEnv
`mcpbridge.Backend` · `sitedoctor.Dependencies`/`DBExecutor` · `deployments.Protector` ·
resolver callback di `devserver`/`cron`/`queueworker`/`agents` (WorkspaceResolver,
PathResolver, RuntimePathResolver) · `pty.Session` structural-compatible dengan `ssh.Session`.
Semua controller berkomunikasi antar-domain lewat seam ini, bukan import silang controller.

## 4. Layering `internal/` (aturan dependensi, bebas siklus)

```
L0 daun:      paths · atomicfile · i18n · credentials · localtls · runtimeinventory ·
              netports · vcruntime · clipboard · documentshutdown · deploycoord · version ·
              sysmetrics · tray · hotkey · pty · terminal · editor · mcpbridge ·
              webviewruntime · applicationcontext
L1:           config · nodejs · php · projectroutes · devserver · cron · queueworker ·
              gitworkspace · relocate · updater · mcpclient
L2:           services · vhost · installer · workspacefiles · ssh · agents
L3:           projects · sourcecontrol · deploy
L4:           cloud (opsional) · sitedoctor · notifications · deployments
komposisi:    desktop/controllers (satu-satunya tempat yang boleh mengimpor lintas domain luas)
```
Aturan praktis: PR yang menambah import dari Ln ke Ln'≥n **ditolak** kecuali disetujui eksplisit
dan dicatat di `docs/progress/`. (LaraEnv membuktikan layering ini layak — 45 paket tanpa siklus.)

## 5. Rename Map Lengkap (LaraEnv → Kitodock)

| Aspek | LaraEnv | Kitodock |
| --- | --- | --- |
| Produk / exe | LaraEnv / `LaraEnv.exe` | Kitodock / `Kitodock.exe` |
| Module Go | `lara4ever` | `kitodock` (repo `github.com/<user>/kitodock`) |
| Root env default | `C:\laraenv` (legacy `C:\lara4ever` dimigrasi) | `C:\kitodock` (migrasi legacy `C:\laraenv`/`C:\lara4ever` **tidak otomatis** — ini produk baru, bukan upgrade) |
| Env var root | `LARAENV_ROOT` | `KITODOCK_ROOT` |
| Marker portable | `.laraenv-portable` | `.kitodock-portable` |
| Marker hosts | `# lara4ever` | `# kitodock` |
| Local CA | CN `LaraEnv Local CA (<root>)`, file `lara4ever-rootCA.crt/.key` | CN `Kitodock Local CA (<root>)`, `kitodock-rootCA.crt/.key` |
| Env terminal/agent | `LARAENV_TERMINAL_ID`, `LARAENV_AGENT_OWNER`, `LARAENV_AGENT_SESSION`, `LARAENV_AGENT_RUN` | `KITODOCK_TERMINAL_ID`, `KITODOCK_AGENT_OWNER`, `KITODOCK_AGENT_SESSION`, `KITODOCK_AGENT_RUN` |
| Lock single-instance | `laraenv-main-single-instance` | `kitodock-main-single-instance` |
| TEMP dirs | `%TEMP%\laraenv-cache|-cmdbar|-attach`, prefix `.laraenv-state-*.tmp`/`.laraenv-document-*.tmp` | `%TEMP%\kitodock-cache|-cmdbar|-attach`, `.kitodock-state-*.tmp`/`.kitodock-document-*.tmp` |
| Cache installer | `.lara4ever-cache\<kind>-<version>.zip` | `.kitodock-cache\...` |
| Marker versi install | `.lara4ever-version` | `.kitodock-version` |
| Task Scheduler | `\LaraEnv\<jobID>` | `\Kitodock\<jobID>` |
| MCP tool prefix | `laraenv_project`, `laraenv_service`, ... (8 tool) | `kitodock_project`, `kitodock_service`, `kitodock_worker`, `kitodock_exec`, `kitodock_logs`, `kitodock_db`, `kitodock_diag`, `kitodock_context` |
| Provider Laravel ctx | `LaraEnvContextServiceProvider`, `config/laraenv-context.php`, `X-LaraEnv-Project`, `storage/laraenv/*.jsonl`, `.ai/guidelines/laraenv.md` | `KitodockContextServiceProvider`, `config/kitodock-context.php`, `X-Kitodock-Project`, `storage/kitodock/*.jsonl`, `.ai/guidelines/kitodock.md` |
| Deploy remote | `.laraenv-bin` shim, marker log `[laraenv-autopull]` | `.kitodock-bin`, `[kitodock-autopull]` |
| Upstream block nginx | `# >>> lara4ever php upstreams` | `# >>> kitodock php upstreams` |
| Tuning block apache | `# laraenv: tuning` | `# kitodock: tuning` |
| Plugin opencode / hooks | `laraenv.js`, `--agent-hook`, marker `// laraenv:managed`, `# laraenv:begin/end` | `kitodock.js`, `--agent-hook`, `// kitodock:managed`, `# kitodock:begin/end` |
| known_hosts / keys | `<root>\ssh\known_hosts`, `<root>\ssh\hosts\` | sama (struktur), prefix file internal berubah |
| Bahasa | en, pt-BR, ru, zh-CN | **id** (prioritas utama penulis) + en; pt-BR/ru/zh-CN menyusul |
| Subdomain TLD default | `.test` | `.test` (tetap) |

> **Penting**: karena prefix marker/semua nama berubah, Kitodock **tidak kompatibel data**
> dengan instalasi LaraEnv (tidak dimaksudkan migrasi in-place). Keputusan ini disengaja untuk
> kebersihan; opsi kompatibilitas dibahas di `05-risiko-dan-licensing.md` §5.

## 6. Frontend Target

### 6.1 Kerangka (pertahankan dari LaraEnv)
React 18 + TS strict + Vite 6 + Tailwind v4 (config v3-kompat) · tanpa router (keep-alive) ·
Context + useReducer + external store · 6 tema `data-theme` · i18n katalog JSON + plural CLDR +
override disk · Vitest + Testing Library + jsdom · Monaco divendorkan (`/vs`) · xterm 5 + fit +
webgl · react-resizable-panels · lucide-react · react-markdown+GFM.

### 6.2 Struktur `desktop/frontend/src`
```
main.tsx · App.tsx (3 kepribadian jendela) · style.css · theme.ts · terminalPrefs.ts
commandCenter/   (types, builtins, launcher, hotkeys, useCommandCenter, CommandPalette, Settings)
components/
  agents/        activityApi, activityModel, useAgentActivity, useAgentMap, useAgentAutoSwitch,
                 AgentMapDialog, AgentLauncherMenu, AgentUsageBar, sessionTree, filters, onboarding
  documents/     api, useDocumentWorkspace, documentOperations, DocumentWorkspace, DocumentEditor,
                 ConflictDiff, MarkdownPreview, WorkspaceFilesPanel, DocumentCloseDialog, useDocumentShutdown
  sourcecontrol/ api, useSourceControl, SourceControlPanel, GitChangesTree, GitReviewEditor,
                 useGitReviews, useStageDocuments, StageDirtyDialog
  terminal/      Terminal, TerminalWorkspace, TerminalGroup, PaneRenderer, workspaceReducer,
                 types, serialize, snap, dnd, hotkeys, keys, clipboard, SSHConnectingTimeline
  workspace/     WorkspaceNavigator, navigationPreferences, types
  dialogs/       DialogProvider, PortalMenu, confirm/alert/prompt
  (root)         Sidebar, WindowControls, ErrorBoundary, GlassCard, NotificationBell/Inbox,
                 SystemMetrics, ServiceCard/Icon, ServiceLogsModal, PortsModal, modal project/ssh/sftp/deploy
pages/           Dashboard, Projects, SSH, Cron, Deployments, Downloads, Settings,
                 Account, Updates, Agents
lib/             i18n.tsx, monaco.ts, fileLanguage, downloadsFilter, installKinds, tags, api helpers
```
(Sama dengan LaraEnv — komponen & halaman diangkat lalu disesuaikan nama brand/i18n; lihat 04.)

### 6.3 Konvensi frontend yang dipertahankan
- Semua event listener: `EventsOn` per-listener dengan canceler; **jangan pernah** `EventsOff(nama)` global.
- Request object dengan seq-token untuk paksa re-render effect.
- `inert` pada shell saat dialog keputusan terbuka; focus-trap + restore di semua modal (portal).
- String UI wajib lewat `useT` (meta-test anti bare-string dipertahankan).
- i18n key literal string (bukan interpolasi) agar linter katalog bekerja.

## 7. Konvensi Backend Kitodock (baru/perbaikan)

1. **Error**: paket `internal/i18n` error ter-lokalisisasi (pola LaraEnv `*i18n.Error`) — dipakai
   konsisten; jangan `fmt.Errorf` untuk pesan sampai user.
2. **Log**: `slog` terstruktur + level; file log rotasi ringan di `<root>\logs\app-YYYYMMDD.log`
   (LaraEnv minim logging — perbaikan eksplisit).
3. **DTO**: DTO Wails didefinisikan di controller pemilik domain (bukan 1 file global); field
   `json` snake_case.
4. **Konkurensi**: mutex per domain di controller (doctor, runtime, deploy, mcp gate) — pola
   LaraEnv dipertahankan, tapi didokumentasikan per controller.
5. **Test**: setiap paket internal unit-test; controller punya test untuk fungsi bebas yang
   diextract (pola LaraEnv `app_runtime.go`/`app_routes.go` yang sukses).
6. **Build tags**: `windows` untuk syscall; `bindings` untuk stub generasi binding (pola LaraEnv).
7. **Versi**: `internal/version` + ldflags `-X ...version.Version=<ver>` (sama).

## 8. Mode Proses Non-GUI (target perilaku sama)

| Mode | Trigger | Perilaku |
| --- | --- | --- |
| `mcp [--connection <path>]` | argv[1] | proxy stdio→HTTP loopback; tanpa GUI/manager |
| `--agent-activity <provider>` | argv | tulis file signal aktivitas ke spool; exit |
| `--agent-hook <provider> [args]` | argv | hook lifecycle provider → signal; exit |
| `--agent-event <id> <source>` | argv | claude-stop / claude-notification / codex-notify → signal; exit |
| `--cron-run <jobID>` | argv | baca config, cek jadwal menit ini, eksekusi; exit |
| `--terminal-window <seed>` | argv | jendela terminal satelit (attach kembali via inbox) |
| `--command-bar` | argv | palette global proses terpisah (inbox intent) |
| GUI (default) | — | relaunch elevasi bila perlu → window utama + tray |

## 9. Keamanan (pola wajib turun-temurun dari LaraEnv)
- Authority path: `os.Root` + open tanpa reparse + identitas file (NTFS file ID) untuk semua akses
  file yang dipicu frontend (workspacefiles, sourcecontrol, document workspace).
- DPAPI untuk semua secret at-rest (password SSH, payload deployment, descriptor MCP).
- known_hosts TOFU + tolak kunci berubah + lock antar-proses.
- Redaksi secret di semua output yang sampai UI/log (URL userinfo, PAT, password).
- MCP: loopback + bearer 32B + origin check + allow-list exec + audit tanpa argumen.
- Kill dengan allow-list nama exe (netports) — jangan pernah bunuh proses di luar milik kita.
- Installer: zip-slip guard + tolak path legal reserved + validasi magic zip + UA/Referer per situs.
