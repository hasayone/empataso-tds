package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/egerkuzma/emtds/internal/config"
	"github.com/egerkuzma/emtds/internal/logbuf"
	"github.com/egerkuzma/emtds/internal/render"
	"github.com/egerkuzma/emtds/internal/router"
	"github.com/egerkuzma/emtds/internal/security"
)

// outboundJob is one server-to-server request. The URL is already expanded.
// seg is the segment of the disk queue that holds the job; it is empty for a
// job that was only put into the channel, and is not part of the stored form.
type outboundJob struct {
	Method  string  `json:"method"`
	URL     string  `json:"url"`
	CID     string  `json:"cid"`
	RootCID string  `json:"root_cid,omitempty"`
	Status  string  `json:"status"`
	Payout  float64 `json:"payout"`

	seg logbuf.Segment
}

// catalogs returns the offer and landing maps the router reads. They are the
// books' published snapshots, shared without a copy: callers must only read
// them. A missing book gives a nil map, which reads as empty.
func (d *engineDeps) catalogs() router.Catalogs {
	if d == nil {
		return router.Catalogs{}
	}
	return router.Catalogs{Offers: d.offers.ByID(), Landings: d.landings.ByID()}
}

// sourceOf returns the group's traffic source when one is configured.
func (d *engineDeps) sourceOf(grp *config.Group) (*config.TrafficSource, bool) {
	if d == nil || d.sources == nil || grp == nil || grp.SourceID == "" {
		return nil, false
	}
	src, ok := d.sources.Get(grp.SourceID)
	if !ok {
		return nil, false
	}
	return &src, true
}

// fireOutbound queues the campaign templates that match status.
// subs are the sub ids of the postback query, for the macros of the templates.
// Local postbacks and ids from the s2s catalog are both included.
// A missing catalog id counts as a match that was not queued.
// With a disk queue each job is written there first and the sender takes it
// from disk; without one, or when the disk refuses it, the job goes into the
// channel, and a full channel drops it so the inbound pixel is never blocked.
// It reports false only when a matching template existed and none was queued.
func (d *engineDeps) fireOutbound(groupID, cid, rootCID string, profit float64, status string, subs [config.MaxSubID]string) bool {
	if d == nil || d.outbound == nil || d.groups == nil || groupID == "" {
		return true
	}
	grp, ok := d.groups.Get(groupID)
	if !ok {
		return true
	}
	list, missing := d.templatesFor(*grp)
	payout := strconv.FormatFloat(profit, 'f', -1, 64)
	matched, queued := missing, 0
	for _, pb := range list {
		if !pb.MatchesStatus(status) {
			continue
		}
		matched++
		method := strings.ToUpper(strings.TrimSpace(pb.Method))
		if method == "" {
			method = http.MethodGet
		}
		job := outboundJob{
			Method: method,
			URL: render.Expand(pb.URL, render.MacroDeps{
				CID: cid, RootCID: rootCID, Status: status, Payout: payout, SubIDs: subs,
			}),
			CID: cid, RootCID: rootCID, Status: status, Payout: profit,
		}
		if d.enqueueOutbound(job) {
			queued++
		}
	}
	if matched == 0 {
		return true
	}
	return queued > 0
}

// enqueueOutbound hands one job to the sender and reports whether it was
// queued. The job is written to the disk queue first; a disk that refuses it
// is logged and the job goes into the channel instead. A full channel drops the
// job: it is counted, written to the journal as dropped and logged.
func (d *engineDeps) enqueueOutbound(job outboundJob) bool {
	if o := d.outSpool; o != nil {
		err := o.put(job)
		if err == nil {
			d.stats.outbound.queued.Add(1)
			return true
		}
		if d.log != nil {
			d.log.Warn("outbound spool refused the job, it goes to memory", "cid", job.CID, "err", err)
		}
	}
	select {
	case d.outbound <- job:
		d.stats.outbound.queued.Add(1)
		return true
	default:
		d.stats.outbound.dropped.Add(1)
		d.notePostback(job.CID, job.Status, "dropped", "out", job.Payout)
		if d.log != nil {
			d.log.Warn("outbound postback dropped", "cid", job.CID)
		}
		return false
	}
}

