package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/hasayone/empataso-tds/internal/security"
)

// aiTally counts the tokens and the US dollars of one subject.
type aiTally struct {
	Tokens int64   `json:"tokens"`
	USD    float64 `json:"usd"`
}

// aiUsageDisk is the content of assistant/usage.json: one UTC day, the total and the
// counters by user id. Reservations are never written.
type aiUsageDisk struct {
	Day    string             `json:"day"`
	Tokens int64              `json:"tokens"`
	USD    float64            `json:"usd"`
	Users  map[string]aiTally `json:"users"`
}

// aiUsageState is the usage counter of the current UTC day plus the reservations in flight.
// mu guards every field. now replaces the clock in tests; nil means the system clock.
// blocked is set for the rest of the day when usage.json was damaged at load: the spent
// amount is unknown, so aiReserve refuses every call until the next UTC day or a restart
// with a repaired file.
type aiUsageState struct {
	mu        sync.Mutex
	loaded    bool
	blocked   bool
	day       string
	total     aiTally
	users     map[string]aiTally
	held      aiTally
	heldUsers map[string]aiTally
	now       func() time.Time
}

// aiReservation is the room held for one provider call, made by aiReserve and ended by
// aiCommit. In and Out are the input and output tokens held (Tokens is their sum, USD their
// cost). Priced tells whether Price applies to the model.
type aiReservation struct {
	UID    string
	Model  string
	Tokens int64
	In     int
	Out    int
	USD    float64
	Price  aiPrice
	Priced bool
}

// aiUsagePath returns the path of usage.json, or "" when the assistant has no directory.
func (s *Server) aiUsagePath() string {
	if s.cfg.AIDir == "" {
		return ""
	}
	return filepath.Join(s.cfg.AIDir, "usage.json")
}

// aiUsageUID returns the id the usage of sess is counted under: the owner id of the
// session, or "env-<login>" for the environment administrator.
func (s *Server) aiUsageUID(sess security.Session) string {
	if uid := sessionUID(sess); uid != "" {
		return uid
	}
	return "env-" + s.cfg.AdminUser
}

// aiRollLocked loads usage.json on first use and starts a new day when the UTC date moved.
// The caller holds s.ai.usage.mu. A missing file, or one of another day, starts the day empty.
// A file that exists but cannot be read or parsed is never treated as empty: see aiUsageDamaged.
func (s *Server) aiRollLocked() {
	u := &s.ai.usage
	now := time.Now
	if u.now != nil {
		now = u.now
	}
	today := now().UTC().Format("2006-01-02")
	if !u.loaded {
		u.loaded = true
		if path := s.aiUsagePath(); path != "" {
			var d aiUsageDisk
			raw, err := os.ReadFile(path)
			switch {
			case errors.Is(err, os.ErrNotExist):
			case err != nil || json.Unmarshal(raw, &d) != nil:
				s.aiUsageDamaged(path, raw)
				u.day, u.blocked = today, true
			case d.Day == today:
				u.day, u.total, u.users = d.Day, aiTally{Tokens: d.Tokens, USD: d.USD}, d.Users
			}
		}
	}
	if u.day != today {
		u.day, u.total, u.users, u.blocked = today, aiTally{}, nil, false
	}
	if u.users == nil {
		u.users = map[string]aiTally{}
	}
	if u.heldUsers == nil {
		u.heldUsers = map[string]aiTally{}
	}
}

// aiUsageDamaged keeps a copy of the damaged usage.json as usage.json.bad (mode 0600) and logs
// the event without any of its content. raw is what was read, nil when the read failed.
// The safe answer to a file whose counters are unknown is to refuse the calls of the day
// (aiUsageState.blocked) instead of starting from zero and letting a limit be spent twice;
// the administrator repairs or removes usage.json and restarts, or waits for the next UTC day.
func (s *Server) aiUsageDamaged(path string, raw []byte) {
	bad := path + ".bad"
	if raw != nil {
		if err := writeFileAtomic(bad, raw, 0o600); err != nil {
			s.logWarn("ai usage file damaged, copy not saved")
			return
		}
	}
	s.logWarn("ai usage file damaged, calls are refused until the next UTC day or a repaired file")
}

// aiUsageSaveLocked writes usage.json: the directory 0700, the file 0600, atomically.
// The caller holds s.ai.usage.mu. A failed write is logged; the counters stay in memory.
func (s *Server) aiUsageSaveLocked() {
	path := s.aiUsagePath()
	if path == "" {
		return
	}
	u := &s.ai.usage
	raw, err := json.Marshal(aiUsageDisk{Day: u.day, Tokens: u.total.Tokens, USD: u.total.USD, Users: u.users})
	if err == nil {
		if err = os.MkdirAll(s.cfg.AIDir, 0o700); err == nil {
			err = writeFileAtomic(path, append(raw, '\n'), 0o600)
		}
	}
	if err != nil {
		s.logWarn("ai usage not saved")
	}
}

// aiQuotaErr builds the 429 ai_quota error with a message naming the limit.
func aiQuotaErr(what string) *aiError {
	return newAIError(http.StatusTooManyRequests, aiErrQuota, what+" limit is reached")
}

