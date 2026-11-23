package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/detect"
	"github.com/hasayone/empataso-tds/internal/geo"
	"github.com/hasayone/empataso-tds/internal/router"
	"github.com/hasayone/empataso-tds/internal/server"
	"github.com/hasayone/empataso-tds/internal/store"
)

const (
	// trafficQueue is how many click paths wait for a writer. A full queue drops
	// the new path and counts it; the click itself never waits.
	trafficQueue = 2048
	// trafficWriters is the number of goroutines that write paths to Redis.
	trafficWriters = 2
	// trafficWriteTimeout bounds the Redis calls of one path.
	trafficWriteTimeout = time.Second
	// trafficWarnEvery is the least pause between two warnings about lost paths.
	trafficWarnEvery = time.Minute
)

// trafficLogger writes click paths to Redis off the request path.
//
// The request goroutine builds a small job and offers it without blocking; the
// writers resolve the full reasons of rejected streams, pack the path and store
// it. A slow or absent Redis loses paths, never clicks.
type trafficLogger struct {
	log   *slog.Logger
	store *store.TrafficLog
	deps  router.Deps
	jobs  chan trafficJob

	written, dropped, failed atomic.Int64
	warnedAt                 atomic.Int64
}

// trafficJob is one click path as the request goroutine leaves it.
type trafficJob struct {
	trace  router.Trace
	hops   []router.HopDraft
	groups []string
}

// trafficStats is the snapshot of the logger counters.
type trafficStats = config.TrafficLogMetrics

// newTrafficLogger returns a logger on st. deps carries the IP lists the
// reasons of rejected streams read. It returns nil when st keeps nothing, so a
// nil logger means the click path is off.
func newTrafficLogger(st *store.TrafficLog, deps router.Deps, log *slog.Logger) *trafficLogger {
	if !st.Enabled() {
		return nil
	}
	return &trafficLogger{log: log, store: st, deps: deps, jobs: make(chan trafficJob, trafficQueue)}
}

// on reports whether the click path of grp is recorded: the logger exists and
// the campaign has not switched it off. A nil logger or campaign is off.
func (l *trafficLogger) on(grp *config.Group) bool {
	return l != nil && grp != nil && !grp.TrafficLogOff
}

// offer queues a job without waiting. It returns false and counts a drop when
// the queue is full. A nil logger accepts nothing.
func (l *trafficLogger) offer(j trafficJob) bool {
	if l == nil {
		return false
	}
	select {
	case l.jobs <- j:
		return true
	default:
		l.noteDrop()
		return false
	}
}

// noteDrop counts one lost path and warns at most once a minute.
func (l *trafficLogger) noteDrop() {
	n := l.dropped.Add(1)
	now := time.Now().UnixNano()
	last := l.warnedAt.Load()
	if now-last >= int64(trafficWarnEvery) && l.warnedAt.CompareAndSwap(last, now) {
		l.log.Warn("traffic log queue full, click paths are dropped", "dropped", n)
	}
}

// run starts the writers and returns when ctx ends. Paths still queued are
// dropped: the log is a diagnostic aid, not a record.
func (l *trafficLogger) run(ctx context.Context, spawn func(func())) {
	for i := 0; i < trafficWriters; i++ {
		spawn(func() { l.write(ctx) })
	}
}

// write is one writer loop.
func (l *trafficLogger) write(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-l.jobs:
			l.persist(ctx, j)
		}
	}
}

// persist resolves, packs and stores one job, counting the outcome.
func (l *trafficLogger) persist(ctx context.Context, j trafficJob) {
	t := j.trace
	for _, h := range j.hops {
		t.Hops = append(t.Hops, h.Hop(l.deps))
	}
	t.Seal()
	b, err := json.Marshal(&t)
	if err != nil {
		l.failed.Add(1)
		return
	}
	wctx, cancel := context.WithTimeout(ctx, trafficWriteTimeout)
	defer cancel()
	if err := l.store.Push(wctx, j.groups, t.CID, b); err != nil {
		if l.failed.Add(1) == 1 {
			l.log.Warn("traffic log write failed", "err", err)
		}
		return
	}
	l.written.Add(1)
}

