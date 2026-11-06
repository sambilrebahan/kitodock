# Progress — Fase 0: Fondasi Proyek (v2 — implementasi mandiri)

> Status: **SELESAI** (2026-11-06, ditulis ulang dari nol setelah review)
> Versi pertama Fase 0 memakai strategi "PORT" (menyalin kode LaraEnv lalu
> me-rename). Pemilik proyek menolak pendekatan itu — LaraEnv hanya
> referensi. Seluruh hasil salinan **dihapus** dan Fase 0 dikerjakan ulang
> sebagai implementasi mandiri. Dokumen ini mencatat hasil akhir v2.

## 1. Apa yang dibangun (semua kode ditulis sendiri)

### Backend Go (module `kitodock`)
| Paket | Isi | Catatan desain |
| --- | --- | --- |
| `paths` | Resolusi root (`KITODOCK_ROOT` > marker `.kitodock-portable` > `C:\kitodock`) + `IsSameRoot`/`MovePath` untuk relokasi | Accessor tumbuh per fase; meta-test melarang hardcode root di luar paket |
| `atomicfile` | `Write` (swap atomik via MoveFileEx WRITE_THROUGH) + `WriteKeepingPermissions` (salin mode + DACL dengan PROTECTED_DACL agar akses tak melebar) | Mesin pin-direktori berlapis (os.Root/NtCreateFile ala LaraEnv) sengaja **belum** dibangun — menunggu editor dokumen Fase 4 |
| `i18n` | `Bundle`/`Text`/`Match`, global `Text`/`Fail`/`FailWrap`, `OSLocale` via registry | Format katalog = kontrak frontend (`$meta`, kunci bertingkat, objek plural CLDR); flag `autoTranslated` |
| `credentials` | DPAPI `Seal`/`Unseal`, envelope prefiks `kddp1:` | Interface `Sealer` untuk injeksi test |
| `clipboard` | user32 native, retry saat clipboard sibuk | Tanpa cgo |
| `notifications` | `Store` feed `Item/Kind/Scope/Delivery` + dedup fingerprint + recovery korup (sidecar) | Skema & penamaan orisinal kitodock |
| `config` | `Open`/`Get` (deep-copy)/`commit` revert-on-fail/`Watch` fsnotify debounce/rekonsiliasi relokasi | Schema Fase 0: InstallRoot, Language, Stack, Projects{TLD,Root} |
| `version` | ldflags target | — |

### Lapisan desktop
`main.go` (GUI saja; 5 mode proses lain menyusul per fase), `app.go`
composition root ±45 baris, `lifecycle.go`, `controllers/events.go`
(EventBus + konstanta event), `controllers/system.go` (9 API: bahasa,
versi, clipboard; watcher bahasa lintas proses sebagai **fungsi bebas**
`WatchLanguageFor` supaya tak ikut ter-bind Wails), `winshow.go` minimal
(restore+foreground), `elevation_windows.go` (belum dipanggil — Fase 1).

### Frontend
Vite+React+TS; **Tailwind v4 CSS-first tanpa tailwind.config.js** (token via
`@theme inline` + 6 tema `[data-theme]`); `lib/i18n.tsx` orisinal; komponen
DialogProvider/ErrorBoundary/WindowControls/Sidebar; 9 halaman keep-alive;
Settings fungsional (tema + bahasa id/en); katalog `langs/{en,id}.json`
orisinal. 12 test vitest (mesin translate + paritas katalog).

### Infra
LICENSE Apache-2.0, NOTICE (LaraEnv = referensi, bukan sumber kode), CI
(`wails build` dari module graph → assert binding → npm ci/test/lint →
`go test` → `go vet -unsafeptr=false` → guard diff), `tools/genicon`
(placeholder), `tools/tools.go` (pin CLI wails), stub `wailsjs/` tulisan
tangan (di-gitignore; bentuk `wailsjs/runtime/package.json` disalin polanya
dari generator wails v2.12 agar `../../wailsjs/runtime` resolve).

## 2. Verifikasi (semua dijalankan ulang setelah penulisan ulang)

| Pemeriksaan | Hasil |
| --- | --- |
| `go build ./...` + `gofmt` bersih | ✅ |
| `go test ./...` — 7 paket | ✅ (paths, atomicfile†, i18n‡, credentials, clipboard, config, notifications) |
| `go vet -unsafeptr=false ./...` | ✅ (pengecualian hanya untuk akses HGLOBAL clipboard — tak ada ejaan vet-clean untuk pola itu) |
| Frontend: `vitest run` 12/12, `eslint` bersih, `tsc && vite build` | ✅ |
| `wails build` → `Kitodock.exe` | ✅ (26 dtk) |
| Smoke-run (`KITODOCK_ROOT` sementara, non-elevated) | ✅ window terbuka, `config.json` tercipta dengan schema benar |
| **Audit originalitas** | ✅ lihat §4 |

