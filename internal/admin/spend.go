package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egerkuzma/kuztds/internal/config"
)

var spendMu sync.Mutex

type spendBody struct {
	GroupID     string `json:"group_id"`
	Zone        string `json:"zone"`
	From        string `json:"from"`
	To          string `json:"to"`
	Amount      string `json:"amount"`
	Currency    string `json:"currency"`
	Unique      bool   `json:"unique"`
	IncludeBots bool   `json:"include_bots"`
}

func (s *Server) handleSpendList(w http.ResponseWriter, r *http.Request) {
	v := s.viewerFrom(r.Context())
	if v.hides("cost") {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	if s.cfg.SpendPath == "" {
		writeJSON(w, http.StatusOK, []config.SpendEntry{})
		return
	}
	list, err := readSpend(s.cfg.SpendPath)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "read error")
		return
	}
	list, err = s.filterSpendList(r.Context(), v, list)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "groups error")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleSpendCreate(w http.ResponseWriter, r *http.Request) {
	v := s.viewerFrom(r.Context())
	if v.hides("cost") {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	if s.cfg.SpendPath == "" {
		writeErr(w, http.StatusServiceUnavailable, "store unavailable")
		return
	}
	var body spendBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	entry, msg := buildSpend(body, time.Now())
	if msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	{
		ok, err := s.spendVisible(r.Context(), v, entry.GroupID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "groups error")
			return
		}
		if !ok {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
	}
	var weights []config.SpendWeight
	if s.cfg.Stats != nil {
		rows, err := s.cfg.Stats.ClickBuckets(r.Context(), entry.GroupID, entry.From, entry.To, body.IncludeBots)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "stats error")
			return
		}
		for _, row := range rows {
			weights = append(weights, config.SpendWeight{
				Stream: row.Stream, OfferID: row.OfferID, NetworkID: row.NetworkID, Hits: row.Hits, Uniq: row.Uniq,
			})
		}
	}
	entry.Buckets = config.Allocate(entry.Cents, weights, entry.Unique)
	spendMu.Lock()
	defer spendMu.Unlock()
	list, err := readSpend(s.cfg.SpendPath)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "read error")
		return
	}
	for _, cur := range list {
		if cur.GroupID != entry.GroupID {
			continue
		}
		if !strings.EqualFold(cur.Currency, entry.Currency) {
			writeErr(w, http.StatusBadRequest, "campaign currency is already set")
			return
		}
		if config.SpendOverlaps(cur.From, cur.To, entry.From, entry.To) {
			writeErr(w, http.StatusBadRequest, "interval overlaps")
			return
		}
	}
	entry.ID = strconv.FormatInt(time.Now().UnixNano(), 10)
	list = append(list, entry)
	if err := writeSpend(s.cfg.SpendPath, list); err != nil {
		writeErr(w, http.StatusInternalServerError, "save error")
		return
	}
	writeJSON(w, http.StatusCreated, entry)
}

