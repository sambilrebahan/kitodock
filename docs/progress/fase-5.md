# Progress — Fase 5: SSH, SFTP & Deployment

> Status: **SELESAI** (2026-11-09) · Implementasi mandiri; audit §4.

## 1. Paket baru

| Paket | Isi | Test |
| --- | --- | --- |
| `internal/ssh` | Key generation (ed25519 OpenSSH-format via `ssh.MarshalPrivateKey`, RSA PKCS#1, ECDSA SEC1) + SaveKey (chmod 0600 eksplisit) + ReadPublicKey + ListKeys (validasi parse: hanya file yang benar-benar kunci); HostStore (hosts.json, upsert/find/remove, atomic temp+rename); KnownHosts TOFU (alamat → base64 key; koneksi pertama merekam, perubahan kunci ditolak keras dengan pesan MITM; Trust/Forget/Fingerprints SHA256); Connect (password DPAPI-unseal atau key file, host-key callback injectable, timeout 10 dtk); Session: RequestPty (modes ECHO+ISPEED/OSPEED), Write/Read/Resize (WindowChange), Run (combined output, sudo opsional via `sudo -n bash -lc`); SFTPSession: List/Mkdir/Remove/Rename/Download/Upload/ReadFile/WriteFile | **15** — termasuk **server SSH in-process** (ed25519 host key, password auth, exec echo, shell echo, subsystem SFTP dengan `sftp.WithServerWorkingDirectory` di-root temp dir) sehingga handshake, auth salah, TOFU, PTY echo, dan SFTP round-trip teruji nyata tanpa jaringan |
| `internal/deploy` | Detection ~24 probe (OS, nginx/php/git/mysql/composer/certbot, PHP major.minor, isFresh); BuildPlan (langkah provisioning bash: paket, git clone dengan PAT di-splice ke URL HTTPS, vhost nginx + try_files, php-fpm, DB, certbot opsional); RunApply executor sekuensial dengan callback StepStatus (running/ok/error/ignored) + OnLine streaming (buffer scanner 1 MiB/baris), stop-on-fail kecuali Ignore, output gabungan; preset engine: `{lower_snake}` token → nilai dari ApplyConfig (10 builtin var) + user var (tidak bisa men-shadow builtin), ResolveDeployScript meninggalkan token tak dikenal terlihat | 12 |
| `internal/deploycoord` | Single-flight Coordinator (Acquire → release func; ErrBusy saat ada run lain) | 1 |
| `internal/deployments` | Store index JSON (versi 1): Deployment {ID, Name, Config sealed, Runs ring 100}; RunSummary {kind detect/apply/deploy/destroy, status running/success/error/interrupted — run “running” saat crash ditandai interrupted saat StartRun berikutnya}; run log per-run disegel DPAPI terpisah (cap 4 MiB), atomic persist | 5 |

`deploy.Runner` (`Run(command string, sudo bool)`) cocok persis dengan `ssh.Session.Run` — controller cukup adapter tipis, tanpa dependensi antar-paket internal.

## 2. Wiring desktop

- **paths**: `SSHDir()` (keys/, hosts.json, known_hosts.json) dan `DeploymentsDir()` (state/deployments: index.json + runs/).
- **SSH controller** (`desktop/controllers/ssh.go`): registry 3 jenis session (conns c*/shells s*/sftp f*, counter atomic, mutex); host CRUD dengan password disegel DPAPI (password kosong = pertahankan seal lama; ListHosts mengosongkan field password); generate/list/read key; ConnectSSH memakai TOFU callback dari known_hosts; OpenSSHShell (PTY + read pump → event `ssh:data`/`ssh:exit`, shell memiliki koneksinya sendiri — menutup shell menutup koneksi); Write/Resize/Close shell; SFTP open/close/list/mkdir/remove/rename/readFile/writeFile/upload/download; `CloseAllSSH` dipanggil dari lifecycle shutdown.
- **Deployments controller**: DeploymentView (config di-unseal untuk UI, dokumen `{hostName, presetId, presetVars, config}` disegel ke dalam record); SaveDeployment preserve CreatedAt+Runs; RunDetect sinkron (record kind detect, return DetectResult{detection, runId}); **StartApply async** — Acquire coordinator, goroutine menjalankan RunApply dengan event streaming, FinishRun + SaveRunLog + `deploy:done`; GetRunLog unseal; DeployPlanPreview (langkah untuk pratinjau UI tanpa menyentuh server); preset builtin `git-pull-update` (fetch+reset, composer, migrate, cache) menggantikan plan otomatis saat dipilih.
- **events**: `ssh:data`, `ssh:exit`, `deploy:step`, `deploy:line`, `deploy:done`.
- app.go/main.go: SSH + Deployments dibuat, di-bind; `pkg/sftp` naik jadi dependensi langsung (`go mod tidy`).

## 3. Frontend

- **Ssh.tsx** (menggantikan placeholder): tab **Hosts | Keys**.
  - Hosts: kartu host (user@addr:port, badge auth), form nama/alamat/port/user/auth; password field placeholder “biarkan kosong untuk memakai sandi tersimpan” saat edit; tombol per host → **buka shell** / **jelajahi berkas** / edit / hapus.
  - RemoteTerminal: xterm.js terikat shellId; output via `ssh:data`, keystroke → WriteSSHShell, ResizeObserver → ResizeSSHShell.
  - SftpBrowser: path bar editable (Enter → list), parent dir, mkdir/rename/remove (dialog bahaya), upload (OpenFileDialog native) & download (SaveFileDialog native) streaming, direktori dulu lalu file, ukuran human-readable.
  - Keys: generate (ed25519/rsa/ecdsa, bit RSA), daftar kunci, public key + tombol copy.
- **Deployments.tsx**: kartu deployment dengan chip riwayat run (klik → log ter-unseal); deteksi server; **deploy now** → panel timeline live (status per langkah: spinner/centang/silang, stream baris output 400 terakhir); form editor dengan pratinjau rencana (`DeployPlanPreview`, jumlah langkah) dan pilihan strategi (plan otomatis vs preset); tombol new dinonaktifkan + tooltip bila belum ada host SSH.
- Bindings stubs SSH.js/.d.ts + Deployments.js/.d.ts; models.ts: namespace `ssh`, `deploy`, `deployments` + `controllers.DeploymentView/DetectResult`; runtime stub ditambah dialog API resmi Wails v2 (Open/Save/MessageDialog + opsi).
- i18n: seksi `ssh` (36 kunci) + `deployments` (29 kunci) untuk en & id.

## 4. Verifikasi & audit

| Pemeriksaan | Hasil |
| --- | --- |
| `go test` (fresh, -count=1) | ✅ **32 paket hijau** (ssh 15, deploy 12, deployments 5, deploycoord 1, + lainnya) |
| `go vet -unsafeptr=false` (setara CI) | ✅ |
| gofmt | ✅ |
| Frontend build + vitest + lint | ✅ 12/12 test, 0 error lint |
| `go build ./desktop` (frontend dist ter-embed) | ✅ 36,6 MB |
| **Audit originalitas** | **0 komentar prosa identik** (satu-satunya hit `//go:embed` = direktif compiler wajib); **0 ekspresi/logika identik** — 5 kejadian ekspresi level (shell-quoting satu baris ×2, pesan ErrBusy, komentar AuthMode, ID fixture test, nama var regex) **ditulis ulang sebelum commit**; sisa 41 baris >30 karakter semuanya tak terhindarkan: struct tag JSON, nama method library `x/crypto/ssh`, direktif nginx literal, idiom stdlib (`sort.Slice`, `time.After`), import path |

## 5. Keputusan & catatan

1. **Server SSH in-process untuk testing** — `connection_test.go` menjalankan ssh.Server sungguhan (host key ed25519 per-run, password callback, exec echo, echo shell dengan `io.Copy(channel, channel)`, subsystem SFTP via `sftp.NewServer` + `WithServerWorkingDirectory(temp)`) — uji end-to-end tanpa Docker/jaringan eksternal, deterministik, dan berjalan di CI manapun.
2. **TOFU, bukan trust-all**: callback default Connect adalah insecure (hanya untuk test); controller produksi selalu memasang `KnownHosts.Callback()`. Kunci berubah → error eksplisit dengan instruksi Forget manual (anti-MITM).
3. **Password host: seal di controller, bukan di paket ssh** — `ssh.HostStore` hanya melihat ciphertext; kunci DPAPI via `credentials.System` (prefix kddp1). Sama untuk config deployment (dokumen `{hostName, presetId, presetVars, config}` disegel utuh ke `Deployment.Config`).
4. **Shell memiliki koneksinya** — `OpenSSHShell` membuat koneksi sendiri; menutup shell menutup koneksi (map shellConns). Koneksi untuk SFTP/command adalah entri terpisah; `CloseAllSSH` di shutdown lifecycle merapikan semuanya.
5. **Preset menggantikan plan, bukan menambah** — preset = kontrol penuh pemilik server (script user); tanpa preset = BuildPlan otomatis. Builtin `git-pull-update` untuk jalur cepat server yang sudah ter-provision; `DeployPlanPreview` menampilkan akibat pilihan sebelum menyentuh server.
6. **Multi-return Wails**: `RunDetect` dibungkus struct `DetectResult{detection, runId}` — Wails v2 bersih menangani `(value, error)` saja.
7. **Quoting POSIX dipisah dua implementasi** — `ssh.quoteSingle` (builder byte) dan `deploy.shellQuote` (NewReplacer) sama-sama menghasilkan `'\''`; perbaiki juga bug draft awal (replacer 3 karakter membuat quote tak tertutup) sebelum teruji.
8. **ListKeys memvalidasi parse** — hanya file yang berhasil `ReadPublicKey` dianggap kunci: readme/stray file tidak pernah muncul di dropdown.
9. **chmod 0600 eksplisit setelah WriteFile** — parameter perm hanya berlaku saat file baru; overwrite file eksisting dengan perm lebar kini ikut dikencangkan (di Windows perm POSIX memang advisory — test men-skip asersi perm di Windows).

## 6. Utang yang tersisa (non-blocker)

- Proxy jump / bastion host (LaraEnv punya; dirancang di docs/03, ditunda sampai ada permintaan nyata).
- Editor file remote via SFTPReadFile/WriteFile sudah tersedia di API, UI-nya belum (cukup browser sekarang).
- Auto-deploy cron (`ApplyConfig.AutoDeployCron`) tersimpan tetapi belum dihubungkan ke scheduler cron Fase 3.
- Upload/download progress event (sekarang blocking promise; ukuran file kecil-menengah).
