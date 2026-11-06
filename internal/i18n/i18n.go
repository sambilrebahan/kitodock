// Package i18n renders user-facing strings from JSON catalogues
// (desktop/langs/*.json). The React side implements the same three semantics
// over the same files — dotted lookup, CLDR plural selection, {{var}}
// interpolation with locale-formatted {{count}} — so a sentence renders the
// same wherever it is produced.
//
// Catalogue shape:
//
//	{
//	  "$meta": { "code": "en", "name": "English", "nativeName": "English" },
//	  "nav": { "settings": "Settings" },
//	  "inbox": { "items": { "one": "{{count}} item", "other": "{{count}} items" } }
//	}
//
// A leaf is a string, or an object whose keys are all CLDR plural categories
// (one/other/…) which counts as a single translatable unit.
package i18n

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"golang.org/x/text/feature/plural"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
	"golang.org/x/text/number"
)

// FallbackCode is the catalogue every lookup falls back to. A key missing
// there is a bug, not a translation gap.
const FallbackCode = "en"

// pluralCategories doubles as the "is this node a plural unit" test; the
// order matches x/text/plural's Form constants (Other, Zero, One, Two, Few,
// Many).
var pluralCategories = [...]string{"other", "zero", "one", "two", "few", "many"}

func isPluralNode(node map[string]any) bool {
	if len(node) == 0 {
		return false
	}
	for key := range node {
		match := false
		for _, category := range pluralCategories {
			if key == category {
				match = true
				break
			}
		}
		if !match {
			return false
		}
	}
	return true
}

// Meta is the $meta block of one catalogue: what the language selector
// shows. Adding a language to the app is dropping a JSON file in langs/.
type Meta struct {
	Code           string `json:"code"`
	Name           string `json:"name"`
	NativeName     string `json:"nativeName"`
	AutoTranslated bool   `json:"autoTranslated"`
}

// Bundle is every catalogue that shipped, parsed once at startup.
type Bundle struct {
	trees map[string]map[string]any
	metas map[string]Meta
}

// Load parses every *.json under dir. Files that do not parse fail the call:
// shipping a catalogue the app cannot read must stop the launch, not render
// key paths at users.
func Load(fsys fs.FS, dir string) (*Bundle, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("read catalogue dir %s: %w", dir, err)
	}
	b := &Bundle{trees: map[string]map[string]any{}, metas: map[string]Meta{}}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		raw, err := fs.ReadFile(fsys, path.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		var tree map[string]any
		if err := json.Unmarshal(raw, &tree); err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		code := strings.TrimSuffix(name, ".json")
		meta := Meta{Code: code, Name: code, NativeName: code}
		if block, ok := tree["$meta"].(map[string]any); ok {
			if v, ok := block["name"].(string); ok {
				meta.Name = v
			}
			if v, ok := block["nativeName"].(string); ok {
				meta.NativeName = v
			}
			if v, ok := block["autoTranslated"].(bool); ok {
				meta.AutoTranslated = v
			}
		}
		b.trees[code] = tree
		b.metas[code] = meta
	}
	if _, ok := b.trees[FallbackCode]; !ok {
		return nil, fmt.Errorf("no %s.json under %s", FallbackCode, dir)
	}
	return b, nil
}

// Locales lists what shipped: the fallback first, the rest alphabetical.
func (b *Bundle) Locales() []Meta {
	out := make([]Meta, 0, len(b.metas))
	for _, meta := range b.metas {
		out = append(out, meta)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Code == FallbackCode {
			return true
		}
		if out[j].Code == FallbackCode {
			return false
		}
		return out[i].Code < out[j].Code
	})
	return out
}

// ValidLocale reports whether code names a shipped catalogue.
func (b *Bundle) ValidLocale(code string) bool {
	_, ok := b.trees[code]
	return ok
}