// stats returns the counters. A nil logger reports zeros.
func (l *trafficLogger) stats() trafficStats {
	if l == nil {
		return trafficStats{}
	}
	return trafficStats{Written: l.written.Load(), Dropped: l.dropped.Load(), Failed: l.failed.Load()}
}

// clickPath is what root() knows about a click when it answers, gathered in one
// place so the trace is built in one call.
type clickPath struct {
	r        *http.Request
	began    time.Time
	apiMode  bool
	entered  *config.Group // the campaign the visitor entered
	served   *config.Group // the campaign that finally served
	cid      string
	step     bool // the /offer step
	ip       netip.Addr
	geo      geo.Geo
	info     detect.Info
	v        router.Visitor
	country  string
	operator string
	bot      string
	botWhy   string
	botServe bool
	unique   bool
	subs     [config.MaxSubID]string
	via      string
	stream   string
	action   string
	out      string
	offerID  string
	landing  string
	network  string
	saved    bool
	hops     []router.HopDraft
	notes    []string
}

// trace builds the path of the click. It copies values out of the request and
// never reads a cookie or the Authorization header; secret query values are
// masked.
func (p clickPath) trace() router.Trace {
	r := p.r
	scheme := "http"
	if server.IsHTTPS(r) {
		scheme = "https"
	}
	t := router.Trace{
		TS: p.began, CID: p.cid, Campaign: p.served.ID, Name: p.served.Name,
		URL: router.MaskURL(scheme + "://" + r.Host + r.URL.RequestURI()),
		Headers: []router.HeaderLine{
			{Name: "x-forwarded-for", Value: strings.Join(r.Header.Values("X-Forwarded-For"), ", ")},
			{Name: "cf-connecting-ip", Value: r.Header.Get("CF-Connecting-IP")},
			{Name: "x-real-ip", Value: r.Header.Get("X-Real-IP")},
		},
		Trusted: server.ViaTrustedProxy(r.Context()), IP: p.ip.String(),
		Stream: p.stream, Via: p.via, Action: p.action, Out: p.out,
		OfferID: p.offerID, Landing: p.landing, Network: p.network,
		Notes: p.notes, Saved: p.saved, DurUS: time.Since(p.began).Microseconds(),
	}
	if p.step {
		t.Step = 1
	}
	if p.apiMode {
		t.Notes = append(t.Notes, "api")
	}
	if p.botServe {
		t.Notes = append(t.Notes, "bot_served")
	}
	g, v := p.geo, p.v
	t.Visitor = router.VisitorInfo{
		Country: dashEmpty(p.country), Region: dashEmpty(g.Region), City: dashEmpty(g.City), Timezone: dashEmpty(g.Timezone),
		ASN: g.ASN, Org: dashEmpty(g.Org), Operator: dashEmpty(p.operator),
		Device: p.info.Device, OS: p.info.OS, OSVersion: p.info.OSVersion,
		Browser: p.info.Browser, BrowserVer: p.info.BrowserVer, Brand: p.info.Brand, Model: p.info.Model,
		Lang: dashEmpty(v.Lang), Referer: dashEmpty(v.Referer), Keyword: v.Key, UserAgent: v.UA,
		Bot: p.bot, BotWhy: p.botWhy, Proxy: v.Proxy, Unique: p.unique,
	}
	for i, s := range p.subs {
		if s == "" {
			continue
		}
		if t.Visitor.Subs == nil {
			t.Visitor.Subs = map[string]string{}
		}
		t.Visitor.Subs["sub_id_"+strconv.Itoa(i+1)] = s
	}
	return t
}

// groupIDs returns the distinct campaign ids a click path is stored under: the
// one that served and the one the visitor entered, each only when it records.
func (p clickPath) groupIDs(l *trafficLogger) []string {
	var ids []string
	if l.on(p.served) {
		ids = append(ids, p.served.ID)
	}
	if p.entered != nil && p.entered != p.served && l.on(p.entered) {
		ids = append(ids, p.entered.ID)
	}
	return ids
}

// recordClick queues the path of a click. It does nothing when the campaigns
// that handled it do not record. It never blocks.
func (d *engineDeps) recordClick(p clickPath) {
	if d == nil {
		return
	}
	ids := p.groupIDs(d.traffic)
	if len(ids) == 0 {
		return
	}
	d.traffic.offer(trafficJob{trace: p.trace(), hops: p.hops, groups: ids})
}
