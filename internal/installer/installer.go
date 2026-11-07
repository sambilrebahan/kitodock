// Package installer turns pinned download URLs into installed runtimes and
// services: fetch into a cache, verify the archive is an archive, extract
// without ever escaping the destination, and leave a marker behind so
// "installed" is a fact on disk rather than a guess.
//
// The catalog is hand-pinned, exactly like a lockfile: an install that
// quietly drifts to whatever is newest today is an install nobody signed
// off on. Refreshing a pin is a deliberate commit.
package installer

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"kitodock/internal/atomicfile"
	"kitodock/internal/runtimeinventory"
)

// Kind groups sources in the Downloads screen.
type Kind string

const (
	KindWebServer Kind = "web-server"
	KindDatabase  Kind = "database"
	KindCache     Kind = "cache"
	KindMail      Kind = "mail"
	KindRuntime   Kind = "runtime"
	KindTool      Kind = "tool"
)

// Source is one installable thing at one pinned version.
type Source struct {
	Kind     Kind
	ID       string // stable id, "nginx"
	Version  string // pinned, "1.28.0"
	LabelKey string

	// URL is the direct download. Empty for Manual sources.
	URL string
	// PageURL is where a human finds the download when hotlinking is
	// blocked; the archive then arrives via InstallFromArchive.
	Manual  bool
	PageURL string

	// Direct marks a single-file download (composer.phar) copied as-is
	// instead of being extracted.
	Direct bool

	// Flatten strips a single shared top-level directory from the archive
	// (zips that wrap everything in nginx-1.28.0/).
	Flatten bool

	// InstallTo is the destination directory.
	InstallTo func() string
	// ExeName is the binary that proves the install, when there is one.
	ExeName string
	// RuntimeVersion reads the installed binary's own version (PHP, Node);
	// ExeName + marker decide everything else.
	RuntimeVersion bool

	// RedistURL overrides the VC++ download for this build.
	RedistURL string

	// PlainUA drops the identifying User-Agent for hosts whose edge
	// refuses scripted agents by name (MySQL's CDN blocks anything it has
	// not seen before). The Go default agent passes.
	PlainUA bool
}

// Phase names the progress milestones of an install.
const (
	PhaseDownload = "download"
	PhaseExtract  = "extract"
	PhaseDone     = "done"
	PhaseError    = "error"
)

// Progress is one install heartbeat. The UI renders phase + numbers; an
// install reports at most a few times a second, not per chunk.
type Progress struct {
	ID       string `json:"id"`
	Version  string `json:"version"`
	Phase    string `json:"phase"`
	Bytes    int64  `json:"bytes"`
	Total    int64  `json:"total"`
	Percent  int    `json:"percent"`
	SpeedBps int64  `json:"speedBps"`
	Note     string `json:"note,omitempty"`
}

// Manager installs catalog sources into the environment.
type Manager struct {
	cacheDir string
	onEvent  func(Progress)
	client   *http.Client
}

func NewManager(cacheDir string, onEvent func(Progress)) *Manager {
	return &Manager{
		cacheDir: cacheDir,
		onEvent:  onEvent,
		client:   &http.Client{Timeout: 0}, // per-request contexts bound the time
	}
}

// ErrManualNeeded tells the UI a source refuses direct downloads; the user
// fetches the archive themselves and hands it to InstallFromArchive.
type ErrManualNeeded struct {
	Source Source
}

func (e *ErrManualNeeded) Error() string {
	return fmt.Sprintf("%s %s must be downloaded manually", e.Source.ID, e.Source.Version)
}

// archivePath is where a source's archive lives once fetched. Manual
// sources expect the user to drop the file exactly here.
func (m *Manager) archivePath(src Source) string {
	return filepath.Join(m.cacheDir, fmt.Sprintf("%s-%s%s", src.ID, src.Version, archiveExt(src)))
}

func archiveExt(src Source) string {
	if src.Direct {
		return ".phar"
	}
	return ".zip"
}

// markerPath names the install receipt inside the destination.
func markerPath(src Source) string {
	return filepath.Join(src.InstallTo(), ".kitodock-version")
}

// IsInstalled reports whether the source's artifact is present: the pinned
// marker plus (when declared) the expected binary — or, for runtimes, the
// binary saying the right version out of its own version resource.
func (m *Manager) IsInstalled(src Source) bool {
	marker, err := os.ReadFile(markerPath(src))
	if err != nil || strings.TrimSpace(string(marker)) != src.Version {
		return false
	}
	if src.ExeName == "" {
		return true
	}
	exe := filepath.Join(src.InstallTo(), src.ExeName)
	if _, err := os.Stat(exe); err != nil {
		return false
	}
	if src.RuntimeVersion {
		meta, err := runtimeinventory.Read(exe)
		if err != nil || meta.FullVersion != src.Version {
			return false
		}
	}
	return true
}

