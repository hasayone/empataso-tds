package main

import (
	"context"
	"log/slog"
	"net/http"
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
// A full queue drops the job so the inbound pixel is never blocked.
func (d *engineDeps) fireOutbound(groupID, cid string, profit float64, status string, q url.Values) {
	if d == nil || d.outbound == nil || d.groups == nil || groupID == "" {
		return
	}
	grp, ok := d.groups.Get(groupID)
	if !ok {
		return
	}
	_, subs := config.ApplySource(q, nil)
	payout := strconv.FormatFloat(profit, 'f', -1, 64)
	for _, pb := range grp.Postbacks {
		if !pb.MatchesStatus(status) {
			continue
		}
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
		default:
			d.notePostback(cid, status, "dropped", "out", profit)
			if d.log != nil {
				d.log.Warn("outbound postback dropped", "cid", cid)
			}
		}
	}
}

// serveOutbound sends queued postbacks until ctx is cancelled.
// It is the only sender; cron does not run a second one.
func (d *engineDeps) serveOutbound(ctx context.Context) {
	client := &http.Client{Timeout: 5 * time.Second}
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
	dir := ""
	if groupsFile != "" {
		dir = filepath.Dir(groupsFile)
	}
	path := func(env, name string) string {
		if v := os.Getenv(env); v != "" {
			return v
		}
		if dir == "" {
			return ""
		}
		return filepath.Join(dir, name)
	}
	offers := mustBook(path("KUZTDS_OFFERS_FILE", "offers.json"), func(o config.Offer) string { return o.ID }, log)
	landings := mustBook(path("KUZTDS_LANDINGS_FILE", "landings.json"), func(o config.Landing) string { return o.ID }, log)
	sources := mustBook(path("KUZTDS_SOURCES_FILE", "sources.json"), func(o config.TrafficSource) string { return o.ID }, log)
	networks := mustBook(path("KUZTDS_NETWORKS_FILE", "networks.json"), func(o config.AffiliateNetwork) string { return o.ID }, log)
	return offers, landings, sources, networks
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
	if !strings.HasPrefix(job.URL, "https://") && !strings.HasPrefix(job.URL, "http://") {
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