// aiReserve holds the room for one provider call and checks the daily limits.
// uid is the user (see aiUsageUID), model the model of the call and st the settings.
// The reservation is turn_input_tokens plus max_output_tokens and, when the model has a
// price, their cost; reservations in flight count against the limits too. A switched-off limit
// is not checked, a limit of 0 refuses every call it counts, and a model without a price is not
// checked against the USD limits or budgets.
// The USD budgets of st (the user's own and the one of its group, the role of its record, summed
// over the accounts that hold the role) are checked against the day's spent money plus this
// reservation; the length of a chat never enters. It returns an ai_quota error (429) when a limit
// of the user or of the team would be exceeded, or a budget would be passed (params scope:
// user_budget or group_budget). Every successful call is followed by exactly one aiCommit.
func (s *Server) aiReserve(uid, model string, st aiSettings) (aiReservation, error) {
	lim := st.limits()
	r := aiReservation{UID: uid, Model: model, In: lim.TurnInput, Out: lim.MaxOutput,
		Tokens: int64(lim.TurnInput) + int64(lim.MaxOutput)}
	if p, ok := s.aiPriceOf(st, model); ok {
		r.Price, r.Priced = p, true
		r.USD = aiCost(p, lim.TurnInput, lim.MaxOutput)
	}
	gid, members := s.aiBudgetScope(uid, st)
	u := &s.ai.usage
	u.mu.Lock()
	defer u.mu.Unlock()
	s.aiRollLocked()
	if u.blocked {
		return aiReservation{}, newAIError(http.StatusTooManyRequests, aiErrQuota,
			"the usage counters are damaged, calls resume on the next UTC day or after the file is repaired")
	}
	if r.Priced {
		if scope := aiBudgetOver(st, uid, gid, members, u.users, u.heldUsers, r.USD); scope != "" {
			return aiReservation{}, aiBudgetErr(scope)
		}
	}
	mine, heldMine := u.users[uid], u.heldUsers[uid]
	switch {
	case aiOverLimit(mine.Tokens+heldMine.Tokens+r.Tokens, lim.UserDailyTokens):
		return aiReservation{}, aiQuotaErr("the daily token")
	case aiOverLimit(u.total.Tokens+u.held.Tokens+r.Tokens, lim.DailyTokens):
		return aiReservation{}, aiQuotaErr("the daily team token")
	case r.Priced && aiOverLimit(mine.USD+heldMine.USD+r.USD, lim.UserDailyUSD):
		return aiReservation{}, aiQuotaErr("the daily USD")
	case r.Priced && aiOverLimit(u.total.USD+u.held.USD+r.USD, lim.DailyUSD):
		return aiReservation{}, aiQuotaErr("the daily team USD")
	}
	u.held.Tokens += r.Tokens
	u.held.USD += r.USD
	heldMine.Tokens += r.Tokens
	heldMine.USD += r.USD
	u.heldUsers[uid] = heldMine
	return r, nil
}

// aiCommit ends a reservation and records what the call used.
// r is the reservation of aiReserve, in and out the input and output tokens the provider
// reported. The held room is released and the facts are added to the day of the team and
// of the user, with their cost when the model has a price; usage.json is written. A call
// that failed releases its room with in and out of 0. Call it once per reservation.
func (s *Server) aiCommit(r aiReservation, in, out int) {
	u := &s.ai.usage
	u.mu.Lock()
	defer u.mu.Unlock()
	s.aiRollLocked()
	u.held.Tokens = max(u.held.Tokens-r.Tokens, 0)
	u.held.USD = max(u.held.USD-r.USD, 0)
	held := u.heldUsers[r.UID]
	held.Tokens = max(held.Tokens-r.Tokens, 0)
	held.USD = max(held.USD-r.USD, 0)
	if held == (aiTally{}) {
		delete(u.heldUsers, r.UID)
	} else {
		u.heldUsers[r.UID] = held
	}
	fact := aiTally{Tokens: int64(max(in, 0)) + int64(max(out, 0))}
	if r.Priced {
		fact.USD = aiCost(r.Price, max(in, 0), max(out, 0))
	}
	if fact == (aiTally{}) {
		return
	}
	u.total.Tokens += fact.Tokens
	u.total.USD += fact.USD
	mine := u.users[r.UID]
	mine.Tokens += fact.Tokens
	mine.USD += fact.USD
	u.users[r.UID] = mine
	s.aiUsageSaveLocked()
}

// aiUsageSnapshot returns the current UTC day, the total and a copy of the counters by
// user id. Reservations in flight are not part of it.
func (s *Server) aiUsageSnapshot() (string, aiTally, map[string]aiTally) {
	u := &s.ai.usage
	u.mu.Lock()
	defer u.mu.Unlock()
	s.aiRollLocked()
	users := make(map[string]aiTally, len(u.users))
	for k, v := range u.users {
		users[k] = v
	}
	return u.day, u.total, users
}

// aiUsageView builds the usage of the day for the settings view of the session sess. Users are
// sorted by tokens, the biggest first; the login comes from the user list and is empty for an
// account that no longer exists. Me is the usage of sess.
func (s *Server) aiUsageView(sess security.Session) aiUsageView {
	day, total, users := s.aiUsageSnapshot()
	v := aiUsageView{Day: day, Tokens: total.Tokens, USD: aiRoundUSD(total.USD), Users: []aiUsageUserView{}, Me: s.aiMyUsage(sess)}
	for uid, t := range users {
		v.Users = append(v.Users, aiUsageUserView{UID: uid, Login: s.aiUsageLogin(uid), Tokens: t.Tokens, USD: aiRoundUSD(t.USD)})
	}
	sort.Slice(v.Users, func(i, j int) bool {
		if v.Users[i].Tokens != v.Users[j].Tokens {
			return v.Users[i].Tokens > v.Users[j].Tokens
		}
		return v.Users[i].UID < v.Users[j].UID
	})
	return v
}

// aiUsageLogin returns the login of the account with id uid: the users.json record, or the
// environment administrator for "env-<login>". It returns "" for an unknown id.
func (s *Server) aiUsageLogin(uid string) string {
	if u, ok := s.userByID(uid); ok {
		return u.Login
	}
	if uid == "env-"+s.cfg.AdminUser {
		return s.cfg.AdminUser
	}
	return ""
}
