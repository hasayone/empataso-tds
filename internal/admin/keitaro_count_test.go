package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// TestCountByOffset checks the search against many sizes, including the edges of each round.
func TestCountByOffset(t *testing.T) {
	for _, total := range []int{0, 1, 2, 9, 10, 11, 12, 99, 100, 101, 999, 1000, 1001, 9950, 10000, 123456, 999999} {
		var calls atomic.Int32
		probe := func(_ context.Context, offset int) (int, error) {
			calls.Add(1)
			if offset < total {
				return 1, nil
			}
			return 0, nil
		}
		got, err := countByOffset(context.Background(), probe, 0)
		if err != nil || got != total {
			t.Fatalf("total %d: got %d, err %v", total, got, err)
		}
		if calls.Load() > 120 {
			t.Fatalf("total %d took %d probes", total, calls.Load())
		}
	}
}

// TestCountByOffsetIgnoredLimit checks that a first answer longer than one row is the count.
func TestCountByOffsetIgnoredLimit(t *testing.T) {
	var calls atomic.Int32
	probe := func(context.Context, int) (int, error) {
		calls.Add(1)
		return 7, nil
	}
	got, err := countByOffset(context.Background(), probe, 0)
	if err != nil || got != 7 || calls.Load() != 1 {
		t.Fatalf("got %d, err %v, calls %d", got, err, calls.Load())
	}
}

// TestCountByOffsetErrors checks the ceiling and a failing probe.
func TestCountByOffsetErrors(t *testing.T) {
	always := func(context.Context, int) (int, error) { return 1, nil }
	if _, err := countByOffset(context.Background(), always, 0); err == nil {
		t.Fatal("no error over the ceiling")
	}
	boom := errors.New("boom")
	failLate := func(_ context.Context, offset int) (int, error) {
		if offset > 0 {
			return 0, boom
		}
		return 1, nil
	}
	if _, err := countByOffset(context.Background(), failLate, 0); !errors.Is(err, boom) {
		t.Fatalf("err %v", err)
	}
}

// keitaroPagedFake serves total campaigns and honours limit and offset like Keitaro.
// It records the largest response and how many campaign requests came in; log reads of the version
// probe answer 404 and are not counted.
func keitaroPagedFake(t *testing.T, total int, maxBody *atomic.Int64, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/log") {
			http.Error(w, "no log", http.StatusNotFound)
			return
		}
		hits.Add(1)
		if r.Header.Get("Api-Key") == "" || r.URL.Path != "/admin_api/v1/campaigns" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		end := total
		if limit > 0 && offset+limit < total {
			end = offset + limit
		}
		rows := []map[string]int{}
		for i := offset; i < end; i++ {
			rows = append(rows, map[string]int{"id": i + 1})
		}
		raw, _ := json.Marshal(rows)
		mu.Lock()
		if int64(len(raw)) > maxBody.Load() {
			maxBody.Store(int64(len(raw)))
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestKeitaroCheckCountsWithoutDownload checks the handler against a tracker that pages.
func TestKeitaroCheckCountsWithoutDownload(t *testing.T) {
	const total = 9950
	var maxBody atomic.Int64
	var hits atomic.Int32
	fake := keitaroPagedFake(t, total, &maxBody, &hits)
	admin := newKeitaroAdmin(t, fake.URL, "fixture-key", nil, nil, nil)
	raw, code := keitaroPOST(t, admin, "/api/keitaro/check", map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("check %d %s", code, raw)
	}
	cold := hits.Load()
	var body struct {
		OK             bool   `json:"ok"`
		Campaigns      int    `json:"campaigns"`
		API            string `json:"api"`
		ProductVersion string `json:"product_version"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if !body.OK || body.Campaigns != total || body.API != "v1" || body.ProductVersion != "" {
		t.Fatalf("check %+v", body)
	}
	if maxBody.Load() > 64 {
		t.Fatalf("a response held %d bytes, the list was downloaded", maxBody.Load())
	}
	if hits.Load() > 120 {
		t.Fatalf("%d requests", hits.Load())
	}
	t.Logf("cold requests=%d", cold)
	hits.Store(0)
	if _, code := keitaroPOST(t, admin, "/api/keitaro/check", map[string]any{}); code != http.StatusOK {
		t.Fatalf("second check %d", code)
	}
	if hits.Load() != 3 {
		t.Fatalf("second check sent %d requests, want 3 with the remembered count", hits.Load())
	}
}

// TestKeitaroCheckBadKey checks that a refused key stops after the first request and stays masked.
func TestKeitaroCheckBadKey(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, "no", http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	admin := newKeitaroAdmin(t, srv.URL, "fixture-key", nil, nil, nil)
	raw, code := keitaroPOST(t, admin, "/api/keitaro/check", map[string]any{})
	if code != http.StatusBadGateway || strings.Contains(string(raw), "fixture-key") {
		t.Fatalf("check %d %s", code, raw)
	}
	if hits.Load() != 1 {
		t.Fatalf("%d requests after a refused key", hits.Load())
	}
}

// TestCountByOffsetHint checks a remembered count that is right, too small or too large.
func TestCountByOffsetHint(t *testing.T) {
	const total = 9950
	for _, hint := range []int{1, 2, 3, 40, 9949, 9950, 9951, 12000, 999999, 1000000, 5000000} {
		var calls atomic.Int32
		probe := func(_ context.Context, offset int) (int, error) {
			calls.Add(1)
			if offset < total {
				return 1, nil
			}
			return 0, nil
		}
		got, err := countByOffset(context.Background(), probe, hint)
		if err != nil || got != total {
			t.Fatalf("hint %d: got %d, err %v", hint, got, err)
		}
		if hint == total && calls.Load() != 3 {
			t.Fatalf("exact hint took %d probes", calls.Load())
		}
	}
}