// handleSpendDelete removes one saved cost by id.
// Other entries and their buckets are left as stored.
func (s *Server) handleSpendDelete(w http.ResponseWriter, r *http.Request) {
	v := s.viewerFrom(r.Context())
	if v.hides("cost") {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	if s.cfg.SpendPath == "" {
		writeErr(w, http.StatusServiceUnavailable, "store unavailable")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeErr(w, http.StatusBadRequest, "id required")
		return
	}
	spendMu.Lock()
	defer spendMu.Unlock()
	list, err := readSpend(s.cfg.SpendPath)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "read error")
		return
	}
	{
		okDel, err := s.spendDeleteAllowed(r.Context(), v, list, id)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "groups error")
			return
		}
		if !okDel {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
	}
	next, ok := dropSpend(list, id)
	if !ok {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if err := writeSpend(s.cfg.SpendPath, next); err != nil {
		writeErr(w, http.StatusInternalServerError, "save error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// spendDeleteAllowed reports whether v may delete the spend row id in list.
// ok is false when id is missing or the campaign is not visible. List errors propagate.
func (s *Server) spendDeleteAllowed(ctx context.Context, v viewer, list []config.SpendEntry, id string) (bool, error) {
	for _, e := range list {
		if e.ID != id {
			continue
		}
		ok, err := s.spendVisible(ctx, v, e.GroupID)
		if err != nil {
			return false, err
		}
		return ok, nil
	}
	return false, nil
}

// dropSpend returns the list without the entry id.
// ok is false when id is not present. The remaining buckets are not rebuilt.
func dropSpend(list []config.SpendEntry, id string) ([]config.SpendEntry, bool) {
	next := make([]config.SpendEntry, 0, len(list))
	found := false
	for _, e := range list {
		if e.ID == id {
			found = true
			continue
		}
		next = append(next, e)
	}
	if !found {
		return list, false
	}
	return next, true
}

// buildSpend checks the form and returns the entry without a snapshot.
// now is the clock used for the closed-window rules. The error text is for the client.
func buildSpend(body spendBody, now time.Time) (config.SpendEntry, string) {
	body.GroupID = strings.TrimSpace(body.GroupID)
	if body.GroupID == "" {
		return config.SpendEntry{}, "group id required"
	}
	loc, err := time.LoadLocation(strings.TrimSpace(body.Zone))
	if err != nil {
		return config.SpendEntry{}, "unknown timezone"
	}
	from, err := parseLocal(body.From, loc)
	if err != nil {
		return config.SpendEntry{}, "bad from"
	}
	to, err := parseLocal(body.To, loc)
	if err != nil {
		return config.SpendEntry{}, "bad to"
	}
	if !to.After(from) {
		return config.SpendEntry{}, "end must be after start"
	}
	if to.After(now) {
		return config.SpendEntry{}, "end is in the future"
	}
	if from.Before(now.Add(-21 * 24 * time.Hour)) {
		return config.SpendEntry{}, "start is older than 21 days"
	}
	cents, err := config.ParseCents(body.Amount)
	if err != nil {
		return config.SpendEntry{}, err.Error()
	}
	cur := strings.ToUpper(strings.TrimSpace(body.Currency))
	if !config.AllowedCurrency(cur) {
		return config.SpendEntry{}, "unknown currency"
	}
	return config.SpendEntry{
		GroupID: body.GroupID, Zone: loc.String(), From: from.UTC(), To: to.UTC(),
		Cents: cents, Currency: cur, Unique: body.Unique,
	}, ""
}

func parseLocal(raw string, loc *time.Location) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	for _, layout := range []string{"2006-01-02T15:04", "2006-01-02T15:04:05"} {
		if t, err := time.ParseInLocation(layout, raw, loc); err == nil {
			return t, nil
		}
	}
	return time.Time{}, errors.New("bad time")
}

func readSpend(path string) ([]config.SpendEntry, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []config.SpendEntry{}, nil
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return []config.SpendEntry{}, nil
	}
	var list []config.SpendEntry
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, err
	}
	if list == nil {
		list = []config.SpendEntry{}
	}
	return list, nil
}

func writeSpend(path string, list []config.SpendEntry) error {
	if list == nil {
		list = []config.SpendEntry{}
	}
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(b, '\n'), 0o644)
}

func validateStreams(groups []config.Group, offers []config.Offer) string {
	byID := map[string]config.Offer{}
	for _, o := range offers {
		byID[o.ID] = o
	}
	for _, g := range groups {
		for _, st := range g.Streams {
			seen := map[string]bool{}
			for _, ref := range st.Out.Offers {
				if ref.Share < 0 {
					return "share must not be negative"
				}
				o, ok := byID[ref.ID]
				if !ok || o.Local {
					continue
				}
				u := strings.TrimSpace(o.URL)
				if u == "" {
					continue
				}
				if seen[u] {
					return "duplicate offer url"
				}
				seen[u] = true
			}
		}
	}
	return ""
}

// errSharesExceed is the client message for a share list that adds up to more than 100.
const errSharesExceed = "shares exceed 100"

