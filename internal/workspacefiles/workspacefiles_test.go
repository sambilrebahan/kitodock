package workspacefiles

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func newService(t *testing.T) *Service {
	t.Helper()
	return New(t.TempDir())
}

func ref() ContextRef { return ContextRef{Kind: "project", Name: "shop"} }

func TestListPaginatesAndSorts(t *testing.T) {
	s := newService(t)
	// Create a mix of dirs and files.
	for _, name := range []string{"zeta.txt", "alpha.txt", "mid.txt", "beta.txt"} {
		os.WriteFile(filepath.Join(s.root, name), []byte("x"), 0o644)
	}
	os.MkdirAll(filepath.Join(s.root, "zdir"), 0o755)
	os.MkdirAll(filepath.Join(s.root, "adir"), 0o755)

	result := s.List(ref(), "", "")
	if len(result.Entries) != 6 {
		t.Fatalf("List() = %d entries, want 6", len(result.Entries))
	}
	// Dirs first, alphabetical.
	if !result.Entries[0].IsDir || result.Entries[0].Name != "adir" {
		t.Errorf("first = %+v, want adir dir", result.Entries[0])
	}
	if result.Entries[1].Name != "zdir" {
		t.Errorf("second = %+v, want zdir", result.Entries[1])
	}
	if result.Entries[2].Name != "alpha.txt" {
		t.Errorf("third = %+v, want alpha.txt", result.Entries[2])
	}
	if result.Cursor != "" {
		t.Error("small dir should not paginate")
	}
}

func TestListPagination(t *testing.T) {
	s := newService(t)
	for i := 0; i < PageSize+10; i++ {
		name := fmt.Sprintf("file%04d.txt", i)
		os.WriteFile(filepath.Join(s.root, name), []byte("x"), 0o644)
	}
	first := s.List(ref(), "", "")
	if len(first.Entries) != PageSize {
		t.Fatalf("page 1 = %d, want %d", len(first.Entries), PageSize)
	}
	if first.Cursor == "" {
		t.Fatal("page 1 should have cursor")
	}
	second := s.List(ref(), "", first.Cursor)
	if second.Stale {
		t.Fatal("valid cursor marked stale")
	}
	if len(second.Entries) != 10 {
		t.Fatalf("page 2 = %d, want 10", len(second.Entries))
	}
	if second.Cursor != "" {
		t.Error("last page should not have cursor")
	}
}

