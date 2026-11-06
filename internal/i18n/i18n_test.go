package i18n

import (
	"errors"
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

func loadFixture(t *testing.T) *Bundle {
	t.Helper()
	b, err := Load(os.DirFS("testdata"), ".")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestLoadRejectsBundleWithoutFallback(t *testing.T) {
	fsys := fstest.MapFS{"xx.json": &fstest.MapFile{Data: []byte(`{"greet":"s"}`)}}

	if _, err := Load(fsys, "."); err == nil {
		t.Fatal("a bundle without en.json must fail the load")
	}
}

func TestLoadRejectsUnparseableCatalogue(t *testing.T) {
	fsys := fstest.MapFS{
		"en.json": &fstest.MapFile{Data: []byte(`{"a":"b"}`)},
		"xx.json": &fstest.MapFile{Data: []byte(`{broken`)},
	}

	if _, err := Load(fsys, "."); err == nil || !strings.Contains(err.Error(), "xx.json") {
		t.Fatalf("err = %v, want a parse failure naming xx.json", err)
	}
}

func TestLocalesFallbackFirstThenAlphabetical(t *testing.T) {
	b := loadFixture(t)

	codes := []string{}
	for _, meta := range b.Locales() {
		codes = append(codes, meta.Code)
	}
	if len(codes) != 2 || codes[0] != FallbackCode || codes[1] != "xx" {
		t.Fatalf("locale order = %v, want [en xx]", codes)
	}
}

func TestMetaComesFromTheCatalogue(t *testing.T) {
	b := loadFixture(t)

	var xx Meta
	for _, meta := range b.Locales() {
		if meta.Code == "xx" {
			xx = meta
		}
	}
	if xx.NativeName != "Fikstür" || !xx.AutoTranslated {
		t.Fatalf("xx meta = %+v, want the fixture values", xx)
	}
}

func TestTextLookupFallsBackThenKeyPath(t *testing.T) {
	b := loadFixture(t)

	cases := []struct {
		lang, key, want string
	}{
		{"en", "greet", "Hello"},
		{"xx", "greet", "Salam"},
		{"xx", "app.name", "Fikstür"},
		{"xx", "fallbackOnly", "english-only string"}, // missing in xx → English
		{"en", "no.such.key", "no.such.key"},          // missing everywhere → the path itself
		{"xx", "app.items", "{{count}} items"},        // fallback plural without count renders "other"
	}
	for _, tc := range cases {
		if got := b.Text(tc.lang, tc.key, nil); got != tc.want {
			t.Errorf("Text(%q, %q) = %q, want %q", tc.lang, tc.key, got, tc.want)
		}
	}
}

func TestPluralSelectionAndCountFormatting(t *testing.T) {
	b := loadFixture(t)

	if got := b.Text("en", "app.items", map[string]any{"count": 1}); got != "1 item" {
		t.Errorf("count 1 → %q, want %q", got, "1 item")
	}
	if got := b.Text("en", "app.items", map[string]any{"count": 1234}); got != "1,234 items" {
		t.Errorf("count 1234 → %q, want %q (locale-formatted)", got, "1,234 items")
	}
	if got := b.Text("xx", "app.items", map[string]any{"count": 2}); got != "2 items" {
		t.Errorf("xx has no items key → English fallback, got %q", got)
	}
}

func TestPlainVariablesGoInVerbatim(t *testing.T) {
	b := loadFixture(t)

	got := b.Text("en", "app.label", map[string]any{"name": "web-shop"})
	if got != "Project web-shop" {
		t.Fatalf("Text() = %q, want %q", got, "Project web-shop")
	}
	// An unsupplied placeholder stays visible rather than vanishing.
	got = b.Text("en", "app.label", nil)
	if got != "Project {{name}}" {
		t.Fatalf("Text() without vars = %q, want the placeholder left in place", got)
	}
}

func TestHasReportsOwnDefinitionOnly(t *testing.T) {
	b := loadFixture(t)

	if !b.Has("xx", "greet") {
		t.Error("xx defines greet")
	}
	if b.Has("xx", "fallbackOnly") {
		t.Error("fallbackOnly is English-only; Has must not fall back")
	}
}

func TestMatchExactThenBaseLanguageThenFallback(t *testing.T) {
	b := loadFixture(t)

	cases := []struct{ preferred, want string }{
		{"xx", "xx"},
		{"XX", "xx"},
		{"xx-ZZ", "xx"},         // same base language
		{"pt-BR", FallbackCode}, // nothing close ships
		{"", FallbackCode},
	}
	for _, tc := range cases {
		if got := b.Match(tc.preferred); got != tc.want {
			t.Errorf("Match(%q) = %q, want %q", tc.preferred, got, tc.want)
		}
	}
}

func TestGlobalTextWithoutBundleReturnsKey(t *testing.T) {
	previous := Global()
	SetGlobal(nil)
	t.Cleanup(func() {
		if previous != nil {
			SetGlobal(previous)
		}
	})

	if got := Text("some.key", nil); got != "some.key" {
		t.Fatalf("Text() = %q, want the bare key before any bundle is installed", got)
	}
}

func TestGlobalRenderingAndLocalizedErrors(t *testing.T) {
	b := loadFixture(t)
	previous, previousActive := Global(), Active()
	SetGlobal(b)
	SetActive("xx")
	t.Cleanup(func() {
		SetActive(previousActive)
		SetGlobal(previous)
	})

	if got := Text("greet", nil); got != "Salam" {
		t.Fatalf("Text() = %q, want the active language's rendering", got)
	}

	plain := Fail("greet", nil)
	if plain.Error() != "Salam" {
		t.Fatalf("Fail().Error() = %q, want %q", plain.Error(), "Salam")
	}

	cause := errors.New("disk on fire")
	wrapped := FailWrap(cause, "app.name", nil)
	if wrapped.Error() != "Fikstür: disk on fire" {
		t.Fatalf("FailWrap().Error() = %q, want localized text plus the cause", wrapped.Error())
	}
	if !errors.Is(wrapped, cause) {
		t.Error("errors.Is must see through the localization wrapper")
	}
	if FailWrap(nil, "greet", nil) != nil {
		t.Error("wrapping nil must stay nil")
	}
}