// shareSum adds up the shares of one offer or landing list.
// refs is the list as stored in a stream output. Negative values are not counted.
// It returns the total in percent. It does not return an error.
func shareSum(refs []config.ShareRef) int {
	sum := 0
	for _, ref := range refs {
		if ref.Share > 0 {
			sum += ref.Share
		}
	}
	return sum
}

// sharesExceed reports whether any stream of g has an offer list or a landing list above 100%.
// The two lists of a stream are summed separately, as the editor shows them.
// It returns true on the first list over the limit. It does not return an error.
func sharesExceed(g config.Group) bool {
	for _, st := range g.Streams {
		if shareSum(st.Out.Offers) > 100 || shareSum(st.Out.Landings) > 100 {
			return true
		}
	}
	return false
}

// changedFault runs a per-campaign check and reports only faults of campaigns the save changed.
// A stored campaign that already has the fault does not block saving another one.
// stored is the list on disk. groups is the candidate list after prepareGroups.
// fault is the cheap check: it returns the client message, or "" when the campaign is fine.
// It runs first, so only a faulty campaign is compared with its stored row.
// The stored folder is trimmed before the comparison, as prepareGroups trims the candidate.
// A stored campaign that had no number is not changed by receiving exactly the number
// assignGroupNums gives it over the stored list: the editor numbers old campaigns that way
// on save. Any other number, a duplicate one included, is a change.
// It returns "" when no changed campaign has a fault, otherwise the first message.
func changedFault(stored, groups []config.Group, fault func(config.Group) string) string {
	var old map[string]config.Group
	var assigned []config.Group
	for _, g := range groups {
		msg := fault(g)
		if msg == "" {
			continue
		}
		if old == nil {
			old = make(map[string]config.Group, len(stored))
			for _, s := range stored {
				old[s.ID] = s
			}
			assigned = append([]config.Group(nil), stored...)
			assignGroupNums(assigned)
		}
		prev, ok := old[g.ID]
		prev.Folder = strings.TrimSpace(prev.Folder)
		if ok && prev.Num <= 0 && g.Num == serverNum(assigned, g.ID) {
			prev.Num = g.Num
		}
		if !ok || !groupsEqual(prev, g) {
			return msg
		}
	}
	return ""
}

// serverNum returns the Num of the campaign with the given id in a numbered list.
// numbered is a list after assignGroupNums. id is the campaign id.
// It returns 0 when the id is absent. It does not return an error.
func serverNum(numbered []config.Group, id string) int {
	for _, g := range numbered {
		if g.ID == id {
			return g.Num
		}
	}
	return 0
}

// sharesFault returns errSharesExceed when any stream of g has a list above 100%, else "".
func sharesFault(g config.Group) string {
	if sharesExceed(g) {
		return errSharesExceed
	}
	return ""
}

// subIDFault checks the Sub ID rules of every stream of g.
// It returns the client message naming the group id and the stream, or "" when the rules are valid.
func subIDFault(g config.Group) string {
	for _, st := range g.Streams {
		if msg := st.Rules.SubIDRulesError(); msg != "" {
			return msg + " (group " + g.ID + ", stream " + st.Name + ")"
		}
	}
	return ""
}

// validateChangedShares checks the 100% limit only for campaigns the save changed.
// stored is the list on disk, groups the candidate list after prepareGroups.
// It returns an empty string when every changed campaign fits, otherwise the client message.
func validateChangedShares(stored, groups []config.Group) string {
	return changedFault(stored, groups, sharesFault)
}

// validateChangedSubIDs checks the Sub ID rules only for campaigns the save changed.
// stored is the list on disk, groups the candidate list after prepareGroups.
// It returns an empty string when every changed campaign is valid, otherwise the client message.
func validateChangedSubIDs(stored, groups []config.Group) string {
	return changedFault(stored, groups, subIDFault)
}

func (s *Server) offerList(ctx context.Context) ([]config.Offer, error) {
	if s.cfg.Offers == nil {
		return nil, nil
	}
	return s.cfg.Offers.List(ctx)
}
