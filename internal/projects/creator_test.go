package projects

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizeName(t *testing.T) {
	cases := map[string]string{
		"My Shop": "myshop",
		"shop-2":  "shop-2",
		"Shop_v2": "shop_v2",
		"café":    "café", // letters survive
		"a/b\\c":  "abc",
		"":        "",
		"  ":      "",
	}
	for in, want := range cases {
		if got := sanitizeName(in); got != want {
			t.Errorf("sanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCreateBlank(t *testing.T) {
	dir := t.TempDir()
	creator := &Creator{}
	var lines []string
	err := creator.Create(context.Background(), CreateOptions{
		Name: "My Site", Kind: CreateBlank, Dir: dir,
	}, func(line string) { lines = append(lines, line) })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "mysite")); err != nil {
		t.Fatal("blank project not created")
	}
	if len(lines) == 0 {
		t.Error("no output streamed")
	}
}

func TestCreateRefusesExistingFolder(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "shop"), 0o755)
	creator := &Creator{}
	if err := creator.Create(context.Background(), CreateOptions{
		Name: "shop", Kind: CreateBlank, Dir: dir,
	}, func(string) {}); err == nil {
		t.Fatal("existing folder accepted")
	}
}

func TestCreateRefusesEmptyName(t *testing.T) {
	creator := &Creator{}
	if err := creator.Create(context.Background(), CreateOptions{
		Name: "  ", Kind: CreateBlank, Dir: t.TempDir(),
	}, func(string) {}); err == nil {
		t.Fatal("blank name accepted")
	}
}

func TestLaravelWithoutComposerFailsClearly(t *testing.T) {
	creator := &Creator{PhpExeFor: func() string { return "php" }, ComposerFor: func() string { return "" }}
	if err := creator.Create(context.Background(), CreateOptions{
		Name: "x", Kind: CreateLaravel, Dir: t.TempDir(),
	}, func(string) {}); err == nil || !strings.Contains(err.Error(), "composer") {
		t.Fatalf("want composer error, got %v", err)
	}
}

func TestGitWithoutURLFailsClearly(t *testing.T) {
	creator := &Creator{}
	if err := creator.Create(context.Background(), CreateOptions{
		Name: "x", Kind: CreateGit, Dir: t.TempDir(),
	}, func(string) {}); err == nil || !strings.Contains(err.Error(), "URL") {
		t.Fatalf("want URL error, got %v", err)
	}
}

func TestStripPailFromDevScript(t *testing.T) {
	dir := t.TempDir()
	composerJSON := `{"scripts":{"dev":"php artisan serve --host=0.0.0.0 && php artisan pail && npm run dev"}}`
	os.WriteFile(filepath.Join(dir, "composer.json"), []byte(composerJSON), 0o644)

	if err := StripPailFromDevScript(dir); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "composer.json"))
	text := string(data)
	if strings.Contains(text, "pail") {
		t.Fatalf("pail survived: %s", text)
	}
	if !strings.Contains(text, "artisan serve") {
		t.Fatalf("other commands lost: %s", text)
	}
	if !strings.Contains(text, "npm run dev") {
		t.Fatalf("npm run dev lost: %s", text)
	}

	// Idempotent.
	if err := StripPailFromDevScript(dir); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "composer.json"))
	if string(after) != text {
		t.Fatal("second strip changed the file")
	}
}

func TestStripPailWithoutComposerJSON(t *testing.T) {
	dir := t.TempDir()
	if err := StripPailFromDevScript(dir); err != nil {
		t.Fatalf("no composer.json should be a no-op: %v", err)
	}
}

func TestAllCreateKindsAreUnique(t *testing.T) {
	seen := map[CreateKind]bool{}
	for _, kind := range AllCreateKinds {
		if seen[kind] {
			t.Errorf("duplicate kind %q", kind)
		}
		seen[kind] = true
	}
	if len(AllCreateKinds) != 8 {
		t.Errorf("want 8 kinds, got %d", len(AllCreateKinds))
	}
}
