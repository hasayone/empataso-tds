package ktimport

import (
	"reflect"
	"testing"
)

func TestExtractGlobalPostbackNone(t *testing.T) {
	groups := []Group{
		{ID: "a", Name: "A", Streams: []Stream{{Name: "s"}}},
		{ID: "b", Name: "B"},
	}
	got, out := ExtractGlobalPostback(groups, nil)
	if got != nil {
		t.Fatalf("postback = %+v", got)
	}
	if !reflect.DeepEqual(out, groups) {
		t.Fatalf("groups = %+v", out)
	}
	got, out = ExtractGlobalPostback(nil, nil)
	if got != nil || out != nil {
		t.Fatalf("nil input = %v %+v", got, out)
	}
}

func TestExtractGlobalPostbackOneTemplate(t *testing.T) {
	clean := Postback{URL: "https://pb.example/hook", Method: "POST", Statuses: []string{"lead", "sale"}}
	sig := clean.Signature()
	id := PostbackID(sig)
	variants := []Postback{
		{URL: " https://pb.example/hook ", Method: "post", Statuses: []string{" Sale ", "lead", ""}},
		{URL: "https://pb.example/hook", Method: "POST", Statuses: []string{"sale", "lead"}},
		{URL: "https://pb.example/hook", Method: "Post", Statuses: []string{"lead", "sale"}},
	}
	groups := []Group{
		{ID: "c1", Postbacks: []Postback{variants[0]}, PostbackIDs: []string{id, "old"}},
		{ID: "c2", Postbacks: []Postback{variants[1]}, PostbackIDs: []string{"old"}},
		{ID: "c3", Postbacks: []Postback{variants[2]}},
	}
	got, out := ExtractGlobalPostback(groups, nil)
	want := Postback{
		ID: id, Name: "Keitaro pb.example",
		URL: "https://pb.example/hook", Method: "POST", Statuses: []string{"lead", "sale"},
	}
	if !reflect.DeepEqual(got, &want) {
		t.Fatalf("postback = %+v", got)
	}
	wantIDs := [][]string{{id, "old"}, {"old", id}, {id}}
	for i, g := range out {
		if g.Postbacks != nil || !reflect.DeepEqual(g.PostbackIDs, wantIDs[i]) {
			t.Fatalf("group %s = %+v", g.ID, g)
		}
	}
}

func TestExtractGlobalPostbackFrequency(t *testing.T) {
	a := Postback{URL: "https://a.example/pb", Method: "GET", Statuses: []string{"lead"}}
	b := Postback{URL: "https://b.example/pb", Method: "get", Statuses: []string{"Sale"}}
	c := Postback{URL: "https://c.example/pb", Method: "POST", Statuses: []string{"rejected"}}
	groups := []Group{
		{ID: "1", Postbacks: []Postback{a, a}},
		{ID: "2", Postbacks: []Postback{a}},
		{ID: "3", Postbacks: []Postback{a}},
		{ID: "4", Postbacks: []Postback{b}},
		{ID: "5", Postbacks: []Postback{b}},
		{ID: "6", Postbacks: []Postback{c}},
	}
	got, out := ExtractGlobalPostback(groups, nil)
	id := PostbackID(a.Signature())
	if got == nil || got.ID != id || got.Name != "Keitaro a.example" {
		t.Fatalf("postback = %+v", got)
	}
	for i := 0; i < 3; i++ {
		if out[i].Postbacks != nil || !reflect.DeepEqual(out[i].PostbackIDs, []string{id}) {
			t.Fatalf("winner group = %+v", out[i])
		}
	}
	if !reflect.DeepEqual(out[3].Postbacks, []Postback{b}) || out[3].PostbackIDs != nil {
		t.Fatalf("kept b = %+v", out[3])
	}
	if !reflect.DeepEqual(out[4].Postbacks, []Postback{b}) {
		t.Fatalf("kept b2 = %+v", out[4])
	}
	if !reflect.DeepEqual(out[5].Postbacks, []Postback{c}) || out[5].PostbackIDs != nil {
		t.Fatalf("kept c = %+v", out[5])
	}
}

func TestExtractGlobalPostbackTie(t *testing.T) {
	a := Postback{URL: "https://a.example/pb", Method: "GET", Statuses: []string{"lead"}}
	b := Postback{URL: "https://b.example/pb", Method: "GET", Statuses: []string{"lead"}}
	sigA, sigB := a.Signature(), b.Signature()
	if sigA == sigB {
		t.Fatal("signatures equal")
	}
	wantSig := sigA
	want := a
	if sigB < sigA {
		wantSig = sigB
		want = b
	}
	orders := [][]Group{
		{{ID: "1", Postbacks: []Postback{b}}, {ID: "2", Postbacks: []Postback{b}}, {ID: "3", Postbacks: []Postback{a}}, {ID: "4", Postbacks: []Postback{a}}},
		{{ID: "1", Postbacks: []Postback{a}}, {ID: "2", Postbacks: []Postback{a}}, {ID: "3", Postbacks: []Postback{b}}, {ID: "4", Postbacks: []Postback{b}}},
	}
	id := PostbackID(wantSig)
	for _, groups := range orders {
		got, out := ExtractGlobalPostback(groups, nil)
		if got == nil || got.ID != id || got.URL != want.URL {
			t.Fatalf("winner = %+v", got)
		}
		for i, g := range groups {
			if g.Postbacks[0].Signature() == wantSig {
				if out[i].Postbacks != nil || !reflect.DeepEqual(out[i].PostbackIDs, []string{id}) {
					t.Fatalf("bound = %+v", out[i])
				}
				continue
			}
			if !reflect.DeepEqual(out[i].Postbacks, g.Postbacks) || out[i].PostbackIDs != nil {
				t.Fatalf("inline = %+v", out[i])
			}
		}
	}
}