// templatesFor collects the outbound templates of grp.
// grp is the campaign. Local Postbacks come first. Each PostbackIDs entry is
// then read from d.s2s when that book is non-nil. A nil d or a nil book skips
// catalog ids. Templates that share Signature are kept once; the earlier copy wins.
// missing is how many catalog ids were absent from a non-nil book.
// There is no error. Each missing id is logged when d.log is set.
func (d *engineDeps) templatesFor(grp config.Group) ([]config.OutboundPostback, int) {
	out := make([]config.OutboundPostback, 0, len(grp.Postbacks)+len(grp.PostbackIDs))
	seen := make(map[string]struct{}, len(grp.Postbacks)+len(grp.PostbackIDs))
	add := func(p config.OutboundPostback) {
		sig := p.Signature()
		if _, ok := seen[sig]; ok {
			return
		}
		seen[sig] = struct{}{}
		out = append(out, p)
	}
	for _, p := range grp.Postbacks {
		add(p)
	}
	if d == nil || d.s2s == nil {
		return out, 0
	}
	missing := 0
	for _, id := range grp.PostbackIDs {
		if id == "" {
			continue
		}
		pb, ok := d.s2s.Get(id)
		if !ok {
			missing++
			if d.log != nil {
				d.log.Warn("s2s template missing", "id", id, "group", grp.ID)
			}
			continue
		}
		add(pb)
	}
	return out, missing
}

// newOutboundClient builds the default HTTP client for outbound postbacks.
// It uses a guarded dialer (security.GuardDial) and no proxy, so postbacks
// cannot be redirected to private or internal addresses.
func newOutboundClient() *http.Client {
	dialer := &net.Dialer{
		Timeout:   5 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   security.GuardDial,
	}
	tr := &http.Transport{
		Proxy:               nil,
		DialContext:         dialer.DialContext,
		TLSHandshakeTimeout: 5 * time.Second,
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     60 * time.Second,
	}
	return &http.Client{
		Timeout:   10 * time.Second,
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("too many redirects")
			}
			if !outboundURLAllowed(req.URL.String()) {
				return errors.New("redirect blocked")
			}
			return nil
		},
	}
}

// serveOutbound sends queued postbacks until ctx is cancelled.
// It is the only sender; cron does not run a second one.
func (d *engineDeps) serveOutbound(ctx context.Context) {
	client := newOutboundClient()
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-d.outbound:
			// Both sides of the select can be ready while the process is
			// stopping. Sending then can outlast the stop budget, and a second
			// job would be sent again after the restart. Leave it on disk.
			if ctx.Err() != nil {
				return
			}
			if d.sendOutbound(client, job) || !d.deferOutbound(job) {
				d.finishOutbound(job)
			}
		}
	}
}

// openCatalogs loads offers, landings, traffic sources and domains from the groups directory.
// Explicit EMTDS_*_FILE paths override that default. Missing files stay empty.
// The domain book is read when a redirect is checked for a path token.
func openCatalogs(groupsFile string, log *slog.Logger) (*config.Book[config.Offer], *config.Book[config.Landing], *config.Book[config.TrafficSource], *config.Book[config.AffiliateNetwork], *config.Book[config.Domain]) {
	offers := mustBook(catalogPath(groupsFile, "EMTDS_OFFERS_FILE", "offers.json"), func(o config.Offer) string { return o.ID }, log)
	landings := mustBook(catalogPath(groupsFile, "EMTDS_LANDINGS_FILE", "landings.json"), func(o config.Landing) string { return o.ID }, log)
	sources := mustBook(catalogPath(groupsFile, "EMTDS_SOURCES_FILE", "sources.json"), func(o config.TrafficSource) string { return o.ID }, log)
	networks := mustBook(catalogPath(groupsFile, "EMTDS_NETWORKS_FILE", "networks.json"), func(o config.AffiliateNetwork) string { return o.ID }, log)
	domains := mustBook(catalogPath(groupsFile, "EMTDS_DOMAINS_FILE", "domains.json"), func(o config.Domain) string { return o.ID }, log)
	return offers, landings, sources, networks, domains
}

// catalogPath returns a catalog file path beside the groups file.
// groupsFile is the groups.json path. env is the override variable; a non-empty value wins.
// name is the sibling file name used when env is empty.
// It returns an empty string when both env and groupsFile are empty.
// There is no error.
func catalogPath(groupsFile, env, name string) string {
	if v := config.Env(env); v != "" {
		return v
	}
	if groupsFile == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(groupsFile), name)
}

