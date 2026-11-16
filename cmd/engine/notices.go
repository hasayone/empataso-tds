package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
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
// attrs are the attributes bound with WithAttrs, keyed with the group path
// that was open at that moment; groups is the open group path.
type noticeHandler struct {
	slog.Handler
	ring   *noticeRing
	attrs  []slog.Attr
	groups []string
}

// Handle stores a warning or error record as one line "message key=value ...":
// the attributes name the list, the click or the error, so two records with
// the same message stay distinguishable. Records below warning are only forwarded.
// It returns the error of the wrapped handler.
func (h *noticeHandler) Handle(ctx context.Context, rec slog.Record) error {
	if rec.Level >= slog.LevelWarn {
		attrs := append([]slog.Attr(nil), h.attrs...)
		prefix := groupPrefix(h.groups)
		rec.Attrs(func(a slog.Attr) bool {
			attrs = append(attrs, slog.Attr{Key: prefix + a.Key, Value: a.Value})
			return true
		})
		h.ring.add(noticeLine(rec.Message, attrs))
	}
	return h.Handler.Handle(ctx, rec)
}

// WithAttrs returns a handler that also carries a, qualified by the open groups.
func (h *noticeHandler) WithAttrs(a []slog.Attr) slog.Handler {
	prefix := groupPrefix(h.groups)
	bound := append([]slog.Attr(nil), h.attrs...)
	for _, x := range a {
		bound = append(bound, slog.Attr{Key: prefix + x.Key, Value: x.Value})
	}
	return &noticeHandler{Handler: h.Handler.WithAttrs(a), ring: h.ring, attrs: bound, groups: h.groups}
}

// WithGroup returns a handler whose later attributes are keyed under name.
// An empty name changes nothing, as in slog.
func (h *noticeHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	groups := append(append([]string(nil), h.groups...), name)
	return &noticeHandler{Handler: h.Handler.WithGroup(name), ring: h.ring, attrs: h.attrs, groups: groups}
}

// groupPrefix is the key prefix of an open group path: "a.b." or "" for none.
func groupPrefix(groups []string) string {
	if len(groups) == 0 {
		return ""
	}
	return strings.Join(groups, ".") + "."
}

// noticeLine joins msg and attrs into "msg key=value key=value".
// A group-valued attribute is flattened into dotted keys, an attribute with an
// empty key is skipped, and a value with a space, quote or equals sign is quoted.
func noticeLine(msg string, attrs []slog.Attr) string {
	var b strings.Builder
	b.WriteString(msg)
	var put func(key string, v slog.Value)
	put = func(key string, v slog.Value) {
		v = v.Resolve()
		if v.Kind() == slog.KindGroup {
			for _, sub := range v.Group() {
				k := sub.Key
				if key != "" && k != "" {
					k = key + "." + k
				} else if k == "" {
					k = key
				}
				put(k, sub.Value)
			}
			return
		}
		if key == "" {
			return
		}
		s := v.String()
		if s == "" || strings.ContainsAny(s, " \t\r\n\"=") {
			s = strconv.Quote(s)
		}
		b.WriteString(" " + key + "=" + s)
	}
	for _, a := range attrs {
		put(a.Key, a.Value)
	}
	return b.String()
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