// Has reports whether lang itself defines key, without any fallback.
func (b *Bundle) Has(lang, key string) bool {
	_, ok := at(b.trees[lang], key)
	return ok
}

// Text renders key for lang: the locale's own value, else the fallback's,
// else the key path itself — visible and traceable beats a blank space.
func (b *Bundle) Text(lang, key string, vars map[string]any) string {
	node, ok := at(b.trees[lang], key)
	if !ok {
		node, ok = at(b.trees[FallbackCode], key)
		if !ok {
			return key
		}
		lang = FallbackCode
	}
	return expand(render(node, lang, vars), lang, vars)
}

// at walks a dotted path through a catalogue tree.
func at(tree map[string]any, key string) (any, bool) {
	if tree == nil {
		return nil, false
	}
	var node any = tree
	for _, part := range strings.Split(key, ".") {
		children, ok := node.(map[string]any)
		if !ok {
			return nil, false
		}
		node, ok = children[part]
		if !ok {
			return nil, false
		}
	}
	return node, true
}

// render flattens a catalogue node to a string, choosing a plural category
// when the node is a plural unit. A category the translator skipped falls
// back to "other" rather than rendering nothing.
func render(node any, lang string, vars map[string]any) string {
	switch value := node.(type) {
	case string:
		return value
	case map[string]any:
		category := "other"
		if n, ok := countOf(vars["count"]); ok {
			category = categoryFor(lang, n)
		}
		if text, ok := value[category].(string); ok {
			return text
		}
		if text, ok := value["other"].(string); ok {
			return text
		}
		return ""
	default:
		return ""
	}
}

// categoryFor asks CLDR which plural form lang uses for n. The frontend's
// Intl.PluralRules answers the identical question, so both halves of the app
// land on the same form for the same number.
func categoryFor(lang string, n int) string {
	tag, err := language.Parse(lang)
	if err != nil {
		tag = language.English
	}
	if n < 0 {
		n = -n
	}
	form := plural.Cardinal.MatchPlural(tag, n, 0, 0, 0, 0)
	if int(form) < len(pluralCategories) {
		return pluralCategories[form]
	}
	return "other"
}

// countOf accepts the shapes a count arrives in: int from Go code, float64
// after a trip through JSON.
func countOf(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	default:
		return 0, false
	}
}

// expand substitutes {{name}} placeholders. {{count}} is formatted for the
// locale because a count is prose; every other variable goes in verbatim so
// ports, PIDs and versions never grow thousands separators.
func expand(text, lang string, vars map[string]any) string {
	if len(vars) == 0 || !strings.Contains(text, "{{") {
		return text
	}
	for name, value := range vars {
		token := "{{" + name + "}}"
		if !strings.Contains(text, token) {
			continue
		}
		rendered := fmt.Sprint(value)
		if name == "count" {
			if n, ok := countOf(value); ok {
				tag, err := language.Parse(lang)
				if err != nil {
					tag = language.English
				}
				rendered = message.NewPrinter(tag).Sprint(number.Decimal(n))
			}
		}
		text = strings.ReplaceAll(text, token, rendered)
	}
	return text
}

// Match picks the best shipped catalogue for a preferred BCP-47 tag: an
// exact code, then anything sharing the base language, then the fallback.
func (b *Bundle) Match(preferred string) string {
	if preferred == "" {
		return FallbackCode
	}
	for _, meta := range b.metas {
		if strings.EqualFold(meta.Code, preferred) {
			return meta.Code
		}
	}
	base := languagePart(preferred)
	if base != "" {
		for _, meta := range b.metas {
			if languagePart(meta.Code) == base {
				return meta.Code
			}
		}
	}
	return FallbackCode
}

// languagePart strips the region from a BCP-47 tag ("id-ID" → "id").
func languagePart(tag string) string {
	if i := strings.IndexAny(strings.ToLower(tag), "-_"); i > 0 {
		return strings.ToLower(tag[:i])
	}
	return strings.ToLower(tag)
}
