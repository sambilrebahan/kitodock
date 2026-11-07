package php

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

// CoreExtensions are enabled at first ini generation when — and only when —
// their DLL actually ships in the version's ext/ folder.
var CoreExtensions = []string{
	"bcmath", "curl", "exif", "fileinfo", "gd", "gmp", "intl",
	"mbstring", "mysqli", "openssl", "pdo_mysql", "pdo_pgsql", "pdo_sqlite",
	"sockets", "soap", "sodium", "sqlite3", "xsl", "zip",
}

func hideWindow() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}

// versionFromExecutable reads php.exe's own version resource.
func versionFromExecutable(exe string) (string, error) {
	meta, err := runtimeRead(exe)
	if err != nil {
		return "", err
	}
	return meta, nil
}

// EnsureIni makes a freshly extracted PHP tree usable: a php.ini seeded
// from the shipped development template, an absolute extension_dir (the
// relative default resolves against the process CWD on Windows) and every
// bundled core extension switched on. Idempotent: an existing php.ini is
// only repaired where the two directives we own are wrong.
func (m *Manager) EnsureIni(versionDir string) error {
	ini := filepath.Join(versionDir, "php.ini")
	if _, err := os.Stat(ini); err != nil {
		seed := ""
		for _, candidate := range []string{"php.ini-development", "php.ini-production"} {
			if _, err := os.Stat(filepath.Join(versionDir, candidate)); err == nil {
				seed = candidate
				break
			}
		}
		if seed == "" {
			return fmt.Errorf("%s ships no ini template", versionDir)
		}
		data, err := os.ReadFile(filepath.Join(versionDir, seed))
		if err != nil {
			return err
		}
		if err := os.WriteFile(ini, data, 0o644); err != nil {
			return err
		}
	}
	lines, err := readIniLines(ini)
	if err != nil {
		return err
	}

	extDir := strings.ReplaceAll(filepath.Join(versionDir, "ext"), `\`, "/")
	lines = enforceDirective(lines, "extension_dir", `"`+extDir+`"`, false)

	have := map[string]bool{}
	if entries, err := os.ReadDir(filepath.Join(versionDir, "ext")); err == nil {
		for _, e := range entries {
			have[e.Name()] = true
		}
	}
	for _, ext := range CoreExtensions {
		if have["php_"+ext+".dll"] {
			lines = enableExtension(lines, ext)
		}
	}
	return writeIniLines(ini, lines)
}

var directivePattern = func(key string) *regexp.Regexp {
	return regexp.MustCompile(`^\s*;?\s*` + regexp.QuoteMeta(key) + `\s*=`)
}

// enforceDirective sets key=value, uncommenting or appending as needed.
// quoteValue decides whether the value gets ini-style double quotes.
func enforceDirective(lines []string, key, value string, _ bool) []string {
	re := directivePattern(key)
	replaced := false
	for i, line := range lines {
		if re.MatchString(line) {
			if !strings.HasPrefix(strings.TrimSpace(line), ";"+key) &&
				strings.TrimSpace(line) == strings.TrimSpace(key)+` = `+value {
				replaced = true
				continue
			}
			lines[i] = key + " = " + value
			replaced = true
		}
	}
	if !replaced {
		lines = append(lines, key+" = "+value)
	}
	return lines
}

// enableExtension ensures an active extension=<name> line exists exactly
// once, commented or not.
func enableExtension(lines []string, ext string) []string {
	re := regexp.MustCompile(`^\s*;?\s*extension\s*=\s*php_` + regexp.QuoteMeta(ext) + `\.dll`)
	found := false
	for i, line := range lines {
		if re.MatchString(line) {
			if found {
				lines[i] = "" // a duplicate would warn on every start
			} else {
				lines[i] = "extension=php_" + ext + ".dll"
				found = true
			}
		}
	}
	if !found {
		lines = append(lines, "extension=php_"+ext+".dll")
	}
	return lines
}

func readIniLines(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	return strings.Split(text, "\n"), nil
}

func writeIniLines(path string, lines []string) error {
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
}
