package admin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/egerkuzma/emtds/internal/store"
)

// metaStats answers the report with one row and a chosen truncated flag.
type metaStats struct {
	fakeStats
	truncated bool
	metaCalls int
	plain     int
}

func (m *metaStats) Slice(context.Context, time.Time, time.Time) ([]store.SliceRow, error) {
	m.plain++
	return []store.SliceRow{{Group: "g", Hits: 3}}, nil
}

func (m *metaStats) SliceMeta(context.Context, time.Time, time.Time, []store.Cond) ([]store.SliceRow, bool, error) {
	m.metaCalls++
	return []store.SliceRow{{Group: "g", Hits: 3}}, m.truncated, nil
}

func getBody(t *testing.T, c *http.Client, url string) []byte {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s → %d %s", url, resp.StatusCode, b)
	}
	return b
}

// TestReportTruncatedFlag checks that meta=1 wraps the rows with the truncated flag, and that
// a request without meta keeps the bare array other clients read.
func TestReportTruncatedFlag(t *testing.T) {
	for _, cut := range []bool{true, false} {
		st := &metaStats{truncated: cut}
		base, c := serverWithStats(t, st)

		var got struct {
			Rows      []store.SliceRow `json:"rows"`
			Truncated bool             `json:"truncated"`
		}
		if err := json.Unmarshal(getBody(t, c, base+"/api/report?meta=1"), &got); err != nil {
			t.Fatal(err)
		}
		if got.Truncated != cut || len(got.Rows) != 1 || got.Rows[0].Hits != 3 {
			t.Errorf("meta=1 with truncated=%v gave %+v", cut, got)
		}
		if st.metaCalls != 1 || st.plain != 0 {
			t.Errorf("meta=1 used SliceMeta %d time(s) and Slice %d time(s)", st.metaCalls, st.plain)
		}

		var arr []store.SliceRow
		if err := json.Unmarshal(getBody(t, c, base+"/api/report"), &arr); err != nil {
			t.Fatalf("plain report is not an array: %v", err)
		}
		if len(arr) != 1 || st.metaCalls != 1 || st.plain != 1 {
			t.Errorf("plain report gave %+v, SliceMeta %d, Slice %d", arr, st.metaCalls, st.plain)
		}
	}
}

// TestReportMetaWithoutSliceMeta checks that a store without SliceMeta still answers meta=1,
// with truncated false and an empty list instead of null.
func TestReportMetaWithoutSliceMeta(t *testing.T) {
	base, c := serverWithStats(t, &filterStats{})
	var got struct {
		Rows      []store.SliceRow `json:"rows"`
		Truncated bool             `json:"truncated"`
	}
	b := getBody(t, c, base+"/api/report?meta=1")
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Truncated || got.Rows == nil {
		t.Errorf("meta=1 on a plain store gave %s", b)
	}
}
