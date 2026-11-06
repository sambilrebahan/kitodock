package clipboard

import (
	"runtime"
	"strings"
	"testing"
)

func TestWriteThenReadRoundTrip(t *testing.T) {
	if runtime.GOOS != "windows" {
		if _, err := ReadText(); err != ErrUnavailable {
			t.Fatalf("ReadText() off Windows = %v, want ErrUnavailable", err)
		}
		return
	}

	sample := "jalan raya nomor 7 — café ☕"
	if err := WriteText(sample); err != nil {
		t.Fatal(err)
	}
	got, err := ReadText()
	if err != nil {
		t.Fatal(err)
	}
	if got != sample {
		t.Fatalf("ReadText() = %q, want %q", got, sample)
	}
}

func TestWriteEmptyClears(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("native clipboard needs Windows")
	}
	if err := WriteText(""); err != nil {
		t.Fatal(err)
	}
	got, err := ReadText()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(got) != "" {
		t.Fatalf("clipboard still holds %q after writing empty text", got)
	}
}

func TestLongTextSurvives(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("native clipboard needs Windows")
	}
	long := strings.Repeat("kitodock ", 10_000)
	if err := WriteText(long); err != nil {
		t.Fatal(err)
	}
	got, err := ReadText()
	if err != nil {
		t.Fatal(err)
	}
	if got != long {
		t.Fatalf("round trip lost data: got %d bytes, want %d", len(got), len(long))
	}
}
