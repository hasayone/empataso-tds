package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/server"
	"github.com/hasayone/empataso-tds/internal/store"
)

// postbackOriginalMax is the stored length of the raw postback status.
const postbackOriginalMax = 64

// postbackTIDMax is the stored length of the network transaction id.
const postbackTIDMax = 128

// postbackIn is one public /postback query.
// cid is the click id. profit is the payout. depSum is the deposit and is
// not the payout. dep2Sum is not read from the query and stays 0. status is
// folded to lower case. original is that status before folding, at most 64
// bytes. tid is the transaction id, at most 128 bytes. ok is false when a
// payout alias or the deposit parses as NaN or Inf.
type postbackIn struct {
	cid              string
	profit           float64
	depSum           float64
	dep2Sum          float64
	status, original string
	tid              string
	ok               bool
	// payoutAbsent is true when no payout alias held a number, so profit is 0
	// only by default; a payout that was sent, even a zero, leaves it false.
	payoutAbsent bool
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
// payout, profit and sum. The deposit is the first non-empty of DepSum and
// depsum; an empty value stays 0 and a value that is not a number stays 0.
// dep2sum and Dep2Sum are not read. ok is false when a payout alias or the
// deposit parses as NaN or Inf. The old ?pb= pixel does not call this function.
func parsePostback(q url.Values) postbackIn {
	rawStatus := firstNonEmpty(q, "status", "type", "goal")
	in := postbackIn{
		cid:      firstNonEmpty(q, "subid", "clickid", "cid"),
		status:   strings.ToLower(rawStatus),
		original: config.LimitBytes(rawStatus, postbackOriginalMax),
		tid:      config.LimitBytes(strings.TrimSpace(q.Get("tid")), postbackTIDMax),
		ok:       true,

		payoutAbsent: true,
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
		in.payoutAbsent = false
		break
	}
	var ok bool
	if in.depSum, ok = parseDeposit(q, "DepSum", "depsum"); !ok {
		in.ok = false
	}
	return in
}

// parseDeposit reads one deposit amount from the query.
// q is the query. names are the accepted parameter names; the first one with a
// non-empty value is read. An absent or empty parameter and a value that is not
// a number give 0, so no deposit is invented. ok is false only for NaN and Inf.
func parseDeposit(q url.Values, names ...string) (v float64, ok bool) {
	raw := firstNonEmpty(q, names...)
	if raw == "" {
		return 0, true
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, true
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

// postbackOut is the JSON body of GET and POST /postback.
// Status is "success" or "error". Error is set only when Status is "error".
type postbackOut struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// writePostback writes the public /postback JSON body.
// w is the response. code is the HTTP status. errMsg is the public error
// text. An empty errMsg with code 200 writes {"status":"success"}.
// Any other case writes {"status":"error","error":errMsg}.
// A short write is ignored: the status line is already sent.
func writePostback(w http.ResponseWriter, code int, errMsg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if code == http.StatusMethodNotAllowed {
		w.Header().Set("Allow", "GET, POST")
	}
	w.WriteHeader(code)
	out := postbackOut{Status: "success"}
	if errMsg != "" {
		out.Status = "error"
		out.Error = errMsg
	}
	_ = json.NewEncoder(w).Encode(out)
}

// postbackResult maps one accepted postback to the HTTP code, the log outcome
// and the public error text. cid is the click id. hasStore is true when
// ClickHouse is connected. recErr is the RecordConversion error and is read
// only when cid is set and hasStore is true. code is 200 only when the row
// was stored. outcome is ok, no_subid, no_store, click_not_found or
// store_error. errMsg is empty when outcome is ok. A missing click is
// sql.ErrNoRows. Any other recErr is store_error and does not include the
// driver text.
func postbackResult(cid string, hasStore bool, recErr error) (code int, outcome, errMsg string) {
	if cid == "" {
		return http.StatusBadRequest, "no_subid", "subid is required"
	}
	if !hasStore {
		return http.StatusServiceUnavailable, "no_store", "store unavailable"
	}
	if recErr == nil {
		return http.StatusOK, "ok", ""
	}
	if errors.Is(recErr, sql.ErrNoRows) {
		return http.StatusNotFound, "click_not_found", "click not found"
	}
	return http.StatusServiceUnavailable, "store_error", "store unavailable"
}

// handlePostback is the internal implementation called by both /postback and
// the Keitaro /{key}/postback path in root. pathKey is the key extracted from
// the URL path segment; it overrides the "key" query or form parameter when
// non-empty. Every request is written to the postback journal with its sender,
// method, masked path and parameters and User-Agent (see journalIn): accepted
// ones with the click's campaign and network, rejected ones (wrong method, bad
// key, bad payout) with their reason. The journal never delays or changes the
// answer.
//
// The whole handler is bounded by postbackHandlerTimeout, and the store call in
// it by postbackStoreTimeout; the writes to the disk queue are not cancellable
// and wait for the disk. When the store fails, times out or is not connected, or
// when the click is not there yet and the click id is signed, the postback is
// written to the disk queue with its arrival time and the network gets 200:
// outcome queued for the first case, retry for the second. A queue that cannot
// take it leaves the answer as postbackResult gives it; when the cause is a full
// quota the outcome counted is queue_full (store failure) or retry_overflow
// (missing click) in place of the one postbackResult gave, and the answer is
// the same. An unsigned click id without a click is a final 404.
func (d *engineDeps) handlePostback(w http.ResponseWriter, r *http.Request, pathKey string) {
	q := r.URL.Query()
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		d.journalIn(r, pathKey, q, parsePostback(q), store.PostbackOutcomeMethod, store.ConversionResult{})
		writePostback(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if r.Method == http.MethodPost {
		_ = r.ParseForm()
		if len(r.Form) > 0 {
			q = r.Form
		}
	}
	// Auth check: key is taken from the URL path segment first, then from the
	// query or POST form. A rejected key is counted in stats as bad_key and
	// journaled with the key masked.
	if d != nil {
		authKey := pathKey
		if authKey == "" {
			authKey = q.Get("key")
		}
		switch d.pbAuth.check(authKey, d.log) {
		case authDenyBadKey:
			d.stats.postbacks.note("bad_key")
			d.journalIn(r, pathKey, q, parsePostback(q), store.PostbackOutcomeBadKey, store.ConversionResult{})
			writePostback(w, http.StatusForbidden, "bad key")
			return
		case authDenyNoSecret:
			d.stats.postbacks.note("bad_key")
			d.journalIn(r, pathKey, q, parsePostback(q), store.PostbackOutcomeBadKey, store.ConversionResult{})
			writePostback(w, http.StatusForbidden, "postback secret not set")
			return
		}
	}
	in := parsePostback(q)
	if !in.ok {
		if d != nil {
			d.stats.postbacks.note("invalid")
		}
		d.journalIn(r, pathKey, q, in, store.PostbackOutcomeInvalid, store.ConversionResult{})
		writePostback(w, http.StatusBadRequest, "invalid payout")
		return
	}
	at := time.Now()
	ctx, cancelAll := context.WithTimeout(r.Context(), postbackHandlerTimeout)
	defer cancelAll()
	cs := d.convStore()
	var recErr error
	var res store.ConversionResult
	if in.cid != "" && cs != nil {
		sctx, cancel := context.WithTimeout(ctx, postbackStoreTimeout)
		res, recErr = cs.RecordConversion(sctx, store.PostbackIn{
			CID: in.cid, Profit: in.profit, DepSum: in.depSum, Dep2Sum: in.dep2Sum, Status: in.status,
			Original: in.original, TID: in.tid, At: at,
			PayoutAbsent: in.payoutAbsent, OfferPayout: d.offerReserve, Upsell: d.offerUpsell,
		})
		cancel()
		if recErr != nil {
			if d.log != nil {
				d.log.Warn("postback failed", "cid", in.cid, "err", recErr)
			}
		} else {
			if res.ProfitFromOffer {
				in.profit = res.Profit
			}
			d.noteOfferCap(ctx, res, in.status)
			_, subs := config.ApplySource(q, nil)
			d.queueOutboundCtx(ctx, res, in.cid, in.profit, in.status, subs)
		}
	}
	code, outcome, errMsg := postbackResult(in.cid, cs != nil, recErr)
	if parked, ok := d.parkPostback(r, in, q, at, outcome); ok {
		code, outcome, errMsg = http.StatusOK, parked, ""
	} else {
		outcome = parked
	}
	if d != nil {
		d.stats.postbacks.note(outcome)
	}
	d.journalIn(r, pathKey, q, in, outcome, res)
	writePostback(w, code, errMsg)
}

// offerReserve is the reserve payout of an offer for a postback without one.
// offerID is the offer of the click. It returns the amount and true when the
// offer is in the catalog and Offer.ReservePayout gives one; an offer without
// the payout parameter tick, an unknown id and a nil d give false, so the
// payout stays what the postback carried.
func (d *engineDeps) offerReserve(offerID string) (float64, bool) {
	if d == nil || d.offers == nil {
		return 0, false
	}
	o, ok := d.offers.Get(offerID)
	if !ok {
		return 0, false
	}
	return o.ReservePayout()
}

// offerUpsell reports whether the offer counts a later sale with a new tid as
// another sale. offerID is the offer of the click. An unknown id and a nil d
// return false, so a repeat stays one sale.
func (d *engineDeps) offerUpsell(offerID string) bool {
	if d == nil || d.offers == nil {
		return false
	}
	o, ok := d.offers.Get(offerID)
	if !ok {
		return false
	}
	return o.Upsell
}

// parkPostback puts a postback that could not be settled now into the disk
// queue. outcome is what postbackResult decided. It returns the outcome to
// count and whether the postback was queued. When it was, the outcome is queued
// (the store failed, with a signed click id or within the small quota of
// unsigned ones) or retry (a signed click not found yet) and the network gets
// 200. When it was not, the answer stays as postbackResult gave it and the
// outcome is the one passed in, except for a full quota: queue_full for a store
// failure and retry_overflow for a missing click replace it, so the request is
// counted once. Not queued also means there is no queue, the click id is empty,
// the outcome is not one a queue can cure, the click id is not signed and its
// click is missing (final), or the disk refused the segment.
func (d *engineDeps) parkPostback(r *http.Request, in postbackIn, q url.Values, at time.Time, outcome string) (string, bool) {
	if d == nil || d.pbq == nil || in.cid == "" {
		return outcome, false
	}
	e := newSpooledPostback(in, q, at)
	var err error
	switch outcome {
	case "no_store", "store_error":
		kind := kindStore
		if !d.signedCID(in.cid) {
			kind = kindUnsigned
		}
		if err = d.pbq.accept(e, kind, senderOf(r)); err == nil {
			return "queued", true
		}
		if errors.Is(err, errStoreFull) {
			return "queue_full", false
		}
	case "click_not_found":
		if !d.signedCID(in.cid) {
			return outcome, false
		}
		if err = d.pbq.accept(e, kindRetry, senderOf(r)); err == nil {
			return "retry", true
		}
		if errors.Is(err, errRetryFull) {
			return "retry_overflow", false
		}
	default:
		return outcome, false
	}
	if d.log != nil {
		d.log.Warn("postback queue refused a postback", "cid", in.cid, "err", err)
	}
	return outcome, false
}

// senderOf returns the address that sent the request, or "unknown" when the
// middleware did not set one.
func senderOf(r *http.Request) string {
	if ip := server.ClientIP(r.Context()); ip.IsValid() {
		return ip.String()
	}
	return "unknown"
}

// queueOutbound sends the campaign templates once for this click and status.
// res is what the store reported for the conversion: the campaign and whether
// the pair is new. Only a new pair queues anything, so a repeat of the postback
// never sends a second outbound request; the lock of the pair covers repeats
// that arrive at the same time. A campaign whose templates were all dropped
// is counted in outbound.dropped and frees the lock, but a later repeat is not
// new and sends nothing: a dropped job is lost, not retried. It is
// queueOutboundCtx without a deadline.
func (d *engineDeps) queueOutbound(res store.ConversionResult, cid string, profit float64, status string, subs [config.MaxSubID]string) {
	d.queueOutboundCtx(context.Background(), res, cid, profit, status, subs)
}

// queueOutboundCtx is queueOutbound with ctx bounding the Redis lock; an
// expired ctx makes the lock fall back to memory at once. The write of the job
// to the disk queue is not cancellable.
func (d *engineDeps) queueOutboundCtx(ctx context.Context, res store.ConversionResult, cid string, profit float64, status string, subs [config.MaxSubID]string) {
	if d == nil || !res.Fresh || !d.takeOutbound(ctx, cid, status) {
		return
	}
	if !d.fireOutbound(res.GroupID, cid, res.RootCID, profit, status, subs) {
		d.releaseOutbound(ctx, cid, status)
	}
}

// postback handles GET and POST /postback. It delegates to handlePostback with
// an empty path key: the authentication key is taken from the "key" query or
// form parameter.
func (d *engineDeps) postback(w http.ResponseWriter, r *http.Request) {
	d.handlePostback(w, r, "")
}

// notePostback adds one inbound or outbound line to the postback journal. The
// key and the raw query are not stored. The line is queued without waiting: the
// journal writer inserts it in a batch or parks it on disk. Without a journal
// writer the line is ignored.
func (d *engineDeps) notePostback(subid, status, outcome, direction string, payout float64) {
	if d == nil || d.plog == nil {
		return
	}
	d.plog.note(store.PostbackLogRow{
		Ts: time.Now(), Subid: subid, Status: status, Payout: payout,
		Outcome: outcome, Direction: direction,
	})
}
