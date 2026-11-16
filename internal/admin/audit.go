package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// auditSubdir is the directory under DataDir that holds the monthly files.
	auditSubdir = "audit"
	// auditFilePrefix and auditFileSuffix frame the month in a file name: admin-2026-11.jsonl.
	auditFilePrefix = "admin-"
	auditFileSuffix = ".jsonl"
	// auditMonthLayout is the UTC month written into the file name.
	auditMonthLayout = "2006-01"
	// auditKeepMonths is how many months of files are kept.
	auditKeepMonths = 6
	// auditPruneEvery is the interval between two retention runs.
	auditPruneEvery = 24 * time.Hour
)

// auditEntry is one line of the audit file. It holds no body, header, query or key.
// Route is the mux pattern, not the request path.
type auditEntry struct {
	TS     string `json:"ts"`
	KeyID  string `json:"key_id"`
	Method string `json:"method"`
	Route  string `json:"route"`
	Status int    `json:"status"`
	MS     int64  `json:"ms"`
	IP     string `json:"ip"`
	// UID, Source and ChangeID are set only for a change the AI assistant made on behalf of a
	// user: the user id, the origin ("assistant") and the id of the recorded change.
	UID      string `json:"uid,omitempty"`
	Source   string `json:"source,omitempty"`
	ChangeID string `json:"change_id,omitempty"`
}

// auditLog appends write requests of API keys to monthly JSONL files under
// <dir>. Appends and retention are serialized by mu. An empty dir disables the
// file; a nil auditLog is also disabled.
type auditLog struct {
	mu  sync.Mutex
	dir string
	now func() time.Time
}

// newAuditLog returns a writer for <dataDir>/audit. An empty dataDir disables
// the file part. The directory is created on the first append.
func newAuditLog(dataDir string) *auditLog {
	a := &auditLog{now: time.Now}
	if dataDir != "" {
		a.dir = filepath.Join(dataDir, auditSubdir)
	}
	return a
}

// enabled reports whether the file part is on.
func (a *auditLog) enabled() bool {
	return a != nil && a.dir != ""
}

// clock returns the current time of the log, the system time for a nil log.
func (a *auditLog) clock() time.Time {
	if a == nil {
		return time.Now()
	}
	return a.now()
}

// monthFile returns the path of the file for the UTC month of t.
func (a *auditLog) monthFile(t time.Time) string {
	return filepath.Join(a.dir, auditFilePrefix+t.UTC().Format(auditMonthLayout)+auditFileSuffix)
}

// append writes e as one line to the file of the month of e's time.
// The directory is created with mode 0700 and the file with 0600. It returns
// the first error of the create, marshal or write; a disabled log returns nil.
func (a *auditLog) append(at time.Time, e auditEntry) error {
	if !a.enabled() {
		return nil
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := os.MkdirAll(a.dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(a.monthFile(at), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(line)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

// prune removes audit files whose month ended more than auditKeepMonths before
// now. Files that do not match the audit name are never touched. It returns
// the names removed and the first error of the directory read; a missing
// directory is not an error.
func (a *auditLog) prune(now time.Time) ([]string, error) {
	if !a.enabled() {
		return nil, nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	entries, err := os.ReadDir(a.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	cutoff := now.UTC().AddDate(0, -auditKeepMonths, 0)
	var removed []string
	for _, e := range entries {
		month, ok := auditFileMonth(e.Name())
		if !ok || e.IsDir() || !month.AddDate(0, 1, 0).Before(cutoff) {
			continue
		}
		if os.Remove(filepath.Join(a.dir, e.Name())) == nil {
			removed = append(removed, e.Name())
		}
	}
	return removed, nil
}

// auditFileMonth returns the first instant of the month named by an audit file
// name, and false for any other name.
func auditFileMonth(name string) (time.Time, bool) {
	if !strings.HasPrefix(name, auditFilePrefix) || !strings.HasSuffix(name, auditFileSuffix) {
		return time.Time{}, false
	}
	t, err := time.Parse(auditMonthLayout, strings.TrimSuffix(strings.TrimPrefix(name, auditFilePrefix), auditFileSuffix))
	return t, err == nil
}

// WatchAudit applies the audit retention at once and then every 24 hours until
// ctx ends. It returns at once when the audit file is off. The caller starts it
// once after New: New itself never starts it.
func (s *Server) WatchAudit(ctx context.Context) {
	if !s.audit.enabled() {
		return
	}
	ticker := time.NewTicker(auditPruneEvery)
	defer ticker.Stop()
	for {
		s.pruneAudit()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// pruneAudit runs one retention pass and logs the removed files or the failure.
func (s *Server) pruneAudit() {
	removed, err := s.audit.prune(s.audit.clock())
	if err != nil {
		s.logWarn("audit retention failed", "err", err)
		return
	}
	if len(removed) > 0 {
		s.logInfo("audit files removed", "files", strings.Join(removed, ","))
	}
}

// statusWriter records the status code of a response. It delegates Flush to
// the wrapped writer when that is a Flusher and exposes it through Unwrap, so
// streaming handlers and http.ResponseController keep working.
type statusWriter struct {
	http.ResponseWriter
	status int
}

// WriteHeader records code, ignoring informational 1xx codes, and passes it on.
func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 && code >= http.StatusOK {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

// Write passes p on; a body without WriteHeader counts as 200.
func (w *statusWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(p)
}

// Flush flushes the wrapped writer when it supports it; a flush commits a 200.
func (w *statusWriter) Flush() {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap returns the wrapped writer for http.ResponseController.
func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// code returns the recorded status; a handler that wrote nothing answers 200.
func (w *statusWriter) code() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

// auditWrites records every state-changing request of an API key.
//
// pattern is the mux pattern of the route, written as the route instead of the
// path. It runs after auth: a cookie session and a GET, HEAD or OPTIONS request
// pass to next untouched. For the rest, one line goes to the audit file and
// one "api key write" line to the log with the same fields: ts, key_id,
// method, route, status, ms and the client address. Bodies, headers, the query
// and the key are never written. A file error is logged and does not change the
// response. When the handler panics the line is still written, with status 500 unless the
// handler had already sent a status, and the panic goes on to the caller. There is no error return.
func (s *Server) auditWrites(pattern string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess := sessionFrom(r.Context())
		if !sess.API || isSafeMethod(r.Method) {
			next.ServeHTTP(w, r)
			return
		}
		sw := &statusWriter{ResponseWriter: w}
		start := s.audit.clock()
		finished := false
		defer func() {
			status := sw.code()
			if !finished && sw.status == 0 {
				status = http.StatusInternalServerError
			}
			s.recordAudit(start, auditEntry{
				TS:     start.UTC().Format(time.RFC3339Nano),
				KeyID:  sess.KeyID,
				Method: r.Method,
				Route:  pattern,
				Status: status,
				MS:     s.audit.clock().Sub(start).Milliseconds(),
				IP:     s.clientIP(r),
			})
		}()
		next.ServeHTTP(sw, r)
		finished = true
	})
}

// recordAudit appends e to the audit file and mirrors it to the log.
// at is the request start, which picks the month file.
func (s *Server) recordAudit(at time.Time, e auditEntry) {
	if err := s.audit.append(at, e); err != nil {
		s.logWarn("audit write failed", "err", err)
	}
	s.logInfo("api key write", "ts", e.TS, "key_id", e.KeyID, "method", e.Method,
		"route", e.Route, "status", e.Status, "ms", e.MS, "ip", e.IP)
}
