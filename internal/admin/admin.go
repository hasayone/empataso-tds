// Package admin — KuzTDS management REST API (auth, statistics, groups, lists).
//
// Fixes the problems of the old admin panel: argon2id instead of MD5, server-side
// sessions in a store instead of an md5(ip+pass) cookie, CSRF tokens on mutations,
// login rate-limit without a blocking sleep, parameterized queries to ClickHouse,
// a safe .dat editor without path traversal.
package admin

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/logbuf"
	"github.com/egerkuzma/kuztds/internal/security"
	"github.com/egerkuzma/kuztds/internal/store"
)

// StatsProvider — source of statistics (implemented by store.CH).
type StatsProvider interface {
	Summary(ctx context.Context, from, to time.Time) (store.StatsSummary, error)
	// SummaryFiltered returns period totals restricted to clicks matching conds.
	SummaryFiltered(ctx context.Context, from, to time.Time, conds []store.Cond) (store.StatsSummary, error)
	TimeSeries(ctx context.Context, from, to time.Time, stepSec int) ([]store.TSPoint, error)
	// TimeSeriesFiltered returns chart buckets restricted to clicks matching conds.
	TimeSeriesFiltered(ctx context.Context, from, to time.Time, stepSec int, conds []store.Cond) ([]store.TSPoint, error)
	Breakdown(ctx context.Context, from, to time.Time, dim string, limit int) ([]store.KV, error)
	// BreakdownFiltered returns the top values of dim under conds.
	BreakdownFiltered(ctx context.Context, from, to time.Time, dim string, limit int, conds []store.Cond) ([]store.KV, error)
	Performance(ctx context.Context, from, to time.Time) ([]store.PerfRow, error)
	// PerformanceFiltered returns the performance table restricted to clicks matching conds.
	PerformanceFiltered(ctx context.Context, from, to time.Time, conds []store.Cond) ([]store.PerfRow, error)
	Logs(ctx context.Context, f store.LogFilter) ([]store.LogRow, int64, error)
	Postbacks(ctx context.Context, from, to time.Time, group string, limit int) ([]store.PostbackRow, float64, error)
	QueryPostbacks(ctx context.Context, q store.PostbackQuery) ([]store.PostbackRow, store.PostbackTotals, error)
	PostbackLog(ctx context.Context, from, to time.Time, limit int) ([]store.PostbackLogRow, error)
	Slice(ctx context.Context, from, to time.Time) ([]store.SliceRow, error)
	// Fields lists journal fields and ready waves without a query.
	Fields() store.FieldsInfo
	// Values lists the top values of q.Field on q.Table between q.From and q.To under q.Conds.
	Values(ctx context.Context, q store.ValuesQuery) ([]store.KV, error)
	// SliceFiltered returns the period report restricted by conds.
	SliceFiltered(ctx context.Context, from, to time.Time, conds []store.Cond) ([]store.SliceRow, error)
	DeleteGroupLogs(ctx context.Context, group string) error
	ClickFacts(ctx context.Context, from, to time.Time) (store.Facts, error)
	// ClickFactsFiltered returns catalog facts restricted to clicks matching conds.
	ClickFactsFiltered(ctx context.Context, from, to time.Time, conds []store.Cond) (store.Facts, error)
	ClickBuckets(ctx context.Context, groupID string, from, to time.Time, includeBots bool) ([]store.BucketCount, error)
}

// KeysReader reads the collected keywords.
type KeysReader interface {
	Read(group, date string, se bool) (string, error)
}

// GroupsStore — read and save the groups configuration.
type GroupsStore interface {
	List(ctx context.Context) ([]config.Group, error)
	Save(ctx context.Context, groups []config.Group) error
}

// ListsStore — management of .dat lists (IP/WAP operators/signatures).
type ListsStore interface {
	Names() ([]string, error)
	Read(name string) (string, error)
	Write(name, content string) error
	// Delete removes name from the list directory.
	// A missing file is not an error. An invalid name or a disk failure is.
	Delete(name string) error
}

// LoginLimiter limits the rate of login attempts per key (IP).
//
// Allow reports whether the attempt may proceed, and separately whether the
// limiter could answer at all. The two are not the same: a limiter that cannot
// reach its storage has no opinion, and the handler needs to know that before
// it decides whether to check a password.
type LoginLimiter interface {
	Allow(ctx context.Context, key string) (bool, error)
}

