# 04 — Rancangan Modul & Rename (revisi strategi)

> **Strategi berlaku sejak Fase 0 v2 (2026-11-06): IMPLEMENTASI MANDIRI.**
> LaraEnv adalah referensi perilaku dan arsitektur — kode kitodock ditulis
> sendiri seluruhnya. Dokumen ini memetakan *perilaku apa yang dirujuk* per
> modul dan *rancangan kitodock sendiri* untuk mewujudkannya, bukan rencana
> penyalinan. Audit baris-identik dijalankan tiap akhir fase
> (docs/progress/fase-0.md memuat skripnya).

## 1. Prinsip implementasi mandiri

1. Baca entri modul di `01-inventaris-laraenv.md` untuk memahami **perilaku**
   (kontrak, kasus tepi, keputusan keamanan) — lalu tutup kodenya.
2. Rancang API/nama/struktur kitodock sendiri; nama boleh sama hanya bila
   nama itu merupakan satu-satunya ejaan alami (mis. `SetLanguage`).
3. Format berkas/data yang menjadi kontrak lintas-proses (nama event Wails,
   struktur config.json, format katalog) boleh setara — itu antarmuka,
   bukan ekspresi.
4. Fitur yang belum dibutuhkan fasenya **tidak** diimplementasikan dulu
   (YAGNI): mesin DACL berlapis, override katalog disk, dsb. menyusul saat
   ada pemakainya.
5. Akhir fase: audit baris-identik — wajib **0 komentar/prosa identik**;
   sisa hanya import/bentuk API pihak ketiga/idiom satu-baris.

## 2. Peta rujukan per paket `internal/` (perilaku → rancangan kitodock)

| Perilaku yang dirujuk dari LaraEnv | Rancangan kitodock | Status |
| --- | --- | --- |
| Resolusi root env-var > marker portable > root tetap (paths) | `paths` milik sendiri; accessor tumbuh per fase (Fase 0: Config/State/Projects/Logs saja) | ✅ Fase 0 |
| Penulisan file crash-safe + pertahanan izin (atomicfile) | `Write` + `WriteKeepingPermissions`; salin DACL via Get/SetSecurityInfo dengan PROTECTED_DACL; mesin pin direktori berlapis menunggu Fase 4 | ✅ Fase 0 |
| Katalog JSON + plural CLDR + interpolasi + error terlokalisasi (i18n) | `Bundle`/`Text`/`Fail`/`FailWrap`; format katalog `{$meta; kunci bertingkat; objek plural}`; `autoTranslated` (bukan needsNativeReview) | ✅ Fase 0 |
| Seal DPAPI per akun Windows (credentials) | `Sealer`/`Seal`/`Unseal` dengan prefiks envelope `kddp1:` | ✅ Fase 0 |
| Riwayat notifikasi persist + dedup + recovery korup (notifications) | `Store` dengan `Item/Kind/Scope/Delivery`; penamaan & skema JSON kitodock sendiri | ✅ Fase 0 |
| Preferensi config.json: backfill, relokasi root, watch lintas proses | `config.Open` + `commit()` revert-on-fail + `Watch` debounce 150ms | ✅ Fase 0 |
| Clipboard teks native tanpa cgo | user32 langsung, retry saat sibuk | ✅ Fase 0 |
| Supervisor 8 service + pool FPM + backoff restart | dirancang Fase 1 (`services`) | ⏳ |
| Katalog installer pinned + progress + zip-slip guard | dirancang Fase 1 (`installer`) | ⏳ |
| Multi-versi PHP/Node + skema port FPM | dirancang Fase 2 | ⏳ |
| Render vhost 3 server dari satu spec + CA lokal + hosts marker | dirancang Fase 2 (`vhost`, `localca` terpisah) | ⏳ |
| Resolusi constraint runtime (grammar Composer/npm) | dirancang Fase 2 (`projects`) | ⏳ |
| PTY ConPTY + registry PATH live | dirancang Fase 3 (`pty`) | ⏳ |
| Git facade tervalidasi (status/commit/push) | dirancang Fase 4 (`sourcecontrol`) | ⏳ |
| SSH + known_hosts TOFU + SFTP | dirancang Fase 5 (`ssh`) | ⏳ |
| Engine deploy bash + presets + histori terenkripsi | dirancang Fase 5 (`deploy`, `deployments`) | ⏳ |
| Agents 4 provider + hooks + activity multi-jendela | dirancang Fase 6 (`agents`) | ⏳ |
| MCP loopback 8 tool + registrasi klien | dirancang Fase 6 (`mcpbridge`, `mcpclient`) | ⏳ |
| Sync E2EE Argon2id+AES-GCM | TUNDA (tanpa cloud) | ⏸ |
| dll. (lihat 01-inventaris §C untuk daftar lengkap 45 paket) | — | — |

## 3. Rename map (tetap berlaku)

Tabel rename di `02-arsitektur-kitodock.md` §5 tidak berubah — itu kontrak
penamaan kitodock, bukan hasil salin.

## 4. API surface per controller (target paritas ±274 method)

Tabel sebaran per controller di bagian 3 dokumen sebelumnya tetap menjadi
target; controller tumbuh per fase. Terwujud Fase 0: `System` (9 method:
AppVersion, GetLanguage, ListLocales, SetLanguage, ReadClipboardText,
WriteClipboardText — plus fungsi non-bind `InstallCatalogues`,
`WatchLanguageFor`).

## 5. Hal yang SENGAJA tidak dibawa

Tidak berubah sejak revisi planning sebelumnya (lihat git historis / versi
lama dokumen ini): tanpa migrasi data LaraEnv, tanpa cloud/akun, katalog
pt-BR/ru/zh-CN menyusul, harness screenshot menunggu produk stabil, NSIS
tidak dipakai. Ditambah sejak v2: fitur override katalog disk dan mesin
DACL berlapis os.Root ditunda sampai fasenya (YAGNI).

## 6. Checklist tiap modul baru

1. Baca entri perilaku di 01-inventaris; tulis daftar kasus tepi.
2. Rancang API kitodock sendiri; catat di progress bila menyimpang.
3. Tulis kode + test sendiri (nama kasus/skop bebas, jangan terjemahan test
   LaraEnv).
4. Jalankan audit baris-identik; pastikan 0 komentar/prosa identik.
