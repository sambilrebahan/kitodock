// Package projects: the creator builds new projects from templates. Each
// kind shells out to the tool that knows best (composer for Laravel, the
// pm for JS frameworks, git for clones) and streams every line of output
// to the caller.
package projects

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unicode"
)

// CreateKind is what to scaffold.
type CreateKind string

const (
	CreateBlank     CreateKind = "blank"
	CreateLaravel   CreateKind = "laravel"
	CreateWordPress CreateKind = "wordpress"
	CreateGit       CreateKind = "git"
	CreateVite      CreateKind = "vite"
	CreateNext      CreateKind = "next"
	CreateNuxt      CreateKind = "nuxt"
	CreateAstro     CreateKind = "astro"
)

// AllCreateKinds is the UI's picker order.
var AllCreateKinds = []CreateKind{
	CreateLaravel, CreateWordPress, CreateGit, CreateVite, CreateNext, CreateNuxt, CreateAstro, CreateBlank,
}

// CreateOptions is one scaffolding request.
type CreateOptions struct {
	Name           string
	Kind           CreateKind
	Dir            string // parent directory (the project folder is created inside)
	GitURL         string
	PackageManager string // npm/pnpm/yarn/bun (JS kinds)
	PhpExe         string // resolved PHP binary (Laravel)
	// ComposerPhar is the bundled composer, when PATH composer is absent.
	ComposerPhar string
}

// Creator resolves what to run; the package stays tool-agnostic.
type Creator struct {
	// PhpExeFor returns the php.exe path to run composer phars with.
	PhpExeFor func() string
	// ComposerFor returns the composer invocation as an argv slice —
	// either the executable alone or [php.exe, composer.phar].
	ComposerFor func() []string
	NodeDirFor  func() string // node dir to prepend to PATH
}

// Create scaffolds a project, streaming output through emit.
func (c *Creator) Create(ctx context.Context, opts CreateOptions, emit func(string)) error {
	name := sanitizeName(opts.Name)
	if name == "" {
		return fmt.Errorf("project name cannot be empty")
	}
	target := filepath.Join(opts.Dir, name)
	if _, err := os.Stat(target); err == nil {
		return fmt.Errorf("%s already exists", target)
	}
	if err := os.MkdirAll(opts.Dir, 0o755); err != nil {
		return err
	}

	// From here on the sanitized name is the only name: folder names,
	// composer/npm arguments and messages all use it.
	opts.Name = name

	switch opts.Kind {
	case CreateBlank:
		emit(fmt.Sprintf("creating %s", target))
		return os.MkdirAll(target, 0o755)

	case CreateLaravel:
		return c.runLaravel(ctx, opts, target, emit)

	case CreateWordPress:
		return c.runWordPress(ctx, opts, target, emit)

	case CreateGit:
		if opts.GitURL == "" {
			return fmt.Errorf("git clone needs a URL")
		}
		// git clone refuses a working directory that does not exist;
		// run from the parent and name the target explicitly.
		return c.run(ctx, emit, "git", filepath.Dir(target), "clone", "--progress", opts.GitURL, target)

	case CreateVite, CreateNext, CreateNuxt, CreateAstro:
		return c.runJS(ctx, opts, target, emit)

	default:
		return fmt.Errorf("unknown project kind %q", opts.Kind)
	}
}

