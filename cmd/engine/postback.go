package main

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func firstNonEmpty(q url.Values, keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(q.Get(k)); v != "" {
			return v
		}
	}
	return ""
}

// parsePostback reads the public /postback query.
// The click id is the first non-empty of subid, clickid and cid.
// The status is the first non-empty of status, type and goal, folded to lower case.
// The payout is the first value that parses as a number among payout, profit and sum.
// The old ?pb= pixel does not call this function and still reads only cid, profit and status.
func parsePostback(q url.Values) (cid string, profit float64, status string) {
	cid = firstNonEmpty(q, "subid", "clickid", "cid")
	status = strings.ToLower(firstNonEmpty(q, "status", "type", "goal"))
	for _, k := range []string{"payout", "profit", "sum"} {
		raw := strings.TrimSpace(q.Get(k))
		if raw == "" {
			continue
		}
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			continue
		}
		profit = v
		break
	}
	return cid, profit, status
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
	cid, profit, status := parsePostback(q)
	outcome := "ok"
	gid := ""
	if cid == "" {
		outcome = "no_subid"
	} else if d == nil || d.chConn == nil {
		outcome = "no_store"
	} else {
		var err error
		gid, err = d.chConn.RecordPostback(r.Context(), cid, profit, status)
		if err != nil {
			outcome = "click_not_found"
			if d.log != nil {
				d.log.Warn("postback failed", "cid", cid, "err", err)
			}
		} else if d.markOutbound(cid, status) {
			d.fireOutbound(gid, cid, profit, status, q)
		}
	}
	if d != nil {
		d.notePostback(cid, status, outcome, "in", profit)
	}
	w.WriteHeader(http.StatusOK)
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

// notePostback writes one inbound or outbound line. The key and the raw query
// are not stored. A missing store is ignored.
func (d *engineDeps) notePostback(subid, status, outcome, direction string, payout float64) {
	if d == nil || d.chConn == nil {
		return
	}
	_ = d.chConn.WritePostbackLog(context.Background(), subid, status, outcome, direction, payout)
}