// Install fetches (or reuses) the archive and lays it down. Install of an
// already-installed source is a cheap no-op, so "Bootstrap stack" can be
// pressed without thinking.
func (m *Manager) Install(ctx context.Context, src Source) error {
	if m.IsInstalled(src) {
		return nil
	}
	archive := m.archivePath(src)
	if src.Manual {
		if _, err := os.Stat(archive); err != nil {
			return &ErrManualNeeded{Source: src}
		}
	} else if !m.cachedArchiveValid(archive) {
		if err := m.download(ctx, src, archive); err != nil {
			return err
		}
	}
	if src.Direct {
		return m.layDownDirect(ctx, src, archive)
	}
	return m.extract(ctx, src, archive)
}

// InstallFromArchive installs a source from an archive the user fetched
// themselves (Manual sources), copying it into the cache first so the usual
// install path stays uniform.
func (m *Manager) InstallFromArchive(ctx context.Context, src Source, archivePath string) error {
	data, err := os.ReadFile(archivePath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(m.cacheDir, 0o755); err != nil {
		return err
	}
	if err := atomicfile.Write(m.archivePath(src), data, 0o644); err != nil {
		return err
	}
	return m.extract(ctx, src, m.archivePath(src))
}

// cachedArchiveValid accepts a cached file only when it is non-empty and
// carries its format magic: a truncated download must never install.
func (m *Manager) cachedArchiveValid(archive string) bool {
	f, err := os.Open(archive)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 4)
	n, _ := f.Read(head)
	if n < 4 {
		return false
	}
	if strings.HasSuffix(archive, ".zip") {
		return head[0] == 'P' && head[1] == 'K'
	}
	return true // phar: anything non-empty; composer.phar starts with its own magic
}

func (m *Manager) download(ctx context.Context, src Source, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.URL, nil)
	if err != nil {
		return err
	}
	// Some hosts (windows.php.net included) refuse bare clients; others
	// refuse unknown custom agents instead — PlainUA picks the Go default
	// for those.
	if !src.PlainUA {
		req.Header.Set("User-Agent", "Kitodock-installer/0.1 (+https://github.com/sambilrebahan/kitodock)")
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("download %s %s: %w", src.ID, src.Version, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s %s: status %d", src.ID, src.Version, resp.StatusCode)
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	part := dest + ".part"
	out, err := os.Create(part)
	if err != nil {
		return err
	}

	started := time.Now()
	counted := &countingWriter{under: out}
	// An opening beat, so even a cache-warming one-tick download is visible
	// in the UI as a download rather than appearing already extracted.
	m.emit(Progress{ID: src.ID, Version: src.Version, Phase: PhaseDownload,
		Bytes: 0, Total: resp.ContentLength})
	// The progress ticker, not the copy loop, sets the reporting pace.
	stopTicker := make(chan struct{})
	tickerDone := make(chan struct{})
	go func() {
		defer close(tickerDone)
		interval := time.NewTicker(250 * time.Millisecond)
		defer interval.Stop()
		for {
			select {
			case <-stopTicker:
				return
			case <-interval.C:
				copied := atomicLoad64(&counted.n)
				total := resp.ContentLength
				m.emit(Progress{
					ID: src.ID, Version: src.Version, Phase: PhaseDownload,
					Bytes: copied, Total: total,
					Percent:  percentOf(copied, total),
					SpeedBps: int64(float64(copied) / time.Since(started).Seconds()),
				})
			}
		}
	}()

	_, copyErr := io.Copy(counted, resp.Body)
	syncErr := out.Sync()
	closeErr := out.Close()
	close(stopTicker)
	<-tickerDone
	if copyErr != nil || syncErr != nil || closeErr != nil {
		os.Remove(part)
		return errors.Join(copyErr, syncErr, closeErr)
	}
	// And a closing beat with the full size, for the same reason.
	final := atomicLoad64(&counted.n)
	m.emit(Progress{ID: src.ID, Version: src.Version, Phase: PhaseDownload,
		Bytes: final, Total: resp.ContentLength,
		Percent: percentOf(final, resp.ContentLength)})
	return os.Rename(part, dest)
}

// countingWriter tracks bytes seen so far behind an atomic load, letting
// the progress ticker read a consistent number without a lock.
type countingWriter struct {
	under io.Writer
	n     int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	atomicAdd64(&c.n, int64(len(p)))
	return c.under.Write(p)
}

// layDownDirect places a single-file source and, for composer.phar, the
// wrapper scripts that make `composer` runnable from anywhere we manage.
func (m *Manager) layDownDirect(ctx context.Context, src Source, archive string) error {
	dest := src.InstallTo()
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	data, err := os.ReadFile(archive)
	if err != nil {
		return err
	}
	name := path.Base(strings.TrimSuffix(src.URL, "/"))
	if !strings.HasSuffix(name, ".phar") {
		name = src.ID
	}
	if err := atomicfile.Write(filepath.Join(dest, name), data, 0o644); err != nil {
		return err
	}
	if strings.HasSuffix(name, ".phar") {
		if err := writePharWrappers(dest, strings.TrimSuffix(name, ".phar")); err != nil {
			return err
		}
	}
	return m.markInstalled(src)
}

// extract unpacks the archive into the destination, refusing any entry that
// tries to leave it, and optionally dropping a single wrapper directory.
func (m *Manager) extract(ctx context.Context, src Source, archive string) error {
	reader, err := zip.OpenReader(archive)
	if err != nil {
		return fmt.Errorf("open archive for %s: %w", src.ID, err)
	}
	defer reader.Close()

	dest := src.InstallTo()
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}

	strip := ""
	if src.Flatten {
		strip, err = sharedTopDir(reader.File)
		if err != nil {
			return err
		}
	}

	total := len(reader.File)
	for i, entry := range reader.File {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Strip the flatten wrapper only; a leading slash or anything else
		// fishy goes to withinDir and gets refused, not rescued.
		name := strings.TrimPrefix(entry.Name, strip)
		if name == "" {
			continue
		}
		target, safe := withinDir(dest, name)
		if !safe {
			return fmt.Errorf("archive for %s tries to escape its destination at %q", src.ID, entry.Name)
		}
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := extractFile(entry, target); err != nil {
			return fmt.Errorf("extract %s from %s: %w", entry.Name, src.ID, err)
		}
		if i%25 == 0 || i == total-1 {
			m.emit(Progress{ID: src.ID, Version: src.Version, Phase: PhaseExtract,
				Bytes: int64(i + 1), Total: int64(total), Percent: percentOf(int64(i+1), int64(total))})
		}
	}
	return m.markInstalled(src)
}