func (c *Creator) runLaravel(ctx context.Context, opts CreateOptions, target string, emit func(string)) error {
	composer := c.ComposerFor()
	if len(composer) == 0 {
		return fmt.Errorf("composer not found (install it from Libs / Tools)")
	}
	phpExe := c.PhpExeFor()
	if phpExe == "" {
		return fmt.Errorf("no PHP runtime installed")
	}
	// composer is an argv: [composer.exe …] or [php.exe, phar …].
	composerArgs := append(append([]string{}, composer[1:]...),
		"create-project", "laravel/laravel", opts.Name, "--no-interaction", "--prefer-dist")

	// Laravel's dev script includes `php artisan pail` which needs POSIX
	// pcntl — strip it on Windows so `composer run dev` works.
	emit(fmt.Sprintf("composer create-project laravel/laravel %s", opts.Name))
	env := append(os.Environ(), "PATH="+filepath.Dir(phpExe)+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := c.runEnv(ctx, emit, composer[0], filepath.Dir(target), composerArgs, env); err != nil {
		return err
	}
	return StripPailFromDevScript(target)
}

func (c *Creator) runWordPress(ctx context.Context, opts CreateOptions, target string, emit func(string)) error {
	// WordPress is a zip download; no package manager involved.
	emit("downloading WordPress…")
	zipURL := "https://wordpress.org/latest.zip"
	tmp, err := os.CreateTemp("", "wp-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := downloadTo(ctx, zipURL, tmp); err != nil {
		return err
	}
	tmp.Close()
	emit("extracting…")
	return extractZipTo(tmp.Name(), target, "wordpress/")
}

func (c *Creator) runJS(ctx context.Context, opts CreateOptions, target string, emit func(string)) error {
	pm := opts.PackageManager
	if pm == "" {
		pm = "npm"
	}
	nodeDir := c.NodeDirFor()
	pathPrefix := ""
	if nodeDir != "" {
		pathPrefix = "PATH=" + nodeDir + string(os.PathListSeparator) + os.Getenv("PATH")
	}

	// The create- tool per framework, with the version tag npm-family
	// resolvers expect (`npm create vite@latest app`).
	var tool string
	switch opts.Kind {
	case CreateVite:
		tool = "vite@latest"
	case CreateNext:
		tool = "next-app@latest"
	case CreateNuxt:
		tool = "nuxt@latest"
	case CreateAstro:
		tool = "astro@latest"
	}

	// `npm create vite@latest my-app` / `pnpm create vite my-app` / etc.
	emit(fmt.Sprintf("%s create %s %s", pm, tool, opts.Name))
	var env []string
	if pathPrefix != "" {
		env = append(os.Environ(), pathPrefix)
	} else {
		env = os.Environ()
	}
	return c.runEnv(ctx, emit, pm, filepath.Dir(target), []string{"create", tool, opts.Name}, env)
}

// run wraps a command with streaming output.
func (c *Creator) run(ctx context.Context, emit func(string), name, dir string, args ...string) error {
	return c.runEnv(ctx, emit, name, dir, args, os.Environ())
}

func (c *Creator) runEnv(ctx context.Context, emit func(string), name, dir string, args, env []string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("run %s: %w", name, err)
	}
	buf := make([]byte, 4096)
	for {
		n, err := stdout.Read(buf)
		if n > 0 {
			for _, line := range strings.Split(strings.TrimRight(string(buf[:n]), "\r\n"), "\n") {
				if line != "" {
					emit(line)
				}
			}
		}
		if err != nil {
			break
		}
	}
	return cmd.Wait()
}

// sanitizeName keeps folder names filesystem-safe.
func sanitizeName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

// StripPailFromDevScript removes `php artisan pail` from composer.json's
// dev script on Windows (it needs POSIX pcntl; keeping it breaks
// `composer run dev` with a process-tree kill).
func StripPailFromDevScript(projectDir string) error {
	composerPath := filepath.Join(projectDir, "composer.json")
	data, err := os.ReadFile(composerPath)
	if err != nil {
		return nil // no composer.json: nothing to strip
	}
	text := string(data)
	if !strings.Contains(text, "pail") {
		return nil
	}
	// Surgical removal of the pail entry and any preceding "&&".
	text = strings.ReplaceAll(text, " && php artisan pail", "")
	text = strings.ReplaceAll(text, "php artisan pail && ", "")
	text = strings.ReplaceAll(text, "php artisan pail", "")
	// Clean up any double spaces left behind.
	text = strings.ReplaceAll(text, "  ", " ")
	return os.WriteFile(composerPath, []byte(text), 0o644)
}

// downloadTo streams a URL to a writer (WordPress zip).
func downloadTo(ctx context.Context, url string, out *os.File) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Kitodock-installer/0.1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: status %d", url, resp.StatusCode)
	}
	_, err = io.Copy(out, resp.Body)
	return err
}

// extractZipTo unpacks a zip, stripping an optional top-level prefix.
func extractZipTo(zipPath, destDir, stripPrefix string) error {
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer reader.Close()
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}
	for _, entry := range reader.File {
		name := strings.TrimPrefix(entry.Name, stripPrefix)
		if name == "" {
			continue
		}
		target := filepath.Join(destDir, name)
		if !strings.HasPrefix(target, filepath.Clean(destDir)+string(os.PathSeparator)) {
			return fmt.Errorf("zip entry %q escapes destination", entry.Name)
		}
		if entry.FileInfo().IsDir() {
			os.MkdirAll(target, 0o755)
			continue
		}
		os.MkdirAll(filepath.Dir(target), 0o755)
		in, err := entry.Open()
		if err != nil {
			return err
		}
		out, err := os.Create(target)
		if err != nil {
			in.Close()
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			in.Close()
			out.Close()
			return err
		}
		in.Close()
		out.Close()
	}
	return nil
}