// ImportSink writes imported clicks and conversions and removes them for one host.
// InsertEvents writes a click batch. An empty batch returns nil.
// InsertConversions writes a conversion batch. An empty batch returns nil.
// PurgeImported deletes events and conversions whose domain equals domain.
// CountByDomain returns the events count and the conversions count.
// A blank domain is an error from PurgeImported and CountByDomain.
// Errors come from the warehouse.
type ImportSink interface {
	InsertEvents(ctx context.Context, rows []logbuf.Event) error
	InsertConversions(ctx context.Context, rows []store.ConversionInsert) error
	PurgeImported(ctx context.Context, domain string) error
	CountByDomain(ctx context.Context, domain string) (int64, int64, error)
}

// Config — dependencies and settings of the admin server.
type Config struct {
	AdminUser    string
	PasswordHash string // argon2id
	Sessions     security.SessionStore
	Stats        StatsProvider
	Groups       GroupsStore
	Offers       *JSONStore[config.Offer]
	Landings     *JSONStore[config.Landing]
	LandingFiles LandingFiles
	Sources      *JSONStore[config.TrafficSource]
	Networks     *JSONStore[config.AffiliateNetwork]
	Domains      *JSONStore[config.Domain]
	SeqOffers    *idSeq
	SeqLandings  *idSeq
	SeqSources   *idSeq
	SeqNetworks  *idSeq
	SeqDomains   *idSeq
	SpendPath    string
	BotPath      string
	UIPath       string
	Lists        ListsStore
	Pages        PagesStore
	Manifest     ManifestStore
	Keys         KeysReader
	Limiter      LoginLimiter
	SessionTTL   time.Duration
	CookieSecure bool
	EngineURL    string // address the admin process uses to call the engine
	// LookupIP resolves a host for a domain check.
	// host is the name without a port. Nil uses net.DefaultResolver.LookupIPAddr.
	// It returns the addresses or the resolver error.
	LookupIP func(ctx context.Context, host string) ([]net.IPAddr, error)
	// CheckClient performs the HTTPS probe of a domain check.
	// Nil uses the default client, which refuses a non-public address at connect time.
	CheckClient  *http.Client
	PublicURL    string // address opened in the browser; empty uses EngineURL
	PasswordFile string // file with the argon2id hash (overrides PasswordHash, written on change)
	// UsersFile is users.json; empty leaves only the environment administrator.
	UsersFile   string
	APIKeysFile string // API key file; empty disables keys and rejects the Api-Key header
	Log         slogLogger

	// Tools and automation. All optional: what is not set is reported as
	// unavailable by the corresponding endpoint.
	DataDir     string  // .dat lists, for the IP lookup and the simulator
	Geo         GeoInfo // geo databases, for the same
	GeoCityPath string  // whether the cron service has a target for each geo database
	GeoASNPath  string
	CronFile    string // cron service config

	// KeitaroBase is the tracker origin. KeitaroKey is the Admin API key.
	// Warehouse receives imported clicks and conversions. All three are optional.
	KeitaroBase string
	KeitaroKey  string
	Warehouse   ImportSink
}

// slogLogger — minimal logger interface (to avoid pulling in a hard dependency).
type slogLogger interface {
	Warn(msg string, args ...any)
	Info(msg string, args ...any)
}

// credential is the password hash together with its fingerprint. The two are
// published as one value because the second is derived from the first: computed
// separately they can drift, and a fingerprint belonging to an older hash makes
// auth either reject every session or accept sessions it should have killed.
// Deriving it once, here, also keeps a SHA-256 out of every authenticated
// request, where auth used to recompute it.
type credential struct {
	hash string
	fp   string
}

