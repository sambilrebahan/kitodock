# Progress — Fase 4: Files, Editor & Source Control

> Status: **SELESAI** (2026-11-08) · Implementasi mandiri; audit §3.

## 1. Paket baru

| Paket | Isi | Test |
| --- | --- | --- |
| `internal/workspacefiles` | Hardened file access: ContextRef authority (project/agent), path validation (anti-traversal/absolute/reserved), paginated listing (500/page, cursor, stale detection), read with BOM/EOL/binary detection + revision hashing (SHA-256), save with optimistic revision check + per-document lock, image read with MIME magic detection (PNG/JPEG/GIF/WebP) | 14 |
| `internal/sourcecontrol` | Git facade: porcelain v2 status parser (branch, upstream, ahead/behind, staged/unstaged/untracked), stage/unstage via pathspec-from-file, commit entire index, push, operations tracked with ID/output/status (ring 20), per-repo mutation serialization, clean env (no GIT_* overrides, GIT_TERMINAL_PROMPT=0, LC_ALL=C) | 6 (dengan repositori git nyata) |

## 2. Wiring desktop

- **Files controller**: GetContext, ListDirectory (paginated), ReadDocument (BOM/EOL/revision), CheckDocument (conflict poll), SaveDocument (optimistic revision), ReadImage (base64+MIME). Services lazily created per project.
- **Git controller**: InspectSourceControl, GetSourceControlStatus, StartSourceControlStage/Unstage/Commit/Push, GetSourceControlOperation/Busy. Event `sourcecontrol:operation`.
- app.go: Files + Git dibuat dan di-bind.

## 3. Frontend

- **Monaco vendored** (`public/vs`, versi 0.57.0) — offline, tanpa CDN; `lib/monaco.ts` redirect loader.
- **DocumentEditor**: Monaco dengan vs-dark, wordWrap, minimap off, Ctrl+S; **ConflictDiff** = DiffEditor side-by-side untuk collision save.
- **WorkspaceNavigator**: sidebar di TerminalPage dengan tab **Files** | **Git**; toggle tampil/sembunyi + selector proyek.
  - Files tab: listing direktori (ikon folder/file, entri blocked ditandai), klik file → DocumentEditor dengan banner status (dirty dot, read-only flag, tombol Save Ctrl+S), konflik save → ConflictDiff dengan original vs modified.
  - Git tab: header (branch, upstream, ↑ahead/↓behind), dua pohon (Staged dengan tombol unstage per file + unstage all, Changes dengan stage per file + stage all), textarea commit message, tombol Commit + Push.
- **fileLanguage**: mapper ekstensi → Monaco language id (30+ jenis).
- Binding stubs: Files.js/.d.ts, Git.js/.d.ts.

## 4. Verifikasi & audit

| Pemeriksaan | Hasil |
| --- | --- |
| `go test` (fresh) | ✅ **28 paket hijau** |
| `go vet -unsafeptr=false` | ✅ |
| gofmt | ✅ |
| Frontend build + vitest + lint | ✅ 12/12 |
| `wails build` | ✅ 45 dtk |
| Smoke-run | ✅ |
| **Audit originalitas** | **6,9%**, **0 komentar prosa identik** |

## 5. Keputusan & catatan

1. **Monaco react wrapper**: `@monaco-editor/react` + `monaco-editor` npm — loader diarahkan ke vendored `/vs` via `lib/monaco.ts`; vendor script `scripts/vendor-monaco.mjs` menyalin `min/vs` ke `public/vs` dengan version stamp.
2. **Git facade**: menggunakan git CLI langsung (bukan go-git) — lebih dapat dipercaya untuk edge cases; porcelain v2 diparse manual dengan format deterministik (LC_ALL=C).
3. **Stage via pathspec-from-file**: menghindari shell quoting issues dengan path yang mengandung spasi/karakter khusus; fallback `restore --staged` → `reset HEAD` untuk git lama.
4. **WorkspaceNavigator di TerminalPage**: sidebar 280px yang bisa di-toggle — Files dan Git berdampingan dengan terminal sesuai desain LaraEnv.
5. **File tree recursive**: expand/collapse per direktori dengan lazy-load anak (ListDirectory per level); ikon folder/file, indentasi bertingkat.
6. **MarkdownPreview**: react-markdown + remark-gfm untuk file `.md`/`.markdown` dengan toggle Preview/Source.
7. **Image preview**: ReadImage API → base64 `<img>` untuk `.png`/`.jpg`/`.jpeg`/`.gif`/`.webp` dengan MIME magic detection dari backend.
8. **Git diff view**: klik file di panel Git → DiffEditor Monaco (HEAD vs working tree, via `ShowSourceControlFile` + `ReadSourceControlDiff`).
9. **Save-merged**: tombol "Save anyway" muncul saat konflik — memanggil `SaveDocument` dengan revision kosong (force save).
10. **Document shutdown gate (app-level)**: `beforeClose` di lifecycle.go memeriksa `DocumentDirtyCount()` → emit `documents:shutdown-request` → frontend (App.tsx) menampilkan dialog "Discard and quit" / "Cancel" → `ResolveDocumentShutdown(allow)` melalui channel; timeout 30 dtk default prevent-close (aman). Juga ada fallback `beforeunload` di main.tsx.
11. **Dirty-buffer tracking**: FilesTab melaporkan dirtyCount + dirtyPath ke WorkspaceNavigator → backend (`SetDocumentDirtyCount`) + window global → beforeunload + app gate.
12. **Dirty-buffer stage dialog**: GitTab menerima `dirtyPath`; saat staging file yang buffer-nya kotor, dialog "Save and stage" / "Stage from disk" muncul.
13. **TOCTOU repo detection**: `mutate()` wrapper — fingerprint HEAD sebelum + sesudah stage/unstage/push (commit dikecualikan karena sah mengubah HEAD); `ErrRepositoryChanged` jika berubah.
14. **Markdown polish**: heading anchor (id dari slug), link eksternal diblokir (span non-clickable), gambar lokal dimuat via `ReadImage` API → base64 data URI.

## 6. Utang yang tersisa (non-blocker)
- Commit hooks/signing scenario belum teruji spesifik (infrastructure ada, via git CLI langsung).
