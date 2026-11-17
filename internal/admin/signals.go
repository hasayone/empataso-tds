package admin

import (
	"context"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/cron"
)

// Names of the services in signalsView.Down.
const (
	signalEngine     = "engine"
	signalClickHouse = "clickhouse"
	signalRedis      = "redis"
)

// signalsView is the body of GET /api/system/signals: the two marks of the header.
// Update is true when a fresh check of the update agent found a newer commit for this very build.
// Down lists the services that do not answer, in the order engine, clickhouse, redis; it is never null.
type signalsView struct {
	Update bool     `json:"update"`
	Down   []string `json:"down"`
}

// handleSignals serves GET /api/system/signals for the marks of the header: the dot of the
// avatar, the icon of the "About" entry and the failure window all read this one answer.
// Only administrators may call it, and only with a browser session. A service that does not
// answer is listed in down of a 200 answer, never an error status.
func (s *Server) handleSignals(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(sessionFrom(r.Context())) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	writeJSON(w, http.StatusOK, s.collectSignals(r.Context(), time.Now()))
}

// collectSignals checks the engine, ClickHouse and Redis in parallel and reads the update check.
// ctx bounds every check; now dates the check file. A service that is not configured is not down.
func (s *Server) collectSignals(ctx context.Context, now time.Time) signalsView {
	var engine, ch, redis bool
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		_, msg := s.fetchEngineVersion(ctx)
		engine = msg != ""
	}()
	go func() {
		defer wg.Done()
		ch = checkService(ctx, pingerOf(s.cfg.Stats)).State == serviceDown
	}()
	go func() {
		defer wg.Done()
		redis = checkService(ctx, pingerOf(s.cfg.Sessions)).State == serviceDown
	}()
	out := signalsView{Update: s.updateAvailable(now), Down: []string{}}
	wg.Wait()
	for _, d := range []struct {
		down bool
		name string
	}{{engine, signalEngine}, {ch, signalClickHouse}, {redis, signalRedis}} {
		if d.down {
			out.Down = append(out.Down, d.name)
		}
	}
	return out
}

// updateAvailable reports whether the header shows the update mark. It is the update notice of
// GET /api/notices narrowed to this build: a fresh valid check.json with ahead > 0 whose current
// commit is the commit of the running admin. A development build or a check made before the last
// apply gives false, so the mark goes out as soon as the new build runs.
func (s *Server) updateAvailable(now time.Time) bool {
	if _, ok := s.updateNotice(now); !ok {
		return false
	}
	b := config.Build()
	if b.IsDev() {
		return false
	}
	var check cron.CheckFile
	if err := cron.ReadUpdateJSON(filepath.Join(s.cfg.UpdateDir, cron.UpdateCheckFile), cron.UpdateMaxJSON, &check); err != nil {
		return false
	}
	return check.Current.Commit == b.Commit
}
