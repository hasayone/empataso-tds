package main

import (
	"context"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/store"
)

// postbackOriginalMax is the stored length of the raw postback status.
const postbackOriginalMax = 64

// postbackTIDMax is the stored length of the network transaction id.
const postbackTIDMax = 128

// postbackIn is one public /postback query.
// cid is the click id. profit is the payout. status is folded to lower
// case. original is that status before folding, at most 64 bytes. tid is
// the transaction id, at most 128 bytes. ok is false when a payout alias
// parses as NaN or Inf.
type postbackIn struct {
	cid              string
	profit           float64
	status, original string
	tid              string
	ok               bool
}

func firstNonEmpty(q url.Values, keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(q.Get(k)); v != "" {
			return v
		}
	}
	return ""
}

// parsePostback reads the public /postback query.
// q is the query or the parsed form. The click id is the first non-empty
// of subid, clickid and cid. The status is the first non-empty of status,
// type and goal, folded to lower case. original is that value before
// folding, cut to 64 bytes. tid is the tid parameter, cut to 128 bytes.
// The payout is the first value that parses as a finite number among
// payout, profit and sum. ok is false when one of those fields parses as
// NaN or Inf. The old ?pb= pixel does not call this function.
func parsePostback(q url.Values) postbackIn {
	rawStatus := firstNonEmpty(q, "status", "type", "goal")
	in := postbackIn{
		cid:      firstNonEmpty(q, "subid", "clickid", "cid"),
		status:   strings.ToLower(rawStatus),
		original: config.LimitBytes(rawStatus, postbackOriginalMax),
		tid:      config.LimitBytes(strings.TrimSpace(q.Get("tid")), postbackTIDMax),
		ok:       true,
	}
	for _, k := range []string{"payout", "profit", "sum"} {
		raw := strings.TrimSpace(q.Get(k))
		if raw == "" {
			continue
		}
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			continue
		}
		if math.IsNaN(v) || math.IsInf(v, 0) {
			in.ok = false
			return in
		}
		in.profit = v
		break
	}
	return in
}

// postback handles GET and POST /postback. It does not require the pixel key.
// A repeat of the same click id and status is stored again but does not queue
// a second outbound request. The network postback URL is never called.
func (d *engineDeps) postback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	if r.Method == http.MethodPost {
		_ = r.ParseForm()
		if len(r.Form) > 0 {
			q = r.Form
		}
	}
	in := parsePostback(q)
	if !in.ok {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	outcome := "ok"
	gid := ""
	if in.cid == "" {
		outcome = "no_subid"
	} else if d == nil || d.chConn == nil {
		outcome = "no_store"
	} else {
		var err error
		gid, err = d.chConn.RecordConversion(r.Context(), store.PostbackIn{
			CID: in.cid, Profit: in.profit, Status: in.status,
			Original: in.original, TID: in.tid,
		})
		if err != nil {
			outcome = "click_not_found"
			if d.log != nil {
				d.log.Warn("postback failed", "cid", in.cid, "err", err)
			}
		} else {
			d.queueOutbound(gid, in.cid, in.profit, in.status, q)
		}
	}
	if d != nil {
		d.notePostback(in.cid, in.status, outcome, "in", in.profit)
	}
	w.WriteHeader(http.StatusOK)
}

// queueOutbound sends the campaign templates once for this click and status.
// A full queue removes the mark so a later pixel can try again.
func (d *engineDeps) queueOutbound(groupID, cid string, profit float64, status string, q url.Values) {
	if d == nil || !d.markOutbound(cid, status) {
		return
	}
	if !d.fireOutbound(groupID, cid, profit, status, q) {
		d.unmarkOutbound(cid, status)
	}
}

// markOutbound reports whether this click id and status may queue an outbound
// postback. The first call wins; a later call with the same pair returns false.
func (d *engineDeps) markOutbound(cid, status string) bool {
	if d == nil {
		return false
	}
	key := cid + "\x00" + status
	d.sentMu.Lock()
	defer d.sentMu.Unlock()
	if d.sent == nil {
		d.sent = map[string]struct{}{}
	}
	if _, ok := d.sent[key]; ok {
		return false
	}
	d.sent[key] = struct{}{}
	return true
}

// unmarkOutbound forgets a click and status whose jobs were all dropped.
func (d *engineDeps) unmarkOutbound(cid, status string) {
	if d == nil {
		return
	}
	d.sentMu.Lock()
	defer d.sentMu.Unlock()
	delete(d.sent, cid+"\x00"+status)
}

// notePostback writes one inbound or outbound line. The key and the raw query
// are not stored. A missing store is ignored.
func (d *engineDeps) notePostback(subid, status, outcome, direction string, payout float64) {
	if d == nil || d.chConn == nil {
		return
	}
	_ = d.chConn.WritePostbackLog(context.Background(), subid, status, outcome, direction, payout)
}
