package ktimport

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestMapCampaignNotes проверяет, что заметка кампании Keitaro попадает в группу без краевых пробелов
// и обрезается до MaxNotesRunes символов, а пустая остаётся пустой.
func TestMapCampaignNotes(t *testing.T) {
	long := strings.Repeat("я", MaxNotesRunes+5)
	snap := Snapshot{Campaigns: []Campaign{
		{ID: 1, Alias: "a", Name: "A", State: "active", Notes: "  call before launch \n"},
		{ID: 2, Alias: "b", Name: "B", State: "active"},
		{ID: 3, Alias: "c", Name: "C", State: "active", Notes: long},
	}}
	res := MapAll(snap)
	notes := map[string]string{}
	for _, g := range res.Groups {
		notes[g.ID] = g.Notes
	}
	if notes["a"] != "call before launch" || notes["b"] != "" || utf8.RuneCountInString(notes["c"]) != MaxNotesRunes {
		t.Fatalf("notes = %q %q %d", notes["a"], notes["b"], utf8.RuneCountInString(notes["c"]))
	}
}

// TestNoteSkipped проверяет чтение имён пропущенных фильтров из заметки потока.
func TestNoteSkipped(t *testing.T) {
	if got := NoteSkipped(StreamNote(5, []string{"a", "b"})); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("skipped = %v", got)
	}
	if got := NoteSkipped(StreamNote(5, nil)); got != nil {
		t.Fatalf("skipped = %v", got)
	}
	if got := NoteSkipped("keitaro_stream_id=5; my text"); got != nil {
		t.Fatalf("skipped = %v", got)
	}
}