// openS2S loads the shared postback catalog beside the groups file.
// groupsFile is the groups.json path. log receives a warning when the file cannot be parsed.
// EMTDS_S2S_FILE overrides the path. A missing file is an empty book.
// The return is never nil. A parse error yields an empty book after the warning.
func openS2S(groupsFile string, log *slog.Logger) *config.Book[config.OutboundPostback] {
	return mustBook(catalogPath(groupsFile, "EMTDS_S2S_FILE", "s2s.json"), func(p config.OutboundPostback) string { return p.ID }, log)
}

func mustBook[T any](path string, idOf func(T) string, log *slog.Logger) *config.Book[T] {
	b, err := config.OpenBook(path, idOf)
	if err != nil && log != nil {
		log.Warn("catalog not loaded", "path", path, "err", err)
		b, _ = config.OpenBook("", idOf)
	}
	return b
}

// reloadCatalogs picks up catalog edits on the same interval as the groups file.
func reloadCatalogs(ctx context.Context, every time.Duration, books ...interface{ Reload() error }) {
	if every <= 0 {
		every = time.Minute
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for _, b := range books {
				if b != nil {
					_ = b.Reload()
				}
			}
		}
	}
}

// sendOutbound sends one queued job with client and records the result in the
// postback log and in the outbound counters. It returns whether the outcome is
// final, so the disk copy may be removed. A blocked address, a request that
// cannot be built and an answer from 400 to 499 are final failures. An answer
// below 400 is a final success. A transport error and an answer of 500 or above
// are not final: the network did not accept the job, and the caller retries it.
// A nil d returns true and sends nothing.
func (d *engineDeps) sendOutbound(client *http.Client, job outboundJob) bool {
	if d == nil {
		return true
	}
	if !outboundURLAllowed(job.URL) {
		d.stats.outbound.failed.Add(1)
		d.notePostback(job.CID, job.Status, "error", "out", job.Payout)
		return true
	}
	req, err := http.NewRequest(job.Method, job.URL, nil)
	if err != nil {
		d.stats.outbound.failed.Add(1)
		d.notePostback(job.CID, job.Status, "error", "out", job.Payout)
		if d.log != nil {
			d.log.Warn("outbound postback skipped", "err", err)
		}
		return true
	}
	resp, err := client.Do(req)
	if err != nil {
		d.stats.outbound.failed.Add(1)
		if errors.Is(err, security.ErrPrivateAddr) {
			d.notePostback(job.CID, job.Status, "blocked", "out", job.Payout)
			if d.log != nil {
				host := job.URL
				if u, e := url.Parse(job.URL); e == nil {
					host = u.Hostname()
				}
				d.log.Warn("outbound postback blocked (private address)", "host", host)
			}
			return true
		}
		d.notePostback(job.CID, job.Status, "error", "out", job.Payout)
		if d.log != nil {
			d.log.Warn("outbound postback failed", "err", err)
		}
		return false
	}
	_ = resp.Body.Close()
	if resp.StatusCode >= 500 {
		d.stats.outbound.failed.Add(1)
		d.notePostback(job.CID, job.Status, strconv.Itoa(resp.StatusCode), "out", job.Payout)
		if d.log != nil {
			d.log.Warn("outbound postback rejected", "status", resp.StatusCode)
		}
		return false
	}
	if resp.StatusCode >= 400 {
		d.stats.outbound.failed.Add(1)
		d.notePostback(job.CID, job.Status, strconv.Itoa(resp.StatusCode), "out", job.Payout)
		if d.log != nil {
			d.log.Warn("outbound postback rejected", "status", resp.StatusCode)
		}
		return true
	}
	d.stats.outbound.sent.Add(1)
	d.notePostback(job.CID, job.Status, strconv.Itoa(resp.StatusCode), "out", job.Payout)
	return true
}

// outboundURLAllowed reports whether an outbound postback may be sent to raw.
// Loopback, link-local, private addresses, known internal service names, and
// any hostname without a dot (which cannot be a public FQDN) are refused.
// IPv6 literals in brackets are unbracketed by url.Hostname and parsed by
// netip.ParseAddr, so they are never rejected by the no-dot check.
func outboundURLAllowed(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return false
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" {
		return false
	}
	switch host {
	case "localhost", "clickhouse", "redis", "engine", "php", "admin", "cron":
		return false
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		// Not an IP literal: reject any single-label name (no dot = internal).
		if !strings.Contains(host, ".") {
			return false
		}
		return true
	}
	ip = ip.Unmap()
	return !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() && !ip.IsUnspecified()
}
