package i18n

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The shipped catalogue and the code must agree. This test reads the real
// desktop/langs/en.json and walks the module's Go sources for catalogue keys
// used through the i18n API, then asserts:
//
//   - every key the code asks for exists in en.json (a missing one renders
//     as its own dotted path in the UI);
//   - every errors.*/backend.* key in en.json is actually referenced
//     somewhere (dead keys are translator workload);
//   - a key whose English value carries {{placeholders}} is never called
//     with nil vars (the rendered string would show a literal {{token}}).
//
// It scans source text with regexes rather than a type checker on purpose:
// a missed exotic call shape is a false negative (safe), while a type-checked
// inventory that misses nothing is a project of its own.

var keyUses = regexp.MustCompile(
	`i18n\.(?:Text|Fail|FailWrap)\(\s*(?:[^,]+,\s*)?"([a-z][A-Za-z0-9]*(?:\.[A-Za-z0-9_]+)+)"`,
)

var nilVarCalls = regexp.MustCompile(
	`i18n\.(?:Text|Fail)\(\s*(?:[^,]+,\s*)?"([a-z][A-Za-z0-9]*(?:\.[A-Za-z0-9_]+)+)"\s*,\s*nil\s*\)`,
)

// Relative to this package: the module root and the shipped English
// catalogue inside it.
const (
	moduleRoot = "../.."
	englishDoc = "../../desktop/langs/en.json"
)

func englishKeys(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(englishDoc)
	if err != nil {
		t.Fatalf("cannot open the shipped English catalogue: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("the shipped English catalogue does not parse: %v", err)
	}
	keys := map[string]string{}
	pluralish := map[string]bool{"zero": true, "one": true, "two": true, "few": true, "many": true, "other": true}
	var walk func(node any, prefix string)
	walk = func(node any, prefix string) {
		switch value := node.(type) {
		case string:
			keys[prefix] = value
		case map[string]any:
			allPlural := len(value) > 0
			for k := range value {
				if !pluralish[k] {
					allPlural = false
					break
				}
			}
			if allPlural {
				joined := []string{}
				for _, v := range value {
					if s, ok := v.(string); ok {
						joined = append(joined, s)
					}
				}
				keys[prefix] = strings.Join(joined, "\x00")
				return
			}
			for k, v := range value {
				if k == "$meta" {
					continue
				}
				path := k
				if prefix != "" {
					path = prefix + "." + k
				}
				walk(v, path)
			}
		}
	}
	walk(doc, "")
	return keys
}

func moduleSources(t *testing.T) []string {
	t.Helper()
	var sources []string
	skip := map[string]bool{"node_modules": true, "frontend": true, "build": true, "bin": true, ".git": true}
	root := moduleRoot
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if skip[entry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			sources = append(sources, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return sources
}

func TestEveryUsedKeyShips(t *testing.T) {
	shipped := englishKeys(t)
	var missing []string
	seen := map[string]bool{}
	for _, source := range moduleSources(t) {
		raw, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range keyUses.FindAllSubmatch(raw, -1) {
			key := string(match[1])
			seen[key] = true
			if _, ok := shipped[key]; !ok {
				missing = append(missing, key)
			}
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("keys used in code but absent from desktop/langs/en.json:\n  %s",
			strings.Join(missing, "\n  "))
	}
}

func TestNoDeadBackendKeys(t *testing.T) {
	anyUse := map[string]bool{}
	for _, source := range moduleSources(t) {
		raw, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		text := string(raw)
		for key := range englishKeys(t) {
			if strings.Contains(text, `"`+key+`"`) {
				anyUse[key] = true
			}
		}
	}
	var dead []string
	for key := range englishKeys(t) {
		if !strings.HasPrefix(key, "errors.") && !strings.HasPrefix(key, "backend.") {
			continue // UI namespaces are covered by the frontend catalogue test
		}
		if !anyUse[key] {
			dead = append(dead, key)
		}
	}
	sort.Strings(dead)
	if len(dead) > 0 {
		t.Fatalf("backend keys nobody references:\n  %s", strings.Join(dead, "\n  "))
	}
}

func TestNilVarCallsHaveNoPlaceholders(t *testing.T) {
	shipped := englishKeys(t)
	var offenders []string
	for _, source := range moduleSources(t) {
		raw, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range nilVarCalls.FindAllSubmatch(raw, -1) {
			key := string(match[1])
			if value, ok := shipped[key]; ok && strings.Contains(value, "{{") {
				offenders = append(offenders, source+": "+key)
			}
		}
	}
	if len(offenders) > 0 {
		t.Fatalf("keys called with nil vars whose English value has placeholders:\n  %s",
			strings.Join(offenders, "\n  "))
	}
}
