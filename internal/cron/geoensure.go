package cron

import (
	"context"
	"time"
)

// The guard of the city database. Without a city database the country of every
// click is empty and every stream condition on the country fails, so the
// service does not wait for anyone to turn on geo_db or press a button: it
// looks at the file itself and downloads DB-IP City Lite when it is missing or
// does not open.
const (
	// cityGuardEvery is the pause between two checks of the city database.
	cityGuardEvery = 10 * time.Minute
	// cityRetryMin and cityRetryMax bound the pause after a failed download: it
	// doubles from the first to the second.
	cityRetryMin = time.Minute
	cityRetryMax = 30 * time.Minute
	// cityStaleAfter is the age of the database, by its build time, after which
	// the guard looks for the next monthly file. DB-IP publishes one a month.
	cityStaleAfter = 31 * 24 * time.Hour
	// cityRefreshEvery is the least pause between two looks for a newer monthly file.
	cityRefreshEvery = 24 * time.Hour
)

// GeoCityState is what the guard knows about the city database after its last
// check, for the admin panel.
// Present tells that the file opens as a city or country database, and Built is
// its build time then. DownloadedAt is the last time the guard installed a
// file, RefreshedAt the last time it looked for a newer monthly one. Running is
// true while it downloads. Error is the failure of the last download and
// NextTry when it tries again; both are empty when the database is in place.
type GeoCityState struct {
	CheckedAt    time.Time `json:"checked_at"`
	Present      bool      `json:"present"`
	Built        time.Time `json:"built,omitempty"`
	DownloadedAt time.Time `json:"downloaded_at,omitempty"`
	RefreshedAt  time.Time `json:"refreshed_at,omitempty"`
	Running      bool      `json:"running"`
	Error        string    `json:"error,omitempty"`
	NextTry      time.Time `json:"next_try,omitempty"`
}

// WatchCityDB turns the guard of the city database on. The service calls it
// before Run. Without a city database path the guard has nothing to look at.
func (r *Runner) WatchCityDB() { r.cityGuard = true }

// dbipCitySource is the DB-IP City Lite source: free, no account. The URL is
// the runner's, which tests point at a local server.
func (r *Runner) dbipCitySource() GeoSource {
	return GeoSource{Kind: KindCity, URL: r.dbipCityURL}
}

// maybeGuardCityDB runs the guard when it is on and a check is due. cfg is the
// cron config, empty when the config cannot be used. The check does not depend
// on the schedule or the switch of geo_db.
func (r *Runner) maybeGuardCityDB(ctx context.Context, cfg Config) {
	if !r.cityGuard || r.paths.CityDB == "" {
		return
	}
	r.launchCheck(&r.cityNext, &r.cityBusy, cityGuardEvery, func() { r.guardCityDB(ctx, cfg) })
}

// geoCityManaged reports whether geo_db keeps the city database on schedule:
// the job is on and has a city source.
func geoCityManaged(cfg Config) bool {
	if !cfg.GeoDB.Enabled {
		return false
	}
	for _, s := range cfg.GeoDB.Sources {
		if s.Kind == KindCity {
			return true
		}
	}
	return false
}

// guardCityDB is one check of the city database. A file that is missing or does
// not open is replaced by DB-IP City Lite at once. A database no job keeps
// current is looked after too: once it is older than cityStaleAfter the guard
// looks for the next monthly file, at most once in cityRefreshEvery. A failed
// download is logged and retried after a growing pause. While the geo_db job
// runs the guard leaves the database to it. The state is stored for the panel.
func (r *Runner) guardCityDB(ctx context.Context, cfg Config) {
	path := r.paths.CityDB
	now := r.now()
	r.mu.Lock()
	jobRuns := r.running[JobGeoDB]
	var st GeoCityState
	if r.st.GeoCity != nil {
		st = *r.st.GeoCity
	}
	r.mu.Unlock()
	if jobRuns {
		return
	}

	built, err := geoFileBuilt(path, KindCity)
	st.CheckedAt, st.Present, st.Built = now, err == nil, built
	reason := ""
	switch {
	case err != nil:
		reason = "missing"
	case !geoCityManaged(cfg) && now.Sub(built) > cityStaleAfter && now.Sub(st.RefreshedAt) >= cityRefreshEvery:
		reason = "stale"
	}
	if reason == "" {
		if st.Present {
			st.Error, st.NextTry = "", time.Time{}
		}
		r.storeCityState(st)
		return
	}

	if reason == "missing" {
		r.log.Info("cron: city database is not usable, downloading DB-IP City Lite", "path", path)
	}
	st.Running = true
	r.storeCityState(st)
	msg, err := r.updateGeo(ctx, r.dbipCitySource(), path)
	st.Running = false
	if ctx.Err() != nil {
		r.storeCityState(st)
		return
	}
	if reason == "stale" {
		st.RefreshedAt = now
	}
	if err != nil {
		r.failCityGuard(&st, reason, err)
		return
	}
	r.mu.Lock()
	r.cityFails = 0
	r.mu.Unlock()
	st.Error, st.NextTry = "", time.Time{}
	if b, berr := geoFileBuilt(path, KindCity); berr == nil {
		st.Present, st.Built = true, b
		if reason == "missing" {
			st.DownloadedAt = r.now()
		}
	}
	r.storeCityState(st)
	r.log.Info("cron: city database checked", "reason", reason, "result", msg)
}

// failCityGuard records a failed download in st and stores it. After a failure
// to fetch a missing database the next check is brought forward to the retry
// pause; a failed look for a newer file waits for the usual pause, the database
// in place keeps working. The error is logged.
func (r *Runner) failCityGuard(st *GeoCityState, reason string, err error) {
	st.Error = err.Error()
	attrs := []any{"reason", reason, "err", err}
	if reason == "missing" {
		r.mu.Lock()
		r.cityFails++
		pause := cityRetryMin << min(r.cityFails-1, 10)
		if pause > cityRetryMax {
			pause = cityRetryMax
		}
		r.cityNext = r.now().Add(pause)
		r.mu.Unlock()
		st.NextTry = r.now().Add(pause)
		attrs = append(attrs, "retry_in", pause.String())
	}
	r.storeCityState(*st)
	r.log.Warn("cron: city database download failed", attrs...)
}

// storeCityState keeps st as the guard state and writes the status file.
func (r *Runner) storeCityState(st GeoCityState) {
	r.state(func(s *Status) { s.GeoCity = &st })
	r.save()
}
