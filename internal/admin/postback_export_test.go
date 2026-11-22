package admin

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/hasayone/empataso-tds/internal/security"
	"github.com/hasayone/empataso-tds/internal/store"
)

type exportPostbackStats struct {
	fakeStats
	rows []store.PostbackRow
}

func (e *exportPostbackStats) EachPostback(_ context.Context, _ store.PostbackQuery, max int, prepare func(store.PostbackTotals) error, fn func(store.PostbackRow) error) (bool, error) {
	tot := store.PostbackTotals{Count: int64(len(e.rows))}
	truncated := tot.Count > int64(max)
	if prepare != nil {
		if err := prepare(tot); err != nil {
			return false, err
		}
	}
	n := len(e.rows)
	if n > max {
		n = max
	}
	for i := 0; i < n; i++ {
		if err := fn(e.rows[i]); err != nil {
			return false, err
		}
	}
	return truncated, nil
}

func TestPostbackExportHeaderMatchesRecord(t *testing.T) {
	head := postbackExportHeader()
	rec := postbackExportRecord(store.PostbackRow{Profit: 1.234, Status: "=1+1", CID: "c1"})
	if len(head) != len(rec) {
		t.Fatalf("header %d cols, record %d", len(head), len(rec))
	}
	if rec[8] != "1.23" {
		t.Fatalf("profit cell = %q", rec[8])
	}
	if rec[10] != "'=1+1" {
		t.Fatalf("formula cell = %q", rec[10])
	}
	if head[len(head)-2] != "depsum" || head[len(head)-1] != "dep2sum" {
		t.Fatalf("last columns = %q %q", head[len(head)-2], head[len(head)-1])
	}
	got := postbackExportRecord(store.PostbackRow{DepSum: 2.5, Dep2Sum: 4})
	if got[len(got)-2] != "2.50" || got[len(got)-1] != "4.00" {
		t.Fatalf("deposit cells = %q %q", got[len(got)-2], got[len(got)-1])
	}
}

func TestPostbackExportHiddenFields(t *testing.T) {
	rig := newRolesRig(t, "")
	rig.hide(t, "profit")
	code, body := rig.get(t, rig.user, "/api/postbacks/export")
	if code != http.StatusOK {
		t.Fatalf("export %d %s", code, body)
	}
	recs, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	if err != nil || len(recs) < 1 {
		t.Fatalf("csv err=%v len=%d", err, len(recs))
	}
	for _, col := range recs[0] {
		if col == "profit" {
			t.Fatal("profit column must be hidden")
		}
	}
}

func TestPostbackExportTruncatedHeader(t *testing.T) {
	rows := make([]store.PostbackRow, exportRows+1)
	st := &exportPostbackStats{rows: rows}
	base, c := serverWithStats(t, st)
	resp, err := c.Get(base + "/api/postbacks/export")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("X-Export-Truncated") != "1" {
		t.Fatalf("truncated header = %q", resp.Header.Get("X-Export-Truncated"))
	}
	io.Copy(io.Discard, resp.Body)
}

func TestPostbackExportRowCount(t *testing.T) {
	rows := make([]store.PostbackRow, 1500)
	for i := range rows {
		rows[i] = store.PostbackRow{CID: fmt.Sprintf("c%d", i)}
	}
	st := &exportPostbackStats{rows: rows}
	base, c := serverWithStats(t, st)
	code, body := getWithClient(t, c, base+"/api/postbacks/export")
	if code != http.StatusOK {
		t.Fatalf("export %d %s", code, body)
	}
	recs, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1501 {
		t.Fatalf("csv rows = %d, want 1501 (header + 1500)", len(recs))
	}
}

func TestPostbackExportAccess(t *testing.T) {
	rig := newRolesRig(t, "")
	if _, err := rig.s.roles.Update(rig.roleID, func(role *security.Role) error {
		role.Access.Sections = []string{string(secDashboard), string(secFlows), string(secLogs), string(secReport)}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	code, _ := rig.get(t, rig.user, "/api/postbacks/export")
	if code != http.StatusForbidden && code != http.StatusNotFound {
		t.Fatalf("no conversions access: %d", code)
	}
}

func getWithClient(t *testing.T, c *http.Client, url string) (int, string) {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}
