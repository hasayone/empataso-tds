package admin

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
)

const (
	// domainWatchFirst is the wait before the first tick after the watch starts.
	domainWatchFirst = 30 * time.Second
	// domainWatchEvery is the tick interval used when WatchDomains gets none.
	domainWatchEvery = 5 * time.Minute
	// domainWatchWindow is how long a purchased domain without Cloudflare stays in the watch.
	domainWatchWindow = 7 * 24 * time.Hour
	// domainWatchFast is how long a domain is checked on every tick; after it a domain is
	// checked at most once per domainWatchSlow.
	domainWatchFast = 48 * time.Hour
	domainWatchSlow = time.Hour
	// domainWatchCap is the most domains one tick works on, domainWatchWorkers how many at once.
	domainWatchCap     = 50
	domainWatchWorkers = 4
	// domainWatchStep bounds the work on one domain.
	domainWatchStep = 2 * time.Minute
)

// watchReport counts what one tick did: domains handled, domains whose verdict or Cloudflare
// status changed, domains whose work failed, and the text of the first failure.
type watchReport struct {
	checked, changed, failed int
	firstError               string
}

// WatchDomains keeps unfinished domains moving until ctx ends.
// every is the tick interval; zero or less uses five minutes. The first tick runs about thirty
// seconds after the call, then one runs per interval. A tick that finds the previous one still
// running does nothing. A domain is watched while it is enabled, not OK, and either has a
// Cloudflare status or is a Namecheap purchase less than a week old. A watched domain is
// connected when it is connect_pending, otherwise its zone is read and the domain is checked
// again. WatchDomains returns at once without a domain store. The caller starts it once
// after New: New itself never starts it.
func (s *Server) WatchDomains(ctx context.Context, every time.Duration) {
	if s.cfg.Domains == nil {
		return
	}
	if every <= 0 {
		every = domainWatchEvery
	}
	first := s.watchFirst
	if first <= 0 {
		first = domainWatchFirst
	}
	firstTimer := time.NewTimer(first)
	defer firstTimer.Stop()
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-firstTimer.C:
		case <-ticker.C:
		}
		s.watchOnce(ctx)
	}
}

// watchOnce runs one tick: it picks the due domains, works on them with a few workers and logs
// one line when a verdict changed or a domain failed. It returns the counts; a tick that finds
// another one running, or cannot read the file, returns the zero report.
func (s *Server) watchOnce(ctx context.Context) watchReport {
	var rep watchReport
	if !s.watchBusy.CompareAndSwap(false, true) {
		return rep
	}
	defer s.watchBusy.Store(false)
	list, err := s.cfg.Domains.List(ctx)
	if err != nil {
		s.logWarn("domain watch cannot read domains", "err", err)
		return rep
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, domainWatchWorkers)
	for _, d := range pickWatched(list, s.nowUTC()) {
		sem <- struct{}{}
		wg.Add(1)
		go func(d config.Domain) {
			defer func() { <-sem; wg.Done() }()
			changed, err := s.watchOne(ctx, d)
			mu.Lock()
			defer mu.Unlock()
			rep.checked++
			switch {
			case err != nil:
				rep.failed++
				if rep.firstError == "" {
					rep.firstError = d.Host + ": " + err.Error()
				}
			case changed:
				rep.changed++
			}
		}(d)
	}
	wg.Wait()
	if rep.changed > 0 || rep.failed > 0 {
		s.logInfo("domain watch", "checked", rep.checked, "changed", rep.changed, "failed", rep.failed, "first_error", rep.firstError)
	}
	return rep
}

// pickWatched chooses the domains a tick works on.
// list is the stored domains and now the current time. A domain is picked when it is enabled,
// not OK, and has a Cloudflare status or is a Namecheap purchase whose watch started less than
// a week ago. A domain whose watch started more than two days ago, or has no start time, is
// skipped while its last check is less than an hour old. The result is ordered by the last
// check, never checked first, and cut to domainWatchCap.
func pickWatched(list []config.Domain, now time.Time) []config.Domain {
	var out []config.Domain
	for _, d := range list {
		since, hasSince := parseRFC3339(d.WatchSince)
		inWindow := d.Registrar == namecheapRegistrar && hasSince && now.Sub(since) < domainWatchWindow
		adminProbe := config.IsAdminDomain(d) && !d.AdminOK && d.CfZoneID != ""
		if d.Disabled || (d.OK && !adminProbe) || (d.CfStatus == "" && !inWindow && !adminProbe) {
			continue
		}
		if checked, ok := parseRFC3339(d.Checked); ok && (!hasSince || now.Sub(since) > domainWatchFast) && now.Sub(checked) < domainWatchSlow {
			continue
		}
		out = append(out, d)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Checked < out[j].Checked })
	if len(out) > domainWatchCap {
		out = out[:domainWatchCap]
	}
	return out
}

// parseRFC3339 reads a stored time. ok is false for an empty or damaged text.
func parseRFC3339(text string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, text)
	return t, err == nil
}

// watchOne works on one picked domain.
// d is the row as the tick read it. A connect_pending domain is connected; when that leaves the
// zone active it is checked at once. Any other domain has its zone read, when it has one, and is
// checked. A domain whose connection is already running or whose setup is missing is skipped
// without an error. It returns whether OK, Reason or the Cloudflare status changed and the
// error of the work, which is free of secrets.
func (s *Server) watchOne(ctx context.Context, d config.Domain) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, domainWatchStep)
	defer cancel()
	after := d
	if d.CfStatus == cfStatusConnectPending {
		row, _, err := s.connectCloudflare(ctx, d.ID)
		if errors.Is(err, errCFBusy) || errors.Is(err, errCFNotConfigured) || errors.Is(err, errNoPublicIP) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		after = row
	} else if d.CfZoneID != "" {
		s.refreshZone(ctx, d)
	}
	if d.CfStatus != cfStatusConnectPending || after.CfStatus == cfStatusActive {
		row, err := s.recheckDomain(ctx, d.ID)
		if err != nil {
			return false, err
		}
		after = row
	}
	return after.OK != d.OK || after.Reason != d.Reason || after.CfStatus != d.CfStatus, nil
}

// refreshZone reads the Cloudflare zone of d and writes its status and name servers when they
// differ from the stored ones; a pending zone is also asked to check its name servers now.
// d is the stored row with a zone id. Nothing happens when Cloudflare is not configured. A
// failed call leaves the row as it is, and so does a failed activation check, which Cloudflare
// limits.
func (s *Server) refreshZone(ctx context.Context, d config.Domain) {
	acct := s.cloudflareAccount()
	if !acct.ready() {
		return
	}
	client := s.cloudflareClient(acct)
	zone, err := client.zone(ctx, d.CfZoneID)
	if err != nil {
		return
	}
	ns := lowerHosts(zone.NameServers)
	status := cfStatusPending
	if zone.Status == cfStatusActive {
		status = cfStatusActive
	}
	if status != d.CfStatus || !sameHosts(d.CfNS, ns) {
		saved, _ := s.editDomain(ctx, d.ID, func(cur *config.Domain) error {
			if cur.CfZoneID != d.CfZoneID {
				return errDomainHostChanged
			}
			applyZone(cur, zone, ns)
			return nil
		})
		if status == cfStatusActive {
			s.maybeScheduleAdminDNS(saved)
		}
	}
	if status != cfStatusActive {
		_ = client.activationCheck(ctx, zone.ID)
	}
}