† termasuk uji DACL nyata (set ACL khas via icacls → simpan → ACL harus
round-trip identik) — menemukan dua bug desain awal saya sendiri sebelum
dirilis: sharing-violation terhadap handle temp sendiri, dan pelebaran ACL
karena ACE warisan %TEMP% (butuh `PROTECTED_DACL_SECURITY_INFORMATION`).
‡ termasuk pemindai kontrak katalog: kunci terpakai ⊆ en.json, kunci
backend mati terdeteksi, panggilan `nil`-vars tanpa placeholder.

## 3. Keputusan & penyimpangan dari planning

1. **Strategi berubah di tengah fase** (pemicu penulisan ulang ini): dari
   "PORT/adaptasi" menjadi **implementasi mandiri**. PLAN D5/D6 dan
   dokumen 04 telah direvisi konsisten; NOTICE ditulis ulang.
2. **YAGNI diberlakukan mundur**: mesin DACL berlapis LaraEnv tidak
   direproduksi — `WriteKeepingPermissions` yang lebih sederhana cukup untuk
   config.json; versi berlapis dirancang ulang saat editor dokumen (Fase 4)
   membutuhkannya. Fitur override katalog disk dipangkas dari scope (katalog
   cukup id+en rilis awal).
3. API i18n kitodock sendiri (`Bundle/Text/Fail/FailWrap`), nama field
   skema sendiri (`Item/Kind/Delivery/autoTranslated`), prefiks envelope
   DPAPI sendiri (`kddp1:`).
4. `SyncLanguage` sebagai method controller akan ikut ter-bind Wails dan
   bisa menggantung promise frontend → dijadikan fungsi bebas
   `WatchLanguageFor`.
5. Elevasi admin tetap tidak dipaksakan (asInvoker); call site + manifest
   menyusul Fase 1. `winshow` sengaja minimal (tanpa trik ALT-tap) —
   ditambah saat command bar (Fase 6) membuktikan butuh.
6. `frontend/dist/index.html` placeholder di-commit agar `go test` hijau di
   clone baru; stub `wailsjs/` tulisan tangan di-gitignore.
7. Toolchain efektif Go 1.26.10 (unduhan otomatis, disyaratkan x/sys
   v0.48.0); `tools/tools.go` mem-pin dependensi CLI wails.
8. Ikon placeholder dari `tools/genicon` — wajib diganti sebelum rilis.
9. Verifikasi manual tersisa (5 menit, butuh interaksi GUI): `wails dev`,
   ganti tema/bahasa → langsung efektif + persist setelah restart.

## 4. Audit originalitas (metode + hasil)

Metode: kumpulkan semua baris non-trivial (panjang ≥ 15, bukan import /
build-tag / baris sintaks murni) dari seluruh `.go/.ts/.tsx/.css/.js`
kedua repo, bandingkan himpunan saling-irisan. Skrip (jalankan dari root
workspace, Python 3):

```python
import os, re
def collect(root):
    lines = set()
    for dp, ds, fs in os.walk(root):
        ds[:] = [d for d in ds if d not in ("node_modules",".git","dist","wailsjs","build","bin","frontend")]
        for f in fs:
            if not f.endswith((".go",".ts",".tsx",".css",".js")): continue
            for line in open(os.path.join(dp,f), encoding="utf-8", errors="ignore").read().splitlines():
                t = line.strip()
                if len(t) < 15 or t.startswith(("import ","//go:","package ")): continue
                if re.fullmatch(r"[\{\}\(\)\[\];,]+", t): continue
                lines.add(t)
    return lines
kito, lara = collect("kitodock"), collect("laraenv")
shared = kito & lara
comments = [l for l in shared if l.startswith("//")]
print(len(shared), f"{100*len(shared)/len(kito):.1f}%", "identical comments:", len(comments))
```

Hasil akhir Fase 0 v2: **183 baris identik dari 1.655 baris non-trivial
kitodock (11,0%), dengan 0 baris komentar/prosa identik.** Isi 183 baris
tersebut terklasifikasi penuh: pernyataan import framework (`wails/v2`,
`x/text`, `fsnotify`, …), literal opsi Wails yang dipaksa bentuknya
framework, baris tag skema JSON kontrak (mis. `Enabled map[string]bool
\`json:"enabled"\``), nilai default config (`"nginx": true,`), dan idiom Go
satu-baris (`return nil, err`, `dir := t.TempDir()`). Tidak ada fungsi,
blok logika, atau dokumentasi yang tersalin. Audit ini diulang tiap akhir
fase (aturan baru PLAN §7).

## 5. Lanjut ke Fase 1
Services & Installer — `services`, `installer`, `vcruntime`,
`runtimeinventory`, `tray`, `sysmetrics`, `netports`; halaman
Downloads/Dashboard nyata; aktifasi elevasi + hide-to-tray; meta-test
no-bare-string frontend.
