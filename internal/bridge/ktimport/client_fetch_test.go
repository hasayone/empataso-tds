package ktimport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestGetAcceptsNineMegabytes(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 9<<20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/admin_api/v1/campaigns" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(payload)
	}))
	defer srv.Close()
	c := &Client{Base: srv.URL, Key: "k-test", HTTP: srv.Client()}
	got, err := c.Get(context.Background(), "/admin_api/v1/campaigns")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(payload) {
		t.Fatalf("len %d", len(got))
	}
}

func TestFetchSnapshotGroups(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/admin_api/v1/campaigns":
			_, _ = io.WriteString(w, `[{"id":1,"alias":"a","name":"A"}]`)
		case "/admin_api/v1/campaigns/1/streams":
			_, _ = io.WriteString(w, `[]`)
		case "/admin_api/v1/groups":
			switch r.URL.Query().Get("type") {
			case "campaigns":
				_, _ = io.WriteString(w, `[{"id":1,"name":"C","type":"campaigns"}]`)
			case "offers":
				_, _ = io.WriteString(w, `[{"id":2,"name":"O","type":"offers"}]`)
			case "landings":
				http.Error(w, "no", http.StatusInternalServerError)
			default:
				http.Error(w, "Param type is required", http.StatusBadRequest)
			}
		default:
			_, _ = io.WriteString(w, `[]`)
		}
	}))
	defer srv.Close()
	c := &Client{Base: srv.URL, Key: "k-test", HTTP: srv.Client()}
	snap, err := c.FetchSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Groups) != 2 || snap.Groups[0].Name != "C" || snap.Groups[1].Name != "O" {
		t.Fatalf("groups %+v", snap.Groups)
	}
}

func TestFetchStreamsParallel(t *testing.T) {
	var inflight int32
	var maxIn int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/admin_api/v1/campaigns/") && strings.HasSuffix(r.URL.Path, "/streams") {
			n := atomic.AddInt32(&inflight, 1)
			for {
				old := atomic.LoadInt32(&maxIn)
				if n <= old || atomic.CompareAndSwapInt32(&maxIn, old, n) {
					break
				}
			}
			time.Sleep(80 * time.Millisecond)
			atomic.AddInt32(&inflight, -1)
			_, _ = io.WriteString(w, `[]`)
			return
		}
		if r.URL.Path == "/admin_api/v1/campaigns" {
			_, _ = io.WriteString(w, `[{"id":1},{"id":2},{"id":3},{"id":4},{"id":5},{"id":6}]`)
			return
		}
		_, _ = io.WriteString(w, `[]`)
	}))
	defer srv.Close()
	var mu sync.Mutex
	sawSnap := false
	reached := false
	c := &Client{Base: srv.URL, Key: "k-test", HTTP: srv.Client()}
	c.Progress = func(stage string, done, total int) {
		mu.Lock()
		defer mu.Unlock()
		if stage == "snapshot" && done == 0 && total == 0 {
			sawSnap = true
		}
		if stage == "streams" && total == 6 && done == total {
			reached = true
		}
	}
	snap, err := c.FetchSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Streams) != 6 || atomic.LoadInt32(&maxIn) < 2 || !sawSnap || !reached {
		t.Fatalf("streams %d inflight %d snap %v reached %v", len(snap.Streams), maxIn, sawSnap, reached)
	}
}

func TestFetchLogsStopsOnEmptyPage(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = io.WriteString(w, `{"rows":[],"total":"331023"}`)
	}))
	defer srv.Close()
	c := &Client{Base: srv.URL, Key: "k-test", HTTP: srv.Client()}
	n, err := c.FetchClicks(context.Background(), "2026-01-01 00:00:00", "2026-01-02 00:00:00", func(ClickRow) error {
		t.Fatal("row")
		return nil
	})
	if err != nil || n != 0 || calls != 1 {
		t.Fatalf("n=%d calls=%d err=%v", n, calls, err)
	}
}

func TestFetchRowsShortPageContinues(t *testing.T) {
	const limit = 10
	pages := []int{10, 5, 5, 5}
	var calls int
	var offsets []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Limit  int `json:"limit"`
			Offset int `json:"offset"`
			Sort   []struct {
				Name  string `json:"name"`
				Order string `json:"order"`
			} `json:"sort"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("json %v", err)
			return
		}
		if req.Limit != limit || len(req.Sort) != 1 || req.Sort[0].Name != "datetime" || req.Sort[0].Order != "asc" {
			t.Errorf("req limit=%d sort=%+v", req.Limit, req.Sort)
		}
		offsets = append(offsets, req.Offset)
		n := 0
		if calls < len(pages) {
			n = pages[calls]
		}
		calls++
		_, _ = io.WriteString(w, `{"rows":`+logRows(n, fmt.Sprintf(`{"sub_id":"p%d"}`, req.Offset))+`,"total":"25"}`)
	}))
	defer srv.Close()
	c := &Client{Base: srv.URL, Key: "k-test", HTTP: srv.Client()}
	var progress []string
	c.Progress = func(stage string, done, total int) {
		progress = append(progress, fmt.Sprintf("%s %d %d", stage, done, total))
	}
	n, err := fetchRows(c, context.Background(), "/admin_api/v1/clicks/log", "2026-01-01 00:00:00", "2026-01-02 00:00:00", []string{"sub_id"}, "clicks", limit, "datetime", func(ClickRow) error {
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 25 || calls != 4 || fmt.Sprint(offsets) != "[0 10 15 20]" {
		t.Fatalf("n=%d calls=%d offsets=%v", n, calls, offsets)
	}
	if strings.Join(progress, "\n") != "clicks 10 25\nclicks 15 25\nclicks 20 25\nclicks 25 25" {
		t.Fatalf("progress %v", progress)
	}
}

func TestFetchRowsStuckPage(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = io.WriteString(w, `{"rows":[{"sub_id":"same"},{"sub_id":"next"}],"total":100}`)
	}))
	defer srv.Close()
	c := &Client{Base: srv.URL, Key: "k-test", HTTP: srv.Client()}
	n, err := fetchRows(c, context.Background(), "/admin_api/v1/clicks/log", "2026-01-01 00:00:00", "2026-01-02 00:00:00", []string{"sub_id"}, "clicks", 10, "datetime", func(ClickRow) error {
		return nil
	})
	if err == nil || err.Error() != "журнал не двигается" || n != 2 || calls != 2 {
		t.Fatalf("n=%d calls=%d err=%v", n, calls, err)
	}
}

func TestFetchRowsAbsentTotalShortPage(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = io.WriteString(w, `{"rows":[{"sub_id":"a"},{"sub_id":"b"},{"sub_id":"c"}]}`)
	}))
	defer srv.Close()
	c := &Client{Base: srv.URL, Key: "k-test", HTTP: srv.Client()}
	n, err := fetchRows(c, context.Background(), "/admin_api/v1/conversions/log", "2026-01-01 00:00:00", "2026-01-02 00:00:00", []string{"sub_id"}, "conversions", 10, "postback_datetime", func(ConversionRow) error {
		return nil
	})
	if err != nil || n != 3 || calls != 1 {
		t.Fatalf("n=%d calls=%d err=%v", n, calls, err)
	}
}