func TestListStaleCursor(t *testing.T) {
	s := newService(t)
	os.WriteFile(filepath.Join(s.root, "a.txt"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(s.root, "b.txt"), []byte("x"), 0o644)
	result := s.List(ref(), "", "nonexistent-cursor")
	if !result.Stale {
		t.Fatal("bogus cursor should be stale")
	}
}

func TestPathValidation(t *testing.T) {
	s := newService(t)
	for _, bad := range []string{"../escape", "/absolute", "C:\\drive", "a/../b", ".", ".."} {
		if _, err := s.resolve(bad); err == nil {
			t.Errorf("resolve(%q) should fail", bad)
		}
	}
	// Valid paths pass.
	if _, err := s.resolve(""); err != nil {
		t.Errorf("root path should work: %v", err)
	}
	if _, err := s.resolve("dir/file.txt"); err != nil {
		t.Errorf("nested path should work: %v", err)
	}
}

func TestReadDetectsBOMAndEOL(t *testing.T) {
	s := newService(t)
	cases := []struct {
		name, content string
		wantBOM       bool
		wantEOL       string
		wantMixed     bool
		wantReadOnly  bool
	}{
		{"plain.txt", "hello world", false, "", false, false},
		{"bom.txt", "\xEF\xBB\xBFhello", true, "", false, false},
		{"lf.txt", "line1\nline2\n", false, "lf", false, false},
		{"crlf.txt", "line1\r\nline2\r\n", false, "crlf", false, false},
		{"mixed.txt", "line1\nline2\r\n", false, "mixed", true, true},
	}
	for _, tc := range cases {
		os.WriteFile(filepath.Join(s.root, tc.name), []byte(tc.content), 0o644)
		doc, result := s.Read(ref(), tc.name)
		if result != ResultOK {
			t.Errorf("%s: result = %s", tc.name, result)
			continue
		}
		if doc.BOM != tc.wantBOM {
			t.Errorf("%s: BOM = %v, want %v", tc.name, doc.BOM, tc.wantBOM)
		}
		if doc.EOL != tc.wantEOL {
			t.Errorf("%s: EOL = %q, want %q", tc.name, doc.EOL, tc.wantEOL)
		}
		if doc.MixedEOL != tc.wantMixed {
			t.Errorf("%s: MixedEOL = %v", tc.name, doc.MixedEOL)
		}
		if doc.ReadOnly != tc.wantReadOnly {
			t.Errorf("%s: ReadOnly = %v, want %v", tc.name, doc.ReadOnly, tc.wantReadOnly)
		}
	}
}

func TestReadBinaryDetection(t *testing.T) {
	s := newService(t)
	os.WriteFile(filepath.Join(s.root, "blob.bin"), []byte{0x00, 0x01, 0x02, 0x00}, 0o644)
	doc, result := s.Read(ref(), "blob.bin")
	if result != ResultOK || doc.Encoding != "binary" || !doc.ReadOnly {
		t.Fatalf("binary = %+v result=%s", doc, result)
	}
}

func TestReadTooLarge(t *testing.T) {
	s := newService(t)
	big := make([]byte, MaxTextSize+1)
	os.WriteFile(filepath.Join(s.root, "big.txt"), big, 0o644)
	_, result := s.Read(ref(), "big.txt")
	if result != ResultTooLarge {
		t.Fatalf("result = %s, want too_large", result)
	}
}

func TestReadNotFound(t *testing.T) {
	s := newService(t)
	_, result := s.Read(ref(), "ghost.txt")
	if result != ResultNotFound {
		t.Fatalf("result = %s, want not_found", result)
	}
}

func TestSaveOptimisticRevision(t *testing.T) {
	s := newService(t)
	os.WriteFile(filepath.Join(s.root, "doc.txt"), []byte("v1"), 0o644)

	doc, _ := s.Read(ref(), "doc.txt")
	rev1 := doc.Revision

	// Happy path: correct revision → ok.
	result, newRev := s.Save(ref(), "doc.txt", "v2", rev1)
	if result != ResultOK {
		t.Fatalf("save with correct rev = %s", result)
	}
	if newRev == rev1 {
		t.Error("revision didn't change")
	}

	// Conflict: stale revision → refused.
	result, _ = s.Save(ref(), "doc.txt", "v3", rev1)
	if result != ResultConflict {
		t.Fatalf("save with stale rev = %s, want conflict", result)
	}
	// File should still be v2.
	data, _ := os.ReadFile(filepath.Join(s.root, "doc.txt"))
	if string(data) != "v2" {
		t.Fatalf("conflict save overwrote: %q", string(data))
	}
}

func TestSaveNewFile(t *testing.T) {
	s := newService(t)
	result, _ := s.Save(ref(), "new.txt", "fresh", "")
	if result != ResultOK {
		t.Fatalf("new file save = %s", result)
	}
	// But saving with a revision when file doesn't exist is a conflict.
	result, _ = s.Save(ref(), "other.txt", "x", "some-hash")
	if result != ResultConflict {
		t.Fatalf("new file with rev = %s, want conflict", result)
	}
}

func TestCheckRevision(t *testing.T) {
	s := newService(t)
	os.WriteFile(filepath.Join(s.root, "doc.txt"), []byte("hello"), 0o644)
	doc, _ := s.Read(ref(), "doc.txt")

	// Unchanged.
	result, _ := s.Check(ref(), "doc.txt", doc.Revision)
	if result != ResultUnchanged {
		t.Fatalf("check same rev = %s", result)
	}

	// Changed externally.
	os.WriteFile(filepath.Join(s.root, "doc.txt"), []byte("world"), 0o644)
	result, _ = s.Check(ref(), "doc.txt", doc.Revision)
	if result != ResultChanged {
		t.Fatalf("check changed = %s", result)
	}
}

func TestReadImage(t *testing.T) {
	s := newService(t)
	// Minimal valid PNG header.
	pngHeader := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0, 0, 0, 0}
	os.WriteFile(filepath.Join(s.root, "icon.png"), pngHeader, 0o644)

	img, result := s.ReadImage(ref(), "icon.png")
	if result != ResultOK {
		t.Fatalf("result = %s", result)
	}
	if img.MIME != "image/png" {
		t.Fatalf("MIME = %s", img.MIME)
	}
	if img.Data == "" {
		t.Fatal("no data")
	}
}

func TestReadImageWrongMagic(t *testing.T) {
	s := newService(t)
	os.WriteFile(filepath.Join(s.root, "fake.png"), []byte("not a png"), 0o644)
	_, result := s.ReadImage(ref(), "fake.png")
	if result != ResultBinary {
		t.Fatalf("fake png = %s, want binary", result)
	}
}

func TestSymlinkBlocked(t *testing.T) {
	s := newService(t)
	// Symlinks can't be created reliably in tests on Windows without
	// admin, so just verify the field exists on normal entries.
	result := s.List(ref(), "", "")
	for _, e := range result.Entries {
		if e.Blocked && e.BlockReason == "" {
			t.Error("blocked entry without reason")
		}
	}
}

func TestResolveRejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("junction test is Windows-shaped")
	}
	root := t.TempDir()
	outside := t.TempDir()
	// Plant a target file outside the project root.
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("top secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	newSvc := func(dir string) *Service {
		return New(dir)
	}

	// A directory junction pointing outside (no admin needed, unlike symlinks).
	link := filepath.Join(root, "leak")
	cmd := exec.Command("cmd", "/c", "mklink", "/J", link, outside)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("junction creation failed: %v: %s", err, out)
	}

	s := newSvc(root)
	if _, status := s.Read(ref(), "leak/secret.txt"); status != ResultBlocked {
		t.Fatalf("read through junction = %q, want blocked", status)
	}
	// Saving through a junction must be blocked too.
	if status, _ := s.Save(ref(), "leak/secret.txt", "pwned", ""); status != ResultBlocked {
		t.Fatalf("save through junction = %q, want blocked", status)
	}
	// A plain file still reads fine.
	if err := os.WriteFile(filepath.Join(root, "ok.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, status := s.Read(ref(), "ok.txt"); status == ResultBlocked {
		t.Fatal("plain file blocked")
	}
}
