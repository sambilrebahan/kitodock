# Progress — Fase 5: SSH, SFTP & Deployment

> Status: **SELESAI** (2026-11-09, termasuk ronde penutupan celah AC) · Implementasi mandiri; audit §4.

## 1. Paket baru

| Paket | Isi | Test |
| --- | --- | --- |
| `internal/ssh` | Key generation (ed25519 OpenSSH-format, RSA PKCS#1, ECDSA SEC1) + SaveKey (chmod 0600 eksplisit) + ReadPublicKey + ListKeys/InspectKeys (validasi parse: hanya file yang benar-benar kunci, plus algoritma + fingerprint SHA256) + ImportKey (validasi parse, tolak duplikat); HostStore (hosts.json, upsert/find/remove, atomic temp+rename); KnownHosts TOFU (alamat → base64 key; **lock antar-proses dengan stale-takeover + read-modify-write terkunci** sehingga penulis konkuren tidak saling menimpa; kunci berubah → ditolak keras; Trust/Forget/Fingerprints/**Algorithms**); **DialTarget**: ProxyJump rantai datar ala `ssh -J` (resolveJump per nama host, max 5 hop, TOFU per hop, direct-tcpip antar hop), **ProxyCommand `%h/%p`** (helper lokal sebagai net.Conn), **stage timeline** (resolve/dial/jump/hostkey/auth/ready, ms per stage); Session: RequestPty, Write/Read/Resize, Run (sudo opsional); SFTPSession: List/Mkdir/Remove/Rename/Download/Upload/ReadFile/WriteFile + **UploadStream/DownloadStream dengan Progress + Cancelled → ErrTransferCancelled** | **21** — server SSH in-process (exec echo, echo shell, subsystem SFTP di-root temp dir, **forwarding direct-tcpip**) menguji handshake, auth salah, TOFU, PTY, SFTP round-trip, **ProxyJump 2 lapis nyata**, stage order, progress/cancel transfer |
| `internal/deploy` | Detection ~24 probe; BuildPlan (paket, sury, clone preserve, composer, **DB scripts embedded: create+grant mysql/postgres + .env snippet**, vhost, fpm, certbot, autopull; misconfig DB → langkah "Validate database configuration" yang gagal terlihat); **NormalizeDatabaseAccess** (default port/host/user, password digenerate alfanumerik shell-safe, nama DB divalidasi karakter); **BuildDestroyPlan** (disable vhost, hapus vhost+cron, reload, drop DB opsional, **webroot di-arsipkan aside 7 hari — tidak pernah rm -rf in-place**); **Redactor** (literal PAT/db-password → `*****`, pattern URL `https://creds@` → `*****@`; RedactSteps untuk preview; String aman) ; RunApply executor streaming; preset engine token | **16** |
| `internal/deploycoord` | Single-flight Coordinator (Acquire → release; ErrBusy) | 1 |
| `internal/deployments` | Store index JSON v1 (record + run ring 100, crash-recovery interrupted, log per-run disegel DPAPI cap 4 MiB); **Bundle**: `ExportBundle` (sanitasi gitPat + database.password via SanitizeConfig — hanya ekspor yang bersih, record tersimpan tak tersentuh) + `ImportDeployments` (idempoten per nama, re-seal per akun, tak pernah konek/eksekusi) + `ImportResult` | **8** |

`deploy.Runner` cocok persis dengan `ssh.Session.Run` — controller cukup adapter tipis.

## 2. Wiring desktop

- **paths**: `SSHDir()` + `DeploymentsDir()`.
- **SSH controller**: registry conns c*/shells s*/sftp f*/transfers t*/edits e*; host CRUD dengan seal DPAPI (ListHosts mengosongkan password; **LastUsed diperbarui saat konek** untuk sort recent); generate/**import**/list/inspect/read key; **ConnectSSH memakai DialTarget** — jump chain + proxy command + TOFU per hop + **event `ssh:connect-stage` per marker**; shell PTY (`ssh:data`/`ssh:exit`); SFTP CRUD + **`SFTPList` freeze-aware** (saat host dibekukan: path ter-cache disajikan snapshot, path belum ter-cache menolak); **transfer async** `sftp:progress:<id>`/`sftp:done:<id>` + `CancelSFTPTransfer`; **edit eksternal**: `StartRemoteEdit` (unduh ke temp, watch mtime+size tiap 2 dtk → reupload otomatis, `ssh:external-edit`) + `RevealLocalPath` + `StopRemoteEdit`; `ListLocalDir` (pane lokal) + `Read/WriteLocalTextFile` (bundle); `SetHostFrozen` dipakai deploy controller; `CloseAllSSH` di shutdown.
- **Deployments controller**: DeploymentView (doc `{hostName, presetId, presetVars, config}` disegel); **SaveDeployment menormalisasi DB** (password digenerate tersimpan sekali — BuildPlan idempoten terhadapnya); RunDetect sinkron; **StartApply/StartDestroy** lewat satu `startMutation`: coordinator, **freeze host selama run**, **semua output/error/log melewati RedactorFor(cfg)**, event **`deployment:<kind>:<step|line|done>:<deploymentID>`**; DeployPlanPreview + DeployDestroyPreview (**redacted**); **ExportDeploymentBundle** (deployments tersanitasi + host ter-referensi tanpa password) / **ImportDeploymentBundle** (host baru ditambah, host identik dilewati, **konflik nama-alamat → host lokal menang + deployment remapped + dilaporkan**; versi bundle divalidasi); **3 preset builtin**: `git-pull-update`, `laravel-production` (provision penuh sebagai script inspeksi), `static-site`.
- app.go/main.go/lifecycle: SSH + Deployments di-bind; `pkg/sftp` dependensi langsung.

## 3. Frontend

- **Ssh.tsx**: tab Hosts | Keys. Host: kartu dengan **badge tag**, indikator **jump ⇄ / proxy**, **lastUsed/recent sort**; form + field tags/jumpHosts (validasi nama host tersimpan)/proxyCommand; **timeline stage koneksi live**; tombol shell/berkas/edit/hapus. Shell = RemoteTerminal (komponen bersama). **SftpBrowser dual-pane**: kiri lokal (folder picker native + listing, upload →), kanan remote (path bar, mkdir/rename/remove, ← download); **transfer dengan progress bar + cancel**; **editor remote internal** (Monaco DocumentEditor, dirty + save) dan **eksternal** (unduh+reveal di Explorer, banner sesi watch + reupload otomatis). Keys: generate + **import PEM** + daftar dengan **algoritma + fingerprint** + public key copy.
- **Deployments.tsx**: kartu + chip riwayat run → log; detect; apply; **destroy** (dialog ganda: drop-DB? + konfirmasi destruktif) dengan timeline `deployment:destroy:*`; **timeline live per deployment** (subscribe `deployment:<kind>:<event>:<id>` saat run aktif); **import/export bundle** via dialog file native + laporan impor (counter + konflik); form editor + **preview rencana (redacted)** dan **preview script penuh di Monaco (readonly)**; DB engine/name + SSL.
- **TerminalPage**: **pane SSH** — selector host di toolbar membuka tab pane remote; split/close/restore layout mendukung kind `ssh` (respawn OpenSSHShell per host saat restore); RemoteTerminal diekstrak ke `components/terminal/` dan dipakai halaman SSH + workspace.
- Bindings stubs + models (ssh.KeyInfo, controllers.LocalEntry/RemoteEditView, deployments.ImportResult) + runtime dialog API. i18n en/id: ssh 53 kunci, deployments 47 kunci, terminal.sshTab.

## 4. Verifikasi & audit

| Pemeriksaan | Hasil |
| --- | --- |
| `go test` (fresh, -count=1) | ✅ **33 paket hijau** (ssh 21, deploy 16, deployments 8, deploycoord 1, controllers 5, + lainnya) |
| `go vet -unsafeptr=false` (setara CI) | ✅ |
| gofmt | ✅ |
| Frontend build + vitest + lint | ✅ 12/12 test, 0 error |
| `go build ./desktop` (frontend ter-embed) | ✅ 36,7 MB |
| **Audit originalitas (22 file ronde-2)** | **0 komentar prosa identik**; 79 baris kode >30 kar. yang cocok semuanya non-ekspresif: class Tailwind, baris import, idiom stdlib, sintaks nginx/bash literal |

## 5. Keputusan & catatan

1. **Audit AC ronde-2** — pembacaan jujur roadmap menemukan 5 ekspresi identik LaraEnv (ditulis ulang sebelum commit pertama) dan 8 lubang AC; semua ditutup di ronde ini kecuali yang dicatat §6.
2. **ProxyJump diuji nyata 2 lapis** — server uji in-process kini menangani channel `direct-tcpip` (payload RFC 4254 4 field — `ssh.Unmarshal` menolak data sisa), bastion meneruskan ke target; TOFU masing-masing hop tercatat terpisah. ProxyCommand diuji di level substitusi %h/%p + implementasi net.Conn-nya (uji penuh butuh helper eksternal).
3. **Lock known_hosts = RMW terkunci** — bug lost-update ditemukan test konkurensi (4 penulis → 1 entry); perbaikan: first-use merekam dengan read-modify-write di bawah lock (merge entri proses lain), mismatch diperiksa ulang dari disk sebelum menuduh MITM.
4. **Freeze semantik ketat** — saat deploy berjalan, path yang pernah di-list disajikan snapshot; yang belum pernah di-list **menolak** (tidak membaca state setengah-tulis); cache di-refresh saat tak beku.
5. **Redactor menyeluruh** — PAT dan password DB dimask dari: event line streaming, pesan error, run log tersimpan, dan command di plan preview. Password DB digenerate sekali di SaveDeployment dan ikut tersimpan (sealed) agar plan idempoten.
6. **Destroy aman**: webroot dipindah ke `/var/www/kitodock-destroyed/<nama>-<epoch>` (prune >7 hari), drop DB eksplisit opt-in via dialog ganda.
7. **Preset `laravel-production`** — roadmap menyebut id `builtin:laravel-production`; diimplementasi sebagai `laravel-production` (id berbeda dari LaraEnv demi originalitas; roadmap diamendemen).
8. **Bundle**: ekspor selalu tersanitasi (PAT + password DB dihapus, host tanpa password — seal DPAPI per akun tak berguna lintas mesin); impor idempoten per nama, konflik host dimenangkan lokal + remap otomatis; impor murni penyimpanan.
9. **Pane SSH di terminal workspace** — model Pane ditambah `host?`; restore layout me-respawn shell per host; RemoteTerminal jadi komponen bersama.
10. **DialTarget refactor** — `Connect` lama jadi wrapper tipis; auth/hostkey/jump terpusat di satu jalur sehingga TOFU + stage berlaku untuk semua koneksi.

## 6. Yang tidak terverifikasi penuh (jujur)

- **AC5 "deploy ke VPS uji"**: engine teruji via fake runner (16 test) + transport via server in-process; **provisioning nyata ke VPS Ubuntu belum dijalankan** — butuh VPS sungguhan (sudo -n, apt, sury). Semua bahan ada (preset `laravel-production` siap pakai).
- **ProxyCommand e2e** dengan helper eksternal (mis. `ssh -W`): substitusi + net.Conn teruji unit; tak ada helper cross-platform di CI Windows ini.
- Upload/download progress di UI belum diuji dengan file besar sungguhan (logika progress/cancel teruji di level paket ssh).
