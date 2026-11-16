package admin

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/egerkuzma/emtds/internal/store"
)

type sortCaptureStats struct {
	fakeStats
	lastLog store.LogFilter
	lastPB  store.PostbackQuery
}

func (s *sortCaptureStats) Logs(_ context.Context, f store.LogFilter) ([]store.LogRow, int64, error) {
	s.lastLog = f
	return nil, 0, nil
}

func (s *sortCaptureStats) QueryPostbacks(_ context.Context, q store.PostbackQuery) ([]store.PostbackRow, store.PostbackTotals, error) {
	s.lastPB = q
	return nil, store.PostbackTotals{}, nil
}

func TestJournalSortBadDir(t *testing.T) {
	st := &sortCaptureStats{}
	base, c := serverWithStats(t, st)
	resp, err := c.Get(base + "/api/logs?sort=country&dir=up")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestJournalSortHiddenField(t *testing.T) {
	rig := newRolesRig(t, "")
	rig.hide(t, "country")
	code, body := rig.get(t, rig.user, "/api/logs?sort=country")
	if code != http.StatusBadRequest || !strings.Contains(body, "bad sort") {
		t.Fatalf("hidden sort: %d %s", code, body)
	}
}

func TestJournalSortReachesStore(t *testing.T) {
	st := &sortCaptureStats{}
	base, c := serverWithStats(t, st)
	resp, err := c.Get(base + "/api/logs?sort=country&dir=asc")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if st.lastLog.Sort != "country" || st.lastLog.Desc {
		t.Fatalf("log filter sort=%q desc=%v", st.lastLog.Sort, st.lastLog.Desc)
	}

	resp, err = c.Get(base + "/api/postbacks?sort=profit&dir=desc")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if st.lastPB.Sort != "profit" || !st.lastPB.Desc {
		t.Fatalf("postback sort=%q desc=%v", st.lastPB.Sort, st.lastPB.Desc)
	}
}
