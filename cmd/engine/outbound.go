package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/render"
	"github.com/egerkuzma/kuztds/internal/router"
)

// outboundJob is one server-to-server request. The URL is already expanded.
type outboundJob struct {
	Method string
	URL    string
	CID    string
	Status string
	Payout float64
}

// catalogs copies the offer and landing books into the maps the router reads.
func (d *engineDeps) catalogs() router.Catalogs {
	cat := router.Catalogs{
		Offers:   map[string]config.Offer{},
		Landings: map[string]config.Landing{},
	}
	if d != nil {
		for _, o := range d.offers.All() {
			cat.Offers[o.ID] = o
		}
		for _, l := range d.landings.All() {
			cat.Landings[l.ID] = l
		}
	}
	return cat
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
// Local postbacks and ids from the s2s catalog are both included.
// A missing catalog id counts as a match that was not queued.
// A full queue drops the job so the inbound pixel is never blocked.
// It reports false only when a matching template existed and none was queued.
func (d *engineDeps) fireOutbound(groupID, cid string, profit float64, status string, q url.Values) bool {
	if d == nil || d.outbound == nil || d.groups == nil || groupID == "" {
		return true
	}
	grp, ok := d.groups.Get(groupID)
	if !ok {
		return true
	}
	list, missing := d.templatesFor(*grp)
	_, subs := config.ApplySource(q, nil)
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
				CID: cid, Status: status, Payout: payout, SubIDs: subs,
			}),
			CID: cid, Status: status, Payout: profit,
		}
		select {
		case d.outbound <- job:
			queued++
		default:
			d.notePostback(cid, status, "dropped", "out", profit)
			if d.log != nil {
				d.log.Warn("outbound postback dropped", "cid", cid)
			}
		}
	}
	if matched == 0 {
		return true
	}
	return queued > 0
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

// serveOutbound sends queued postbacks until ctx is cancelled.
// It is the only sender; cron does not run a second one.
func (d *engineDeps) serveOutbound(ctx context.Context) {
	client := &http.Client{
		Timeout: 5 * time.Second,
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
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-d.outbound:
			d.sendOutbound(client, job)
		}
	}
}

// openCatalogs loads offers, landings and traffic sources from the groups directory.
// Explicit KUZTDS_*_FILE paths override that default. Missing files stay empty.
func openCatalogs(groupsFile string, log *slog.Logger) (*config.Book[config.Offer], *config.Book[config.Landing], *config.Book[config.TrafficSource], *config.Book[config.AffiliateNetwork]) {
	offers := mustBook(catalogPath(groupsFile, "KUZTDS_OFFERS_FILE", "offers.json"), func(o config.Offer) string { return o.ID }, log)
	landings := mustBook(catalogPath(groupsFile, "KUZTDS_LANDINGS_FILE", "landings.json"), func(o config.Landing) string { return o.ID }, log)
	sources := mustBook(catalogPath(groupsFile, "KUZTDS_SOURCES_FILE", "sources.json"), func(o config.TrafficSource) string { return o.ID }, log)
	networks := mustBook(catalogPath(groupsFile, "KUZTDS_NETWORKS_FILE", "networks.json"), func(o config.AffiliateNetwork) string { return o.ID }, log)
	return offers, landings, sources, networks
}

// catalogPath returns a catalog file path beside the groups file.
// groupsFile is the groups.json path. env is the override variable; a non-empty value wins.
// name is the sibling file name used when env is empty.
// It returns an empty string when both env and groupsFile are empty.
// There is no error.
func catalogPath(groupsFile, env, name string) string {
	if v := os.Getenv(env); v != "" {
		return v
	}
	if groupsFile == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(groupsFile), name)
}

// openS2S loads the shared postback catalog beside the groups file.
// groupsFile is the groups.json path. log receives a warning when the file cannot be parsed.
// KUZTDS_S2S_FILE overrides the path. A missing file is an empty book.
// The return is never nil. A parse error yields an empty book after the warning.
func openS2S(groupsFile string, log *slog.Logger) *config.Book[config.OutboundPostback] {
	return mustBook(catalogPath(groupsFile, "KUZTDS_S2S_FILE", "s2s.json"), func(p config.OutboundPostback) string { return p.ID }, log)
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
func (d *engineDeps) sendOutbound(client *http.Client, job outboundJob) {
	if d == nil {
		return
	}
	if !outboundURLAllowed(job.URL) {
		d.notePostback(job.CID, job.Status, "error", "out", job.Payout)
		return
	}
	req, err := http.NewRequest(job.Method, job.URL, nil)
	if err != nil {
		d.notePostback(job.CID, job.Status, "error", "out", job.Payout)
		if d.log != nil {
			d.log.Warn("outbound postback skipped", "err", err)
		}
		return
	}
	resp, err := client.Do(req)
	if err != nil {
		d.notePostback(job.CID, job.Status, "error", "out", job.Payout)
		if d.log != nil {
			d.log.Warn("outbound postback failed", "err", err)
		}
		return
	}
	_ = resp.Body.Close()
	d.notePostback(job.CID, job.Status, strconv.Itoa(resp.StatusCode), "out", job.Payout)
	if resp.StatusCode >= 400 && d.log != nil {
		d.log.Warn("outbound postback rejected", "status", resp.StatusCode)
	}
}

// outboundURLAllowed reports whether an outbound postback may be sent to raw.
// Loopback, link-local, private addresses and the local service names are refused.
func outboundURLAllowed(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return false
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	switch host {
	case "localhost", "clickhouse", "redis", "engine":
		return false
	}
	if host == "" {
		return false
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return true
	}
	ip = ip.Unmap()
	return !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() && !ip.IsUnspecified()
}
