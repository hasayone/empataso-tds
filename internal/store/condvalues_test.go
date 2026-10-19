package store

import "testing"

func TestSplitCondListLegacy(t *testing.T) {
	got := SplitCondList("ru,us")
	if len(got) != 2 || got[0] != "ru" || got[1] != "us" {
		t.Fatalf("got %v", got)
	}
}

func TestSplitCondListEscapedComma(t *testing.T) {
	got := SplitCondList(`a\,b,c`)
	if len(got) != 2 || got[0] != "a,b" || got[1] != "c" {
		t.Fatalf("got %v", got)
	}
}

func TestSplitCondListBackslashPair(t *testing.T) {
	got := SplitCondList(`x\\,y`)
	if len(got) != 2 || got[0] != `x\` || got[1] != "y" {
		t.Fatalf("got %v", got)
	}
}

func TestSplitCondListWindowsPath(t *testing.T) {
	got := SplitCondList(`C:\tmp`)
	if len(got) != 1 || got[0] != `C:\tmp` {
		t.Fatalf("got %v", got)
	}
}

func TestExpandCondValuesIn(t *testing.T) {
	got := ExpandCondValues("in", []string{"ru,us", "de"})
	want := []string{"ru", "us", "de"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestExpandCondValuesEqRaw(t *testing.T) {
	got := ExpandCondValues("eq", []string{`Foo\, Bar`, `C:\tmp`})
	if len(got) != 2 || got[0] != `Foo\, Bar` || got[1] != `C:\tmp` {
		t.Fatalf("got %v", got)
	}
}
