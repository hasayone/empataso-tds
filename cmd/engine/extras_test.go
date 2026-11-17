package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/logbuf"
)

// withClickLog serves groups through a buffer that keeps the logged clicks.
// opt may set engine fields such as the API key. stop drains the buffer;
// events are complete only after stop returns.
func withClickLog(t *testing.T, groups *config.Groups, opt func(*engineDeps)) (http.Handler, *captureIns, func()) {
	t.Helper()
	ins := &captureIns{}
	buf := logbuf.New(ins, 100, 1, time.Millisecond, discardLog())
	ctx, cancel := context.WithCancel(context.Background())
	go buf.Run(ctx)
	h := testEnv(t, groups, func(d *engineDeps) {
		d.logs = buf
		if opt != nil {
			opt(d)
		}
	})
	stop := func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer ccancel()
		if err := buf.Close(cctx); err != nil {
			t.Fatalf("logbuf: %v", err)
		}
		cancel()
	}
	return h, ins, stop
}

func TestRequestXRWClipsAndSkipsAPI(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Requested-With", strings.Repeat("a", 80))
	if got := requestXRW(r, false); got != strings.Repeat("a", 64) {
		t.Fatalf("%q", got)
	}
	if requestXRW(r, true) != "" {
		t.Fatal("api mode kept X-Requested-With")
	}
}