func TestExtractGlobalPostbackExisting(t *testing.T) {
	inline := Postback{URL: "https://pb.example/hook", Method: "POST", Statuses: []string{"sale"}}
	prev := Postback{ID: "pb-saved", Name: "Archive", URL: "https://pb.example/hook", Method: "post", Statuses: []string{"Sale"}}
	other := Postback{ID: "pb-other", Name: "Other", URL: "https://other.example/pb", Method: "GET", Statuses: []string{"lead"}}
	if inline.Signature() != prev.Signature() {
		t.Fatal("fixture signatures differ")
	}
	groups := []Group{{ID: "c1", Name: "C", Postbacks: []Postback{inline}}}
	got, out := ExtractGlobalPostback(groups, []Postback{other, prev})
	if got == nil || !reflect.DeepEqual(*got, prev) {
		t.Fatalf("postback = %+v", got)
	}
	if out[0].Postbacks != nil || !reflect.DeepEqual(out[0].PostbackIDs, []string{prev.ID}) || out[0].Name != "C" {
		t.Fatalf("group = %+v", out[0])
	}
}

func TestSignatureCaseAndStatusOrder(t *testing.T) {
	a := Postback{Method: "post", URL: " https://pb.example/hook ", Statuses: []string{"Sale", " lead ", ""}}
	b := Postback{Method: "POST", URL: "https://pb.example/hook", Statuses: []string{"lead", "sale"}}
	empty := Postback{URL: "https://pb.example/hook", Statuses: []string{"sale", "lead"}}
	explicit := Postback{Method: "GET", URL: "https://pb.example/hook", Statuses: []string{"lead", "sale"}}
	if a.Signature() != b.Signature() || empty.Signature() != explicit.Signature() {
		t.Fatalf("sig %q %q %q", a.Signature(), b.Signature(), empty.Signature())
	}
}

func TestSignatureDiffersByStatus(t *testing.T) {
	lead := Postback{URL: "https://a.example/pb", Method: "GET", Statuses: []string{"lead"}}
	sale := Postback{URL: "https://a.example/pb", Method: "GET", Statuses: []string{"sale"}}
	if lead.Signature() == sale.Signature() {
		t.Fatalf("sig %q", lead.Signature())
	}
}

func TestExtractGlobalPostbackCountsOnce(t *testing.T) {
	small := Postback{URL: "https://a.example/pb", Method: "GET", Statuses: []string{"lead"}}
	big := Postback{URL: "https://z.example/pb", Method: "GET", Statuses: []string{"lead"}}
	if small.Signature() >= big.Signature() {
		t.Fatal("fixture order")
	}
	groups := []Group{
		{ID: "1", Postbacks: []Postback{small, small}},
		{ID: "2", Postbacks: []Postback{big}},
		{ID: "3", Postbacks: []Postback{big}},
	}
	got, out := ExtractGlobalPostback(groups, nil)
	id := PostbackID(big.Signature())
	if got == nil || got.ID != id {
		t.Fatalf("postback = %+v", got)
	}
	if !reflect.DeepEqual(out[0].Postbacks, []Postback{small, small}) {
		t.Fatalf("small stayed = %+v", out[0])
	}
	if out[1].Postbacks != nil || !reflect.DeepEqual(out[1].PostbackIDs, []string{id}) {
		t.Fatalf("big = %+v", out[1])
	}
}

func TestExtractGlobalPostbackInputUntouched(t *testing.T) {
	groups := []Group{
		{ID: "a", Aliases: []string{"x"}, PostbackIDs: []string{"old"}, Postbacks: []Postback{{URL: "https://a.example/pb", Method: "GET", Statuses: []string{"lead", "sale"}}}},
		{ID: "b", Postbacks: []Postback{{URL: "https://b.example/pb", Method: "POST", Statuses: []string{"rejected"}}}},
	}
	snap := snapshotGroups(groups)
	ExtractGlobalPostback(groups, []Postback{{ID: "z", URL: "https://a.example/pb", Method: "GET", Statuses: []string{"sale", "lead"}}})
	if !reflect.DeepEqual(groups, snap) {
		t.Fatalf("mutated %+v", groups)
	}
}

// snapshotGroups копирует группы и вложенные срезы до вызова.
//
// Параметры: groups — вход теста.
// Результат: независимая копия. nil остаётся nil.
// Ошибки: нет.
func snapshotGroups(groups []Group) []Group {
	if groups == nil {
		return nil
	}
	out := make([]Group, len(groups))
	for i, g := range groups {
		out[i] = g
		if g.Aliases != nil {
			out[i].Aliases = append([]string(nil), g.Aliases...)
		}
		if g.PostbackIDs != nil {
			out[i].PostbackIDs = append([]string(nil), g.PostbackIDs...)
		}
		if g.Postbacks == nil {
			continue
		}
		out[i].Postbacks = make([]Postback, len(g.Postbacks))
		for j, p := range g.Postbacks {
			out[i].Postbacks[j] = p
			if p.Statuses != nil {
				out[i].Postbacks[j].Statuses = append([]string(nil), p.Statuses...)
			}
		}
	}
	return out
}
