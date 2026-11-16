package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/egerkuzma/emtds/internal/store"
)

// TestParseCondsSubstring keeps contains values whole and groups repeats.
func TestParseCondsSubstring(t *testing.T) {
	q := url.Values{}
	q.Add("f", "sub_id_1:contains:ab,c")
	q.Add("f", "sub_id_1:contains:x:y")
	q.Add("f", "referer:not_contains:spam")
	conds, err := parseConds(q)
	if err != nil {
		t.Fatal(err)
	}
	if len(conds) != 2 || conds[0].Op != store.OpContains || strings.Join(conds[0].Values, "|") != "ab,c|x:y" {
		t.Fatalf("contains %+v", conds)
	}
	if conds[1].Field != "referer" || conds[1].Op != store.OpNotContains || conds[1].Values[0] != "spam" {
		t.Fatalf("not_contains %+v", conds[1])
	}
}

// TestSubstringFilterAPI passes contains to the journal and the report and
// answers 400 for a field that is not text and for an empty substring.
func TestSubstringFilterAPI(t *testing.T) {
	st := &filterStats{}
	base, c := serverWithStats(t, st)

	get := func(path string) (int, string) {
		t.Helper()
		resp, err := c.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	if code, body := get("/api/logs?f=sub_id_1:contains:ab"); code != http.StatusOK {
		t.Fatalf("logs contains %d %s", code, body)
	}
	if len(st.lastLog.Conds) != 1 || st.lastLog.Conds[0].Op != store.OpContains || st.lastLog.Conds[0].Values[0] != "ab" {
		t.Fatalf("logs conds %+v", st.lastLog.Conds)
	}
	if code, body := get("/api/report?f=keyword:not_contains:free"); code != http.StatusOK {
		t.Fatalf("report not_contains %d %s", code, body)
	}
	if len(st.filtered) != 1 || st.filtered[0].Op != store.OpNotContains {
		t.Fatalf("report conds %+v", st.filtered)
	}

	logs := st.logs
	for _, path := range []string{
		"/api/logs?f=country:contains:p",
		"/api/logs?f=profit:contains:1",
		"/api/logs?f=sub_id_1:contains:",
		"/api/logs?f=sub_id_1:contains:%20%20",
		"/api/logs/export?f=offer_id:not_contains:1",
		"/api/stats/summary?f=ts:contains:2026",
		"/api/report?f=campaign_id:contains:g",
	} {
		code, body := get(path)
		if code != http.StatusBadRequest || !strings.Contains(body, "bad filter") {
			t.Fatalf("%s → %d %s", path, code, body)
		}
	}
	if st.logs != logs {
		t.Fatalf("bad substring filters reached the store")
	}
}

// TestLogFieldsText reports which fields accept the substring operators.
func TestLogFieldsText(t *testing.T) {
	base, c := serverWithStats(t, &filterStats{})
	resp, err := c.Get(base + "/api/log-fields")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var info struct {
		Fields []struct {
			Name string `json:"name"`
			Text bool   `json:"text"`
		} `json:"fields"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		t.Fatal(err)
	}
	text := map[string]bool{}
	for _, f := range info.Fields {
		text[f.Name] = f.Text
	}
	if !text["sub_id_1"] || text["country"] {
		t.Fatalf("text flags %v", text)
	}
}

// TestSubstringFilterHiddenField keeps a hidden field unfilterable with contains.
func TestSubstringFilterHiddenField(t *testing.T) {
	rig := newRolesRig(t, "")
	rig.hide(t, "sub_id_1")
	code, body := rig.get(t, rig.user, "/api/logs?f=sub_id_1:contains:ab")
	if code != http.StatusBadRequest || !strings.Contains(body, "hidden field sub_id_1") {
		t.Fatalf("hidden contains %d %s", code, body)
	}
	if code, body = rig.get(t, rig.user, "/api/logs?f=sub_id_2:contains:ab"); code != http.StatusOK {
		t.Fatalf("visible contains %d %s", code, body)
	}
}

// TestFilterDocListsSubstringOps keeps the OpenAPI text of f= in step with store.CondOps.
func TestFilterDocListsSubstringOps(t *testing.T) {
	desc := filterParams[0].Description
	for _, op := range store.CondOps {
		if !strings.Contains(desc, op) {
			t.Fatalf("f= description misses %s: %s", op, desc)
		}
	}
}