// Server — admin API.
type Server struct {
	cfg Config

	// cred is the only place the current password lives. It is an atomic
	// pointer rather than a mutex-guarded field because it has exactly one
	// writer, a password change, and one reader on every authenticated
	// request. Publishing goes through publish() and nowhere else, so there is
	// no assignment that could forget to recompute the fingerprint.
	cred atomic.Pointer[credential]

	// pwWrite serialises password changes: the file write and the publication
	// have to happen in one order, or two concurrent changes can leave the disk
	// holding one password and the process running on another, which nothing
	// reveals until the next restart.
	//
	// Deliberately not the same lock the readers use. A mutex held across a
	// write to the filesystem is a mutex that stalls every login for as long as
	// the disk feels like taking.
	pwWrite sync.Mutex

	keys  *security.KeySet // nil when APIKeysFile is empty
	keyMu sync.Mutex       // pairs Generate or Revoke with the following Save

	users  *security.UserSet // nil when UsersFile is empty
	userMu sync.Mutex        // pairs user changes with Save

	// domainMu serializes List, modify and Save of domains.json during a domain check.
	domainMu sync.Mutex

	// groupsMu serializes List, modify and Save of groups.json in the admin API.
	groupsMu sync.Mutex

	// hashes bounds concurrent password verifications: argon2id is 64 MiB a
	// call and runs before any credential is checked.
	hashes *hashGate

	// loginFPKey keys the fingerprint of submitted login names in the log. It
	// is generated once per process and never leaves it.
	loginFPKey []byte

	// ktJob is the background Keitaro import and the purge lock.
	// New always sets it. Import and purge share its mutex.
	ktJob *keitaroJob

	// routes lists mux patterns registered via handle during Handler().
	// Handler runs once at startup; no mutex is required.
	routes []string
}

const cookieName = "kuztds_admin"

// New creates an admin server.
//
// It fails rather than starting with a hash it cannot use. The alternative is
// worse than it sounds: a Server that boots with an unusable hash answers every
// login with "invalid credentials", and the operator goes looking for a broken
// password instead of a broken file.
func New(cfg Config) (*Server, error) {
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = 12 * time.Hour
	}
	key := make([]byte, 32)
	// crypto/rand.Read does not return an error as of Go 1.24 — it panics if
	// the system source fails. That is the right outcome here anyway: a
	// zero-valued key would silently reduce the log fingerprint to a bare
	// SHA-256, which is exactly what it exists to avoid, and nothing would look
	// any different.
	rand.Read(key)
	s := &Server{cfg: cfg, hashes: newHashGate(0), loginFPKey: key, ktJob: newKeitaroJob()}
	hash, err := initialHash(cfg)
	if err != nil {
		return nil, err
	}
	if err := s.publish(hash); err != nil {
		return nil, err
	}
	if cfg.UsersFile != "" {
		us, err := security.LoadUserSet(cfg.UsersFile)
		if err != nil {
			return nil, fmt.Errorf("admin: users file %s: %w", cfg.UsersFile, err)
		}
		s.users = us
	}
	s.loadAPIKeys()
	return s, nil
}

// initialHash picks the hash to start from. The file wins over the environment
// when it exists, because a password change writes it there.
//
// The three ways a file can fail are not one case. Absent means nobody has
// changed the password yet, and falling back to the environment is how every
// clean deployment starts. Unreadable and empty are different: a hash was
// written and we cannot see it, so starting on the environment value would
// quietly restore the password that was replaced — the old one works, the new
// one does not, and nothing says why. An empty file in particular is what a
// torn write leaves behind.
func initialHash(cfg Config) (string, error) {
	if cfg.PasswordFile == "" {
		return cfg.PasswordHash, nil
	}
	b, err := os.ReadFile(cfg.PasswordFile)
	if errors.Is(err, os.ErrNotExist) {
		return cfg.PasswordHash, nil
	}
	if err != nil {
		return "", fmt.Errorf("admin: password file %s: %w", cfg.PasswordFile, err)
	}
	h := strings.TrimSpace(string(b))
	if h == "" {
		return "", fmt.Errorf("admin: password file %s is empty", cfg.PasswordFile)
	}
	return h, nil
}

// publish validates a hash and makes it current. Every path that changes the
// password goes through here — the constructor included, since that is where
// the only two direct assignments used to live.
//
// The validation is not about distrusting HashPassword. It is about never
// putting into service, or onto disk, something we will not be able to read
// back: the file outlives the binary, and a hash that fails to decode after a
// restart locks the administrator out with no way back in.
//
// The error names the defect but never the hash. It goes to a log collector
// that outlives the password.
func (s *Server) publish(hash string) error {
	if err := security.ValidateHash(hash); err != nil {
		return fmt.Errorf("admin: password hash: %w", err)
	}
	s.cred.Store(&credential{hash: hash, fp: security.PasswordFingerprint(hash)})
	return nil
}

func (s *Server) current() credential {
	if c := s.cred.Load(); c != nil {
		return *c
	}
	return credential{}
}

func (s *Server) currentHash() string { return s.current().hash }