func extractFile(entry *zip.File, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	in, err := entry.Open()
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(target)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// sharedTopDir returns the single top-level folder wrapping every entry
// ("nginx-1.28.0/"), or "" when the archive has none to strip.
func sharedTopDir(files []*zip.File) (string, error) {
	top := ""
	for _, entry := range files {
		slash := strings.IndexByte(entry.Name, '/')
		if slash <= 0 {
			return "", nil // a root-level file: nothing to strip anywhere
		}
		candidate := entry.Name[:slash+1]
		if top == "" {
			top = candidate
		} else if top != candidate {
			return "", nil
		}
	}
	return top, nil
}

// withinDir joins dir+name and refuses anything that is not a plain
// relative path: dot-dot climbs, absolute paths, drive letters and
// backslashes are rejected outright — a hostile archive is refused, not
// quietly rewritten into a harmless name. A single trailing slash (zip
// directory entries) is fine.
func withinDir(dir, name string) (string, bool) {
	name = strings.TrimSuffix(name, "/")
	if name == "" || strings.ContainsAny(name, `:\`) || strings.HasPrefix(name, "/") {
		return "", false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." {
			return "", false
		}
	}
	return filepath.Join(dir, name), true
}

func (m *Manager) markInstalled(src Source) error {
	return atomicfile.Write(markerPath(src), []byte(src.Version), 0o644)
}

func (m *Manager) emit(p Progress) {
	if m.onEvent != nil {
		m.onEvent(p)
	}
}

func percentOf(part, total int64) int {
	if total <= 0 {
		return 0
	}
	p := int(float64(part) / float64(total) * 100)
	if p > 100 {
		return 100
	}
	return p
}

// writePharWrappers drops the two-line launchers that run a phar with the
// environment's PHP, whatever it is at call time.
func writePharWrappers(dir, stem string) error {
	bat := fmt.Sprintf("@echo off\r\nphp \"%%~dp0%s.phar\" %%*\r\n", stem)
	if err := atomicfile.Write(filepath.Join(dir, stem+".bat"), []byte(bat), 0o755); err != nil {
		return err
	}
	sh := fmt.Sprintf("#!/bin/sh\nexec php \"$(dirname \"$0\")/%s.phar\" \"$@\"\n", stem)
	return atomicfile.Write(filepath.Join(dir, stem), []byte(sh), 0o755)
}

func atomicLoad64(v *int64) int64   { return atomic.LoadInt64(v) }
func atomicAdd64(v *int64, d int64) { atomic.AddInt64(v, d) }
