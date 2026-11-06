package paths

import "testing"

func TestIsSameRootIgnoresCaseAndSeparators(t *testing.T) {
	same := [][2]string{
		{`C:\kitodock`, `C:\Kitodock`},
		{`C:\kitodock`, `c:/kitodock`},
		{`C:\kitodock\`, `C:\kitodock`},
		{`D:\dev env`, `d:\DEV ENV`},
	}
	for _, pair := range same {
		if !IsSameRoot(pair[0], pair[1]) {
			t.Errorf("IsSameRoot(%q, %q) = false, want true", pair[0], pair[1])
		}
	}
	if IsSameRoot(`C:\kitodock`, `D:\kitodock`) {
		t.Error("different drives are different roots")
	}
}

func TestMovePathRebasesOnlyPathsUnderTheOldRoot(t *testing.T) {
	cases := []struct {
		name    string
		oldRoot string
		newRoot string
		path    string
		want    string
		moved   bool
	}{
		{"direct child", `E:\old`, `F:\new`, `E:\old\www\app`, `F:\new\www\app`, true},
		{"the root itself", `E:\old`, `F:\new`, `E:\old`, `F:\new`, true},
		{"separators vary", `E:/old`, `F:\new`, `E:\old\x`, `F:\new\x`, true},
		{"case varies", `E:\Old`, `F:\new`, `e:\old\x`, `f:\new\x`, true},
		{"outside survives", `E:\old`, `F:\new`, `D:\mine\x`, `D:\mine\x`, false},
		{"sibling prefix is not the root", `E:\old`, `F:\new`, `E:\old-backup\x`, `E:\old-backup\x`, false},
		{"empty stays empty", `E:\old`, `F:\new`, ``, ``, false},
	}
	for _, tc := range cases {
		got, moved := MovePath(tc.oldRoot, tc.newRoot, tc.path)
		if got != tc.want || moved != tc.moved {
			t.Errorf("%s: MovePath(%q, %q, %q) = (%q, %v), want (%q, %v)",
				tc.name, tc.oldRoot, tc.newRoot, tc.path, got, moved, tc.want, tc.moved)
		}
	}
}

// Lowercase stored keys must stay lowercase after a move, or every lookup
// indexed by them breaks silently.
func TestMovePathKeepsLowercaseKeysLowercase(t *testing.T) {
	got, moved := MovePath(`C:\kitodock`, `C:\Kitodock`, `c:\kitodock\www\shop`)

	if !moved || got != `c:\kitodock\www\shop` {
		t.Fatalf("MovePath() = (%q, %v), want the lowercase prefix preserved", got, moved)
	}
}