// Handler returns an http.Handler with all routes and middleware.
func (s *Server) Handler() http.Handler {
	s.routes = nil
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("POST /api/login", s.handleLogin)
	s.handle(mux, "POST /api/logout", cookieOnly(http.HandlerFunc(s.handleLogout)))
	s.handle(mux, "GET /api/me", cookieOnly(http.HandlerFunc(s.handleMe)))
	s.handle(mux, "POST /api/password", cookieOnly(http.HandlerFunc(s.handlePassword)))
	registerAPIKeys(s, mux)
	registerUsers(s, mux)

	s.handle(mux, "GET /api/stats/summary", http.HandlerFunc(s.handleSummary))
	s.handle(mux, "GET /api/stats/timeseries", http.HandlerFunc(s.handleTimeSeries))
	s.handle(mux, "GET /api/stats/breakdown", http.HandlerFunc(s.handleBreakdown))
	s.handle(mux, "GET /api/stats/performance", http.HandlerFunc(s.handlePerformance))
	s.handle(mux, "GET /api/logs", http.HandlerFunc(s.handleLogs))
	s.handle(mux, "GET /api/log-fields", http.HandlerFunc(s.handleLogFields))
	s.handle(mux, "GET /api/log-values", http.HandlerFunc(s.handleLogValues))
	s.handle(mux, "GET /api/logs/filters", http.HandlerFunc(s.handleLogFilters))
	s.handle(mux, "GET /api/logs/export", http.HandlerFunc(s.handleLogsExport))
	s.handle(mux, "DELETE /api/logs", http.HandlerFunc(s.handleDeleteLogs))
	s.handle(mux, "GET /api/postbacks", http.HandlerFunc(s.handlePostbacks))
	s.handle(mux, "GET /api/postback-log", http.HandlerFunc(s.handlePostbackLog))
	s.handle(mux, "GET /api/notices", http.HandlerFunc(s.handleNotices))
	s.handle(mux, "GET /api/report", http.HandlerFunc(s.handleReport))
	s.handle(mux, "GET /api/keys", http.HandlerFunc(s.handleKeys))

	s.handle(mux, "GET /api/groups", http.HandlerFunc(s.handleGroupsGet))
	s.handle(mux, "PUT /api/groups", http.HandlerFunc(s.handleGroupsSave))
	s.handle(mux, "PUT /api/groups/{id}", http.HandlerFunc(s.handleGroupPut))
	s.handle(mux, "DELETE /api/groups/{id}", http.HandlerFunc(s.handleGroupDelete))
	registerOffers(s, mux)
	registerLandings(s, mux)
	registerLandingFiles(s, mux)
	registerSources(s, mux)
	registerNetworks(s, mux)
	registerDomains(s, mux)
	s.handle(mux, "POST /api/domains/{id}/check", http.HandlerFunc(s.handleDomainCheck))
	s.handle(mux, "GET /api/spend", http.HandlerFunc(s.handleSpendList))
	s.handle(mux, "POST /api/spend", http.HandlerFunc(s.handleSpendCreate))
	s.handle(mux, "DELETE /api/spend/{id}", http.HandlerFunc(s.handleSpendDelete))
	s.handle(mux, "GET /api/stats/facts", http.HandlerFunc(s.handleFacts))
	s.handle(mux, "GET /api/bot-protection", http.HandlerFunc(s.handleBotGet))
	s.handle(mux, "PUT /api/bot-protection", http.HandlerFunc(s.handleBotPut))
	s.handle(mux, "GET /api/ui", http.HandlerFunc(s.handleUIGet))
	s.handle(mux, "PUT /api/ui", http.HandlerFunc(s.handleUIPut))
	mux.HandleFunc("GET /api/brand", s.handleBrandGet)
	s.handle(mux, "GET /api/campaigns/{id}/postbacks", http.HandlerFunc(s.handleCampaignPostbacksGet))
	s.handle(mux, "PUT /api/campaigns/{id}/postbacks", http.HandlerFunc(s.handleCampaignPostbacksPut))

	s.handle(mux, "GET /api/lists", http.HandlerFunc(s.handleListsIndex))
	s.handle(mux, "GET /api/lists/{name}", http.HandlerFunc(s.handleListRead))
	s.handle(mux, "PUT /api/lists/{name}", http.HandlerFunc(s.handleListWrite))

	s.handle(mux, "GET /api/pages", http.HandlerFunc(s.handlePagesIndex))
	s.handle(mux, "GET /api/pages/{name}", http.HandlerFunc(s.handlePageRead))
	s.handle(mux, "PUT /api/pages/{name}", http.HandlerFunc(s.handlePageWrite))
	s.handle(mux, "DELETE /api/pages/{name}", http.HandlerFunc(s.handlePageDelete))

	s.handle(mux, "GET /api/lookup", http.HandlerFunc(s.handleLookup))
	s.handle(mux, "POST /api/simulate", http.HandlerFunc(s.handleSimulate))
	s.handle(mux, "GET /api/cron", http.HandlerFunc(s.handleCronGet))
	s.handle(mux, "PUT /api/cron", http.HandlerFunc(s.handleCronSave))
	s.handle(mux, "POST /api/cron/run", http.HandlerFunc(s.handleCronRun))

	s.handle(mux, "GET /api/keitaro/status", http.HandlerFunc(s.handleKeitaroStatus))
	s.handle(mux, "POST /api/keitaro/check", http.HandlerFunc(s.handleKeitaroCheck))
	s.handle(mux, "POST /api/keitaro/import", http.HandlerFunc(s.handleKeitaroImport))
	s.handle(mux, "GET /api/keitaro/job", http.HandlerFunc(s.handleKeitaroJob))
	s.handle(mux, "POST /api/keitaro/purge", http.HandlerFunc(s.handleKeitaroPurge))

	// Embedded web interface (SPA).
	mux.Handle("GET /assets/", serveAssets())
	mux.HandleFunc("GET /{$}", serveUI)
	return gzipMiddleware(securityHeaders(mux))
}

