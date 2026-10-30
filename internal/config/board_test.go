package config

import "testing"

func TestNormalizeBoardID(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", MainBoardID},
		{"main", MainBoardID},
		{"MAIN", MainBoardID},
		{" sales ", "sales"},
	}
	for _, c := range cases {
		if got := NormalizeBoardID(c.in); got != c.want {
			t.Fatalf("NormalizeBoardID(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

func TestSameBoard(t *testing.T) {
	if !SameBoard("", "main") || !SameBoard("main", "MAIN") {
		t.Fatal("empty and main should match")
	}
	if SameBoard("sales", "other") {
		t.Fatal("different ids should not match")
	}
}

func TestBoardIconValidate(t *testing.T) {
	if err := ValidateBoardIcon(""); err != nil {
		t.Fatalf("empty icon: %v", err)
	}
	if err := ValidateBoardIcon("flows"); err != nil {
		t.Fatalf("flows: %v", err)
	}
	if err := ValidateBoardIcon("nope"); err != ErrInvalidBoardIcon {
		t.Fatalf("nope: %v", err)
	}
	if len(allowedBoardIcons) < 200 {
		t.Fatalf("icons = %d; want at least 200", len(allowedBoardIcons))
	}
	if err := ValidateBoardIcon("rocket"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"android", "ios", "seo"} {
		if err := ValidateBoardIcon(name); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if err := ValidateBoardIconColor(""); err != nil {
		t.Fatalf("empty color: %v", err)
	}
	if err := ValidateBoardIconColor("clicks"); err != nil {
		t.Fatalf("clicks: %v", err)
	}
	if err := ValidateBoardIconColor("nope"); err != ErrInvalidBoardIcon {
		t.Fatalf("bad color: %v", err)
	}
	if len(allowedBoardIconColors) != 10 {
		t.Fatalf("colors = %d; want 10", len(allowedBoardIconColors))
	}
}
