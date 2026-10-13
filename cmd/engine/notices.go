package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// noticeRing keeps warning lines since the process started.
// A restart clears it. Nothing is written to an operating-system log file.
type noticeRing struct {
	mu    sync.Mutex
	lines []string
	max   int
}

func newNoticeRing(max int) *noticeRing {
	if max < 1 {
		max = 1
	}
	return &noticeRing{max: max}
}

func (r *noticeRing) add(s string) {
	if r == nil || s == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, time.Now().UTC().Format(time.RFC3339)+" "+s)
	if len(r.lines) > r.max {
		r.lines = append([]string(nil), r.lines[len(r.lines)-r.max:]...)
	}
}

func (r *noticeRing) snapshot() []string {
	if r == nil {
		return []string{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.lines))
	copy(out, r.lines)
	return out
}

// noticeHandler copies warnings into the ring and forwards every record.
type noticeHandler struct {
	slog.Handler
	ring *noticeRing
}

func (h *noticeHandler) Handle(ctx context.Context, rec slog.Record) error {
	if rec.Level >= slog.LevelWarn {
		h.ring.add(rec.Message)
	}
	return h.Handler.Handle(ctx, rec)
}

func (h *noticeHandler) WithAttrs(a []slog.Attr) slog.Handler {
	return &noticeHandler{Handler: h.Handler.WithAttrs(a), ring: h.ring}
}

func (h *noticeHandler) WithGroup(name string) slog.Handler {
	return &noticeHandler{Handler: h.Handler.WithGroup(name), ring: h.ring}
}

func serveNotices(ring *noticeRing) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ring.snapshot())
	}
}