func (s *Server) handleNotices(w http.ResponseWriter, r *http.Request) {
	base := strings.TrimRight(s.cfg.EngineURL, "/")
	if base == "" {
		writeJSON(w, http.StatusOK, []string{})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/internal/notices", nil)
	if err != nil {
		writeJSON(w, http.StatusOK, []string{})
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeJSON(w, http.StatusOK, []string{})
		return
	}
	defer resp.Body.Close()
	var lines []string
	if err := json.NewDecoder(resp.Body).Decode(&lines); err != nil || lines == nil {
		lines = []string{}
	}
	writeJSON(w, http.StatusOK, lines)
}

func (s *Server) handleKeys(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Keys == nil {
		writeErr(w, http.StatusServiceUnavailable, "keys unavailable")
		return
	}
	q := r.URL.Query()
	v := s.viewerFrom(r.Context())
	groupID := q.Get("group")
	if !v.admin && groupID != "" {
		ref, ok, err := s.campaignRefByID(r.Context(), groupID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "groups error")
			return
		}
		if !ok || !v.canSee(resCampaigns, ref) {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
	}
	content, err := s.cfg.Keys.Read(groupID, q.Get("date"), q.Get("se") == "1")
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]string{"content": ""})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"content": content})
}

type ctxKey int

const sessionKey ctxKey = 0

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A present Api-Key is the only credential. A miss does not fall through to the cookie.
		if r.Header.Get("Api-Key") != "" {
			s.authAPIKey(w, r, next)
			return
		}
		c, err := r.Cookie(cookieName)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		sess, ok, err := s.cfg.Sessions.Get(r.Context(), c.Value)
		if err != nil || !ok {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		// Legacy cookies without UID remain environment-administrator sessions.
		if sess.UID != "" {
			u, ok := s.userByID(sess.UID)
			if !ok || !u.Enabled || u.Hash == "" || !security.EqualTokens(sess.PwFP, u.Fingerprint()) {
				_ = s.cfg.Sessions.Delete(r.Context(), c.Value)
				writeErr(w, http.StatusUnauthorized, "unauthorized")
				return
			}
		} else if !security.EqualTokens(sess.PwFP, s.current().fp) {
			// A session issued under a previous root password is dead.
			_ = s.cfg.Sessions.Delete(r.Context(), c.Value)
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if !isSafeMethod(r.Method) {
			if !security.EqualTokens(r.Header.Get("X-CSRF-Token"), sess.CSRF) {
				writeErr(w, http.StatusForbidden, "csrf")
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionKey, sess)))
	})
}

func sessionFrom(ctx context.Context) security.Session {
	if s, ok := ctx.Value(sessionKey).(security.Session); ok {
		return s
	}
	return security.Session{}
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) logWarn(msg string, args ...any) {
	if s.cfg.Log != nil {
		s.cfg.Log.Warn(msg, args...)
	}
}

func (s *Server) logInfo(msg string, args ...any) {
	if s.cfg.Log != nil {
		s.cfg.Log.Info(msg, args...)
	}
}

func isSafeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

func atoiDef(s string, def int) int {
	if s == "" {
		return def
	}
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
