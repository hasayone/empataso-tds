package main

import (
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/store"
)

const (
	// journalMask replaces the value of a secret parameter and the key segment of a path.
	journalMask = "***"
	// journalMaxParams is the most parameters kept from one postback.
	journalMaxParams = 50
	// journalMaxName and journalMaxValue cap a parameter name and value, in bytes.
	journalMaxName  = 64
	journalMaxValue = 256
	// journalMaxPath and journalMaxUA cap the stored path and User-Agent, in bytes.
	journalMaxPath = 256
	journalMaxUA   = 512
	// journalRejectPerSecond is the most rejected postbacks (bad key, bad payout,
	// wrong method) journaled in one second; the rest are only counted in stats,
	// so a flood of bad requests cannot push accepted ones out of the journal.
	journalRejectPerSecond = 20
)

// journalSecretParts are parameter names, or parts of names split on "_", "-"
// and ".", whose value is a secret.
var journalSecretParts = map[string]bool{
	"key": true, "token": true, "secret": true, "sign": true, "signature": true, "sig": true,
	"hash": true, "hmac": true, "password": true, "pass": true, "passwd": true, "pwd": true,
	"auth": true, "apikey": true, "apitoken": true, "accesstoken": true, "authtoken": true,
}

// journalSecretSuffixes are name endings that mark a secret in names written
// together, like accessToken or clientSecret.
var journalSecretSuffixes = []string{"token", "secret", "signature", "password", "apikey"}

// journalSecretName reports whether a postback parameter named name carries a
// secret. The comparison ignores case; "keyword" is not a secret, "api_key" is.
func journalSecretName(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	for _, part := range strings.FieldsFunc(n, func(r rune) bool { return r == '_' || r == '-' || r == '.' }) {
		if journalSecretParts[part] {
			return true
		}
	}
	for _, suf := range journalSecretSuffixes {
		if strings.HasSuffix(n, suf) {
			return true
		}
	}
	return false
}

// maskPostbackParams returns q as a query string for the journal: names sorted,
// at most journalMaxParams of them, names and values cut to their caps. A value
// of a secret parameter, and any value equal to one of secrets, is journalMask.
// Empty strings in secrets are ignored.
func maskPostbackParams(q url.Values, secrets ...string) string {
	names := make([]string, 0, len(q))
	for name := range q {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) > journalMaxParams {
		names = names[:journalMaxParams]
	}
	out := url.Values{}
	for _, name := range names {
		key := config.LimitBytes(name, journalMaxName)
		for _, v := range q[name] {
			if journalSecretName(name) || isSecretValue(v, secrets) {
				v = journalMask
			}
			out.Add(key, config.LimitBytes(v, journalMaxValue))
		}
	}
	return out.Encode()
}

// isSecretValue reports whether v equals one of the non-empty secrets.
func isSecretValue(v string, secrets []string) bool {
	for _, s := range secrets {
		if s != "" && v == s {
			return true
		}
	}
	return false
}

// journalPath returns the request path for the journal. The Keitaro form
// /{key}/postback has the key in the path, so it becomes /***/postback.
func journalPath(r *http.Request, pathKey string) string {
	if pathKey != "" {
		return "/" + journalMask + "/postback"
	}
	return config.LimitBytes(r.URL.Path, journalMaxPath)
}

// rejectGate lets through at most journalRejectPerSecond rejected lines a second.
type rejectGate struct {
	mu     sync.Mutex
	second int64
	n      int
}

// allow reports whether one more rejected line may be journaled at now.
func (g *rejectGate) allow(now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if sec := now.Unix(); sec != g.second {
		g.second, g.n = sec, 0
	}
	if g.n >= journalRejectPerSecond {
		return false
	}
	g.n++
	return true
}

// journalIn queues the journal line of one inbound postback. r is the request,
// pathKey the key of a /{key}/postback path or "", q the parameters read, in what
// parsePostback made of them, outcome the result and res what the store reported
// (zero when the conversion was not stored). Rejected outcomes pass the reject
// gate of the journal writer first. The line is queued without waiting (see notePostback); without a
// journal writer nothing happens. It does not fail.
func (d *engineDeps) journalIn(r *http.Request, pathKey string, q url.Values, in postbackIn, outcome string, res store.ConversionResult) {
	if d == nil || d.plog == nil {
		return
	}
	switch outcome {
	case store.PostbackOutcomeBadKey, store.PostbackOutcomeInvalid, store.PostbackOutcomeMethod:
		if !d.plog.rejects.allow(time.Now()) {
			return
		}
	}
	d.plog.note(store.PostbackLogRow{
		Ts: time.Now(), Subid: in.cid, Status: in.status, Payout: in.profit, Outcome: outcome, Direction: "in",
		IP: senderOf(r), Network: res.NetworkID, Method: r.Method, Path: journalPath(r, pathKey),
		Params:    maskPostbackParams(q, d.pbAuth.secret, d.postbackKey, pathKey),
		UserAgent: config.LimitBytes(r.UserAgent(), journalMaxUA), CampaignID: res.GroupID,
		Duplicate: outcome == "ok" && !res.Fresh,
	})
}
