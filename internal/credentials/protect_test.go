package credentials

import (
	"runtime"
	"strings"
	"testing"
)

func TestSealUnsealRoundTrip(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("DPAPI round-trip needs Windows")
	}
	s := System{}

	sealed, err := s.Seal("s3cret-passphrase")
	if err != nil {
		t.Fatal(err)
	}
	if sealed == "s3cret-passphrase" || !strings.HasPrefix(sealed, "kddp1:") {
		t.Fatalf("Seal() = %q, want an opaque kddp1:-prefixed value", sealed)
	}

	plain, err := s.Unseal(sealed)
	if err != nil {
		t.Fatal(err)
	}
	if plain != "s3cret-passphrase" {
		t.Fatalf("Unseal() = %q, want the original secret back", plain)
	}
}

func TestEmptyValuesStayEmpty(t *testing.T) {
	s := System{}

	sealed, err := s.Seal("")
	if err != nil || sealed != "" {
		t.Fatalf(`Seal("") = (%q, %v), want ("", nil)`, sealed, err)
	}
	plain, err := s.Unseal("")
	if err != nil || plain != "" {
		t.Fatalf(`Unseal("") = (%q, %v), want ("", nil)`, plain, err)
	}
}

func TestUnsealRejectsForeignValues(t *testing.T) {
	s := System{}

	for _, bad := range []string{"plaintext", "kddp1:", "kddp1:!!!not-base64!!!", "dpapi:v1:older-format"} {
		if _, err := s.Unseal(bad); err == nil {
			t.Errorf("Unseal(%q) succeeded, want a rejection", bad)
		}
	}
}
