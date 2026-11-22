// Package admin — EmTDS management REST API (auth, statistics, groups, lists).
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
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/cron"
	"github.com/hasayone/empataso-tds/internal/logbuf"
	"github.com/hasayone/empataso-tds/internal/security"
	"github.com/hasayone/empataso-tds/internal/store"
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
	// EachPostback streams conversions matching q in journal order, up to max rows.
	// prepare runs once with match totals before the first row; nil is skipped.
	EachPostback(ctx context.Context, q store.PostbackQuery, max int, prepare func(store.PostbackTotals) error, fn func(store.PostbackRow) error) (truncated bool, err error)
	PostbackLog(ctx context.Context, q store.PostbackLogQuery) ([]store.PostbackLogRow, error)
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

// LoginLimitAttempts is how many login, setup and recovery attempts one address may make per LoginLimitWindow.
const LoginLimitAttempts = 10

// LoginLimitWindow is the window of LoginLimitAttempts; a refused attempt is told to wait this long.
const LoginLimitWindow = time.Minute

// msgTooManyAttempts is the error text of a refused login, setup or recovery attempt.
const msgTooManyAttempts = "too many attempts"

// ImportSink writes imported clicks and conversions and removes them for one host.
// InsertEvents writes a click batch. An empty batch returns nil.
// InsertConversions writes a conversion batch. An empty batch returns nil.
// PurgeImported deletes events and conversions whose domain equals domain.
// CountByDomain returns the events count and the conversions count.
// InitialWatermark returns the latest stored click and conversion times for domain
// within the last eight days. ExistingCIDs returns ids already stored in that window.
// A blank domain skips InitialWatermark and ExistingCIDs without a query.
// A blank domain is an error from PurgeImported and CountByDomain.
// Errors come from the warehouse.
type ImportSink interface {
	InsertEvents(ctx context.Context, rows []logbuf.Event) error
	InsertConversions(ctx context.Context, rows []store.ConversionInsert) error
	PurgeImported(ctx context.Context, domain string) error
	CountByDomain(ctx context.Context, domain string) (int64, int64, error)
	InitialWatermark(ctx context.Context, domain string) (clicks, conversions time.Time, err error)
	ExistingCIDs(ctx context.Context, domain string, from, to time.Time, cids []string) (map[string]struct{}, error)
}

// Config — dependencies and settings of the admin server.
type Config struct {
	AdminUser    string
	PasswordHash string // argon2id
	Sessions     security.SessionStore
	Stats        StatsProvider
	Groups       GroupsStore
	Offers       *JSONStore[config.Offer]
	Boards       *JSONStore[config.Board]
	S2S          *JSONStore[config.OutboundPostback]
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
	ViewsPath    string
	Lists        ListsStore
	Pages        PagesStore
	Manifest     ManifestStore
	Keys         KeysReader
	Limiter      LoginLimiter
	// LoginRateLimitShared is true when login attempts are counted in a shared store such as Redis.
	LoginRateLimitShared bool
	SessionTTL           time.Duration
	// UniqMax is the ceiling of the uniqueness window that the engine keeps in Redis
	// (EMTDS_UNIQ_MAX_SECONDS). The panel shows it beside the campaign window.
	// Zero or negative uses store.DefaultUniqMax.
	UniqMax      time.Duration
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
	UsersFile string
	// RolesFile is roles.json; empty disables roles, and a user with a role then has no access.
	RolesFile   string
	APIKeysFile string // API key file; empty disables keys and rejects the Api-Key header
	Log         slogLogger

	// Tools and automation. All optional: what is not set is reported as
	// unavailable by the corresponding endpoint.
	DataDir     string  // .dat lists, for the IP lookup and the simulator
	Geo         GeoInfo // geo databases, for the same
	GeoCityPath string  // whether the cron service has a target for each geo database
	GeoASNPath  string
	GeoDBIPPath string // DB-IP ASN Lite target
	CronFile    string // cron service config
	// KeitaroSyncFile is keitaro-sync.json, the log catch-up cursor; empty disables the watcher.
	KeitaroSyncFile string
	// KeitaroJobFile is keitaro-job.json, the journal of the last manual import; empty keeps it in memory.
	KeitaroJobFile string

	// KeitaroBase is the tracker origin. KeitaroKey is the Admin API key.
	// Both are start values used when KeitaroFile does not exist.
	// KeitaroFile is keitaro.json, written by PUT /api/keitaro/settings; empty disables saving.
	// Warehouse receives imported clicks and conversions. All of them are optional.
	KeitaroBase string
	KeitaroKey  string
	KeitaroFile string
	Warehouse   ImportSink

	// NamecheapFile is namecheap.json, the registrar account; empty disables saving it.
	// NamecheapURL and NamecheapSandboxURL replace the Namecheap API endpoints; empty keeps the
	// public ones. NamecheapHTTP replaces the HTTP client; nil keeps the default.
	NamecheapFile       string
	NamecheapURL        string
	NamecheapSandboxURL string
	NamecheapHTTP       *http.Client

	// PublicIP is the address this tracker answers on. Cloudflare records and the domain check
	// ("elsewhere") use it; empty disables both. It must be an IPv4 or IPv6 address.
	PublicIP string
	// CloudflareFile is cloudflare.json, the Cloudflare token and account; empty disables saving it.
	// CloudflareURL replaces the Cloudflare API address; empty keeps the public one.
	// CloudflareHTTP replaces the HTTP client; nil keeps the default.
	CloudflareFile string
	CloudflareURL  string
	CloudflareHTTP *http.Client

	// NetTemplatesFile is net-templates.json on the data volume: the edits and the added templates
	// of the network template catalog; empty disables saving them. The embedded catalog never
	// lives there, so an image update cannot erase an edit.
	NetTemplatesFile string

	// CheckerFile is checker.json, the checker integration settings; empty disables saving it.
	// CheckerAppsFile is checker-apps.json, the application queue; empty disables the queue.
	// CheckerURL replaces the checker API address; empty keeps the address from the settings.
	// CheckerHTTP replaces the HTTP client; nil keeps the default, which does not follow redirects.
	CheckerFile     string
	CheckerAppsFile string
	CheckerURL      string
	CheckerHTTP     *http.Client

	// DocsFile is docs.json, whether the API reference is open without sign-in; empty keeps
	// it open and disables saving the setting.
	DocsFile string

	// AIFile is ai.json, the assistant settings and provider keys; empty disables saving it.
	// AIDir is the assistant directory (chats, changes, usage, landing snapshots); empty disables it.
	// AIBaseURLs replaces the provider base URLs by provider id; AIHTTP replaces the provider
	// HTTP client. Both are for tests only.
	AIFile     string
	AIDir      string
	AIBaseURLs map[string]string
	AIHTTP     *http.Client

	// CIDKeyFile is the click id signing key file of the engine; empty means there is none.
	// SecretValues are the literal secrets of the process environment (database passwords,
	// postback, click id and API keys, the administrator password). The assistant only reads
	// both, to mask their content; cmd/admin fills them from the environment.
	CIDKeyFile   string
	SecretValues []string

	// UpdateDir is the path to the update state directory (agent.json, check.json, state.json, etc.).
	// When empty the update endpoints return empty data.
	UpdateDir string
	// UpdateInboxDir is the inbox directory shared with the updater for apply/rollback/check requests.
	// When empty the write buttons are disabled (inbox_absent on every POST route).
	UpdateInboxDir string

	// AITestBaseURL is the API base (http or https, with its version path) the openai
	// provider calls instead of the public one; its host:port, on any port, is the only
	// address added to the provider host list. For local tests only: production never sets it.
	AITestBaseURL string

	// AdminTrustedProxies lists CIDRs that may set X-Forwarded-For and X-Forwarded-Proto.
	// An empty slice uses localhost only. Invalid CIDRs fail server construction.
	AdminTrustedProxies []string

	// SetupToken is the first-run token (EMTDS_SETUP_TOKEN). When it is not empty, POST /api/setup
	// must carry the same value in the token field. Empty means the first-run form needs no token.
	// The value is a secret: it is never logged and never returned by the API.
	SetupToken string
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

	// setup is true until the first-run form writes the administrator password.
	// It is read from the setup routes and written once the files are in place.
	setup atomic.Bool

	// adminEmail is the environment administrator address from admin.json.
	// The value is a string. It is empty when the file has no address.
	adminEmail atomic.Value

	keys  *security.KeySet // nil when APIKeysFile is empty
	keyMu sync.Mutex       // pairs Generate or Revoke with the following Save

	users  *security.UserSet // nil when UsersFile is empty
	userMu sync.Mutex        // pairs user and role changes with Save

	// roles is nil when RolesFile is empty. Its Failure is why roles.json is not
	// in use; it is empty when the file was accepted.
	roles *security.RoleSet

	// domainMu serializes every write of domains.json: create, replace, delete and check.
	domainMu sync.Mutex

	// netTplMu serializes the read, change and write of the network template edits file.
	netTplMu sync.Mutex

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

	// ktMu guards ktBase, ktKey and ktSource, the tracker settings in use.
	// Readers go through keitaroCreds and take a copy.
	ktMu     sync.RWMutex
	ktBase   string
	ktKey    string
	ktSource string
	// ktCount is the last campaign count found by the connection check.
	ktCount keitaroCountHint
	// ktVersion is the tracker version found by the last connection check; log imports ask id columns by it.
	ktVersion keitaroVersionHint
	// ktConns holds tracker connections from keitaro.json; empty uses ktBase and ktKey only.
	ktConns    []keitaroConn
	ktFileList bool

	// ncMu guards ncAcct and ncErr. Readers take a copy through namecheapAccount.
	// ncErr is why namecheap.json was not used at start; it is empty otherwise.
	// ncBuy holds the domains being registered and ncPrices the cached one-year prices.
	ncMu     sync.RWMutex
	ncAcct   ncAccount
	ncErr    string
	ncBuy    ncBuyState
	ncPrices ncPriceCache
	// ncNow is the clock of the purchase flow, the domain checks and the domain watch; nil
	// means the system clock. Tests set it.
	ncNow func() time.Time

	// cfMu guards cfAcct and cfErr. Readers take a copy through cloudflareAccount.
	// cfErr is why cloudflare.json was not used at start; it is empty otherwise.
	// cfBusy holds the ids of the domains being connected to Cloudflare right now.
	cfMu         sync.RWMutex
	cfAcct       cfAccount
	cfErr        string
	cfBusy       ncBuyState
	adminDNSBusy ncBuyState
	// watchBusy is set while a domain watch tick runs, so ticks never overlap.
	watchBusy atomic.Bool
	// watchFirst is the wait before the first watch tick; zero means thirty seconds. Tests set it.
	watchFirst time.Duration

	// checker is the state of the checker integration: its settings, the applications of the
	// campaigns, the queue and the request gate. loadChecker fills it in New.
	checker checkerState

	// syncBusy is set while a Keitaro log catch-up run is in progress.
	syncBusy atomic.Bool
	// syncKick asks the watcher to run catch-up on the next tick.
	syncKick atomic.Bool
	// syncFailStreak counts consecutive failed catch-up runs for backoff.
	syncFailStreak int
	// syncPassCount counts started catch-up passes (tests only).
	syncPassCount atomic.Int32
	// syncNow is the clock for catch-up scheduling; nil uses time.Now().UTC. Tests set it.
	syncNow func() time.Time
	// catalogRetryMu guards catalogRetry, the time before which a failed catalog check of a
	// sync target is not repeated, keyed by catalogRetryKey. The map lives in memory only.
	catalogRetryMu sync.Mutex
	catalogRetry   map[string]time.Time

	// routes lists mux patterns registered via handle during Handler().
	// Handler runs once at startup; no mutex is required.
	routes []string

	// public lists the mux patterns registered via handlePublic during Handler(),
	// the routes mounted without the session check.
	public []string

	// inner maps a mux pattern to its handler behind boardScope, guard and requireAction,
	// without auth. The assistant runs its tools through it with the session of the user.
	// Filled by handle() at startup, read-only afterwards.
	inner map[string]http.Handler

	// ai is the state of the AI assistant.
	ai assistantState

	// trustedProxies lists CIDRs that may set X-Forwarded-For and X-Forwarded-Proto.
	trustedProxies []netip.Prefix

	// audit records write requests of API keys; keyLim limits requests per key.
	// New sets both; a Server built without New runs without them.
	audit  *auditLog
	keyLim *keyLimiter

	// lookups is the IP check history of the Tools page. New sets it; nil records nothing.
	lookups *lookupHistory

	// docsOnce builds docs, the public API reference, on its first request.
	docsOnce sync.Once
	docs     *publicDocs

	// kwSlots holds the running keyword statistics and trend queries; see acquireKeywordSlot.
	// New makes it; a Server built without New answers those routes 429.
	kwSlots chan struct{}

	// adminDNSWG counts background ensureAdminDNS jobs; tests may Wait on it.
	adminDNSWG sync.WaitGroup
	// adminDNSSync runs ensureAdminDNS in the caller goroutine instead of a background job.
	adminDNSSync bool

	// updateCheckMu guards updateCheckAt, the last time POST /api/update/check was accepted.
	updateCheckMu sync.Mutex
	updateCheckAt time.Time

	// updateWriteMu serialises the precondition-check + request-write sequence for all
	// three POST update routes so that two concurrent calls cannot both pass the
	// request_pending check and both write a request file.
	updateWriteMu sync.Mutex

	// procRoot is the directory GET /api/system reads loadavg, meminfo and uptime from;
	// empty means /proc. Tests point it at a fake tree.
	procRoot string
	// diskStat returns the free and total bytes of the filesystem holding path; nil means
	// cron.DiskFree. Tests replace it.
	diskStat func(path string) (free, total uint64, err error)
}

// cookieName names the session cookie the panel issues.
const cookieName = "emtds_admin"

// sessionCookie returns the emtds_admin session cookie of r.
// The error is http.ErrNoCookie when it is absent.
func sessionCookie(r *http.Request) (*http.Cookie, error) {
	return r.Cookie(cookieName)
}

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
	if cfg.UniqMax <= 0 {
		cfg.UniqMax = store.DefaultUniqMax
	}
	key := make([]byte, 32)
	// crypto/rand.Read does not return an error as of Go 1.24 — it panics if
	// the system source fails. That is the right outcome here anyway: a
	// zero-valued key would silently reduce the log fingerprint to a bare
	// SHA-256, which is exactly what it exists to avoid, and nothing would look
	// any different.
	rand.Read(key)
	if cfg.PublicIP != "" {
		ip := net.ParseIP(strings.TrimSpace(cfg.PublicIP))
		if ip == nil {
			return nil, errors.New("admin: public ip is not an IP address")
		}
		cfg.PublicIP = ip.String()
	}
	prefixes, err := parseAdminTrustedProxies(cfg.AdminTrustedProxies)
	if err != nil {
		return nil, fmt.Errorf("admin: trusted proxies: %w", err)
	}
	s := &Server{cfg: cfg, hashes: newHashGate(0), loginFPKey: key, ktJob: newKeitaroJob(), trustedProxies: prefixes,
		audit: newAuditLog(cfg.DataDir), keyLim: newKeyLimiter(nil), kwSlots: make(chan struct{}, keywordSlots),
		lookups: newLookupHistory(cfg.DataDir)}
	s.ktJob = loadKeitaroJob(cfg.KeitaroJobFile, s.logWarn)
	hash, err := initialHash(cfg)
	if err != nil {
		return nil, err
	}
	if hash == "" {
		if cfg.PasswordFile == "" {
			if err := s.publish(hash); err != nil {
				return nil, err
			}
		}
		s.setup.Store(true)
	} else {
		if err := s.publish(hash); err != nil {
			return nil, err
		}
		if cfg.PasswordFile != "" {
			if _, statErr := os.Stat(cfg.PasswordFile); errors.Is(statErr, os.ErrNotExist) {
				next, seedErr := s.seedPasswordFile(cfg.PasswordFile, hash)
				if seedErr != nil {
					return nil, seedErr
				}
				if next != hash {
					if err := s.publish(next); err != nil {
						return nil, err
					}
				}
			}
		}
		if err := s.applyAdminProfile(); err != nil {
			return nil, err
		}
	}
	if err := s.loadKeitaro(); err != nil {
		return nil, err
	}
	s.loadNamecheap()
	s.loadCloudflare()
	s.loadChecker()
	if cfg.UsersFile != "" {
		us, err := security.LoadUserSet(cfg.UsersFile)
		if err != nil {
			return nil, fmt.Errorf("admin: users file %s: %w", cfg.UsersFile, err)
		}
		s.users = us
		if cfg.AdminUser != "" {
			us.SetOutsideAdmins(1)
		}
	}
	s.loadRoles()
	s.loadAPIKeys()
	if err := s.migrateBotProtection404(context.Background()); err != nil {
		return nil, err
	}
	if cfg.AITestBaseURL != "" {
		hp, err := aiTestHostPort(cfg.AITestBaseURL)
		if err != nil {
			return nil, fmt.Errorf("admin: AI test base url: %w", err)
		}
		s.logWarn("AI test endpoint is on: the openai provider calls a test address, never use this in production", "addr", hp)
	}
	s.aiRecoverChanges()
	s.aiRecoverChats()
	s.pickupKeitaroDeadline()
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

// seedPasswordFile creates path once with the hash already accepted for this start.
// path is the password file. hash is that accepted hash, never the password.
// It returns the hash the process should keep.
// The file is created with O_EXCL and mode 0600, so an existing file is not replaced.
// When the file appears between the absence check and the create, that file wins:
// a usable hash is returned, and an empty or unreadable file is an error.
// A write that cannot finish removes the new file, logs the path and the error
// without the hash, and returns hash so the start continues from the environment.
func (s *Server) seedPasswordFile(path, hash string) (string, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return initialHash(Config{PasswordFile: path})
		}
		s.logWarn("password file was not created", "path", path, "err", err)
		return hash, nil
	}
	_, werr := f.Write([]byte(hash))
	cerr := f.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(path)
		if werr == nil {
			werr = cerr
		}
		s.logWarn("password file was not created", "path", path, "err", werr)
		return hash, nil
	}
	return hash, nil
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
	s.routes, s.public = nil, nil
	mux := http.NewServeMux()
	s.handlePublic(mux, "GET /api/health", s.handleHealth)
	s.handlePublic(mux, "GET /healthz", s.handleHealthz)
	s.handlePublic(mux, "GET /api/setup", s.handleSetupGet)
	s.handlePublic(mux, "POST /api/setup", s.handleSetupPost)
	s.handlePublic(mux, "POST /api/login", s.handleLogin)
	s.handlePublic(mux, "POST /api/recover", s.handleRecover)
	s.handle(mux, "POST /api/logout", http.HandlerFunc(s.handleLogout))
	s.handle(mux, "GET /api/me", http.HandlerFunc(s.handleMe))
	s.handle(mux, "PUT /api/me/lang", http.HandlerFunc(s.handleMeLang))
	s.handle(mux, "PUT /api/me/tz", http.HandlerFunc(s.handleMeTZ))
	s.handle(mux, "GET /api/me/views", http.HandlerFunc(s.handleViewsGet))
	s.handle(mux, "PUT /api/me/views", http.HandlerFunc(s.handleViewsPut))
	s.handle(mux, "POST /api/password", http.HandlerFunc(s.handlePassword))
	registerAPIKeys(s, mux)
	registerUsers(s, mux)
	registerRoles(s, mux)
	registerAssistant(s, mux)

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
	s.handle(mux, "GET /api/postbacks/export", http.HandlerFunc(s.handlePostbacksExport))
	s.handle(mux, "GET /api/postback-log", http.HandlerFunc(s.handlePostbackLog))
	s.handle(mux, "GET /api/notices", http.HandlerFunc(s.handleNotices))
	s.handle(mux, "GET /api/openapi.json", http.HandlerFunc(s.handleOpenAPI))
	s.handle(mux, "GET /api/postman.json", http.HandlerFunc(s.handlePostman))
	s.handle(mux, "GET /api/report", http.HandlerFunc(s.handleReport))
	s.handle(mux, "GET /api/keys", http.HandlerFunc(s.handleKeys))
	s.handle(mux, "GET /api/keys/download", http.HandlerFunc(s.handleKeysDownload))
	s.handle(mux, "GET /api/keywords/stats", http.HandlerFunc(s.handleKeywordStats))
	s.handle(mux, "GET /api/keywords/trend", http.HandlerFunc(s.handleKeywordTrend))

	s.handle(mux, "GET /api/boards", http.HandlerFunc(s.handleBoardsGet))
	s.handle(mux, "POST /api/boards", http.HandlerFunc(s.handleBoardsPost))
	s.handle(mux, "PUT /api/boards/order", http.HandlerFunc(s.handleBoardsOrderPut))
	s.handle(mux, "PATCH /api/boards/{id}", http.HandlerFunc(s.handleBoardPatch))
	s.handle(mux, "DELETE /api/boards/{id}", http.HandlerFunc(s.handleBoardDelete))
	s.handle(mux, "PUT /api/boards/{id}/members", http.HandlerFunc(s.handleBoardMembersPut))
	s.handle(mux, "POST /api/boards/{id}/assign", http.HandlerFunc(s.handleBoardAssignPost))
	s.handle(mux, "POST /api/boards/import", http.HandlerFunc(s.handleBoardImportPost))
	s.handle(mux, "POST /api/boards/move", http.HandlerFunc(s.handleBoardMovePost))
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
	registerNamecheap(s, mux)
	registerCloudflare(s, mux)
	registerChecker(s, mux)
	s.handle(mux, "GET /api/spend", http.HandlerFunc(s.handleSpendList))
	s.handle(mux, "POST /api/spend", http.HandlerFunc(s.handleSpendCreate))
	s.handle(mux, "DELETE /api/spend/{id}", http.HandlerFunc(s.handleSpendDelete))
	s.handle(mux, "GET /api/stats/facts", http.HandlerFunc(s.handleFacts))
	s.handle(mux, "GET /api/bot-protection", http.HandlerFunc(s.handleBotGet))
	s.handle(mux, "PUT /api/bot-protection", http.HandlerFunc(s.handleBotPut))
	s.handle(mux, "GET /api/ui", http.HandlerFunc(s.handleUIGet))
	s.handle(mux, "PUT /api/ui", http.HandlerFunc(s.handleUIPut))
	s.handlePublic(mux, "GET /api/brand", s.handleBrandGet)
	s.handlePublic(mux, "GET /docs", s.docsGate(s.handleDocsPage))
	s.handlePublic(mux, "GET /api/docs/openapi.json", s.docsGate(s.handleDocsOpenAPI))
	s.handlePublic(mux, "GET /api/docs/postman.json", s.docsGate(s.handleDocsPostman))
	s.handlePublic(mux, "GET /api/docs.md", s.docsGate(s.handleDocsMarkdown))
	s.handlePublic(mux, "GET /llms.txt", s.docsGate(s.handleDocsMarkdown))
	s.handle(mux, "GET /api/docs/settings", http.HandlerFunc(s.handleDocsSettingsGet))
	s.handle(mux, "PUT /api/docs/settings", http.HandlerFunc(s.handleDocsSettingsPut))
	s.handle(mux, "GET /api/campaigns/{id}/postbacks", http.HandlerFunc(s.handleCampaignPostbacksGet))
	s.handle(mux, "PUT /api/campaigns/{id}/postbacks", http.HandlerFunc(s.handleCampaignPostbacksPut))
	s.handle(mux, "GET /api/s2s", http.HandlerFunc(s.handleS2SGet))
	s.handle(mux, "PUT /api/s2s", http.HandlerFunc(s.handleS2SPut))

	s.handle(mux, "GET /api/lists", http.HandlerFunc(s.handleListsIndex))
	s.handle(mux, "GET /api/lists/{name}", http.HandlerFunc(s.handleListRead))
	s.handle(mux, "PUT /api/lists/{name}", http.HandlerFunc(s.handleListWrite))

	s.handle(mux, "GET /api/pages", http.HandlerFunc(s.handlePagesIndex))
	s.handle(mux, "GET /api/pages/{name}", http.HandlerFunc(s.handlePageRead))
	s.handle(mux, "PUT /api/pages/{name}", http.HandlerFunc(s.handlePageWrite))
	s.handle(mux, "DELETE /api/pages/{name}", http.HandlerFunc(s.handlePageDelete))

	s.handle(mux, "GET /api/lookup", http.HandlerFunc(s.handleLookup))
	s.handle(mux, "GET /api/tools/lookups", http.HandlerFunc(s.handleLookupHistory))
	s.handle(mux, "DELETE /api/tools/lookups", http.HandlerFunc(s.handleLookupHistoryClear))
	s.handle(mux, "GET /api/tools/geo", http.HandlerFunc(s.handleToolsGeo))
	s.handle(mux, geoDownloadPattern, http.HandlerFunc(s.handleToolsGeoDownload))
	s.handle(mux, "POST /api/simulate", http.HandlerFunc(s.handleSimulate))
	s.handle(mux, "GET /api/cron", http.HandlerFunc(s.handleCronGet))
	s.handle(mux, "PUT /api/cron", http.HandlerFunc(s.handleCronSave))
	s.handle(mux, "POST /api/cron/run", http.HandlerFunc(s.handleCronRun))

	s.handle(mux, "GET /api/keitaro/status", http.HandlerFunc(s.handleKeitaroStatus))
	s.handle(mux, "GET /api/keitaro/settings", http.HandlerFunc(s.handleKeitaroSettingsGet))
	s.handle(mux, "PUT /api/keitaro/settings", http.HandlerFunc(s.handleKeitaroSettingsPut))
	s.handle(mux, "POST /api/keitaro/check", http.HandlerFunc(s.handleKeitaroCheck))
	s.handle(mux, "POST /api/keitaro/import", http.HandlerFunc(s.handleKeitaroImport))
	s.handle(mux, "GET /api/keitaro/job", http.HandlerFunc(s.handleKeitaroJob))
	s.handle(mux, "POST /api/keitaro/purge", http.HandlerFunc(s.handleKeitaroPurge))

	s.handle(mux, "GET /api/version", http.HandlerFunc(s.handleVersion))
	s.handle(mux, "GET /api/system", http.HandlerFunc(s.handleSystem))
	s.handle(mux, "GET /api/system/signals", http.HandlerFunc(s.handleSignals))
	s.handle(mux, "GET /api/update", http.HandlerFunc(s.handleUpdate))
	s.handle(mux, "GET /api/update/log", http.HandlerFunc(s.handleUpdateLog))
	s.handle(mux, "POST /api/update/apply", http.HandlerFunc(s.handleUpdateApply))
	s.handle(mux, "POST /api/update/rollback", http.HandlerFunc(s.handleUpdateRollback))
	s.handle(mux, "POST /api/update/check", http.HandlerFunc(s.handleUpdateCheck))

	// Embedded web interface (SPA).
	mux.Handle("GET /assets/", serveAssets())
	mux.HandleFunc("GET /{$}", serveUI)
	return gzipMiddleware(securityHeaders(toonFormat(originMiddleware(s.adminHostMiddleware(apiJSONErrors(mux))))))
}

// statusProbe records the status and headers a handler answers with and drops its body.
type statusProbe struct {
	header http.Header
	status int
}

func (p *statusProbe) Header() http.Header { return p.header }

func (p *statusProbe) Write(b []byte) (int, error) { return len(b), nil }

func (p *statusProbe) WriteHeader(code int) {
	if p.status == 0 {
		p.status = code
	}
}

// isAPIPath reports whether path is the API root /api or lies under /api/.
// path is the URL path of the request. It does not fail.
func isAPIPath(path string) bool {
	return path == "/api" || strings.HasPrefix(path, "/api/")
}

// apiJSONErrors makes the 404 and 405 the mux gives for /api or a path under /api/ a JSON answer
// with writeErr, so an API client reads the same {error, code} body as for any other failure.
// next is the mux. A 405 keeps the Allow header of the mux. Paths outside the API, routes
// that match, and every other answer of the mux (a redirect to the clean path) pass through
// unchanged. It does not return an error.
func apiJSONErrors(next *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isAPIPath(r.URL.Path) {
			if h, pattern := next.Handler(r); pattern == "" {
				probe := &statusProbe{header: http.Header{}}
				h.ServeHTTP(probe, r)
				switch probe.status {
				case http.StatusNotFound:
					writeErr(w, http.StatusNotFound, "not found")
					return
				case http.StatusMethodNotAllowed:
					if allow := probe.header.Get("Allow"); allow != "" {
						w.Header().Set("Allow", allow)
					}
					writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// handleNotices writes the system notices: the engine's warnings, then the
// Redis memory alert and the event loss alert from the cron status when they
// are raised, then an update available line for administrators when a fresh
// check is available. The body is a list of lines, empty rather than null.
// An unreachable engine contributes no lines.
func (s *Server) handleNotices(w http.ResponseWriter, r *http.Request) {
	lines := s.engineNotices(r.Context())
	if s.cfg.CronFile != "" {
		if st, err := cron.ReadStatus(cron.StatusPath(s.cfg.CronFile)); err == nil {
			if line, ok := cron.RedisNotice(st, time.Now()); ok {
				lines = append(lines, line)
			}
			if line, ok := cron.LossNotice(st, time.Now()); ok {
				lines = append(lines, line)
			}
			if line, ok := cron.SpoolNotice(st, time.Now()); ok {
				lines = append(lines, line)
			}
		}
	}
	sess := sessionFrom(r.Context())
	if s.isAdmin(sess) && s.cfg.UpdateDir != "" {
		if line, ok := s.updateNotice(time.Now()); ok {
			lines = append(lines, line)
		}
	}
	writeJSON(w, http.StatusOK, lines)
}

// engineNotices reads the engine's warning lines within two seconds.
// It returns an empty list when the engine URL is unset, unreachable or answers garbage.
func (s *Server) engineNotices(ctx context.Context) []string {
	base := strings.TrimRight(s.cfg.EngineURL, "/")
	if base == "" {
		return []string{}
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/internal/notices", nil)
	if err != nil {
		return []string{}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return []string{}
	}
	defer resp.Body.Close()
	var lines []string
	if err := json.NewDecoder(resp.Body).Decode(&lines); err != nil || lines == nil {
		return []string{}
	}
	return lines
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
		c, err := sessionCookie(r)
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
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "frame-ancestors 'none'")
		h.Set("X-Robots-Tag", "noindex")
		path := r.URL.Path
		switch {
		case isAPIPath(path):
			h.Set("Cache-Control", "private, no-store")
		case path == "/":
			h.Set("Cache-Control", "no-cache")
		case strings.HasPrefix(path, "/assets/"):
			h.Set("Cache-Control", "private, max-age=86400")
		}
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

func atoiDef(s string, def int) int {
	if s == "" {
		return def
	}
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

// writeJSON answers with v encoded as JSON and status code. When the request asked for TOON
// (toonFormat marked w), the same JSON document goes out as text/toon instead.
func writeJSON(w http.ResponseWriter, code int, v any) {
	if toonRequested(w) {
		if body, err := json.Marshal(v); err == nil {
			writeJSONBytes(w, code, body)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// writeJSONBytes answers with the encoded JSON document body and status code, converted to
// text/toon when the request asked for TOON. A body the converter refuses goes out as JSON.
func writeJSONBytes(w http.ResponseWriter, code int, body []byte) {
	if toonRequested(w) {
		if out, err := toonFromJSON(body); err == nil {
			w.Header().Set("Content-Type", toonContentType)
			w.WriteHeader(code)
			_, _ = w.Write(out)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write(body)
}

// writeErr writes {"error": msg, "code": <stable code>} with status code.
// code is the HTTP status and msg the error text, which is left unchanged.
func writeErr(w http.ResponseWriter, code int, msg string) {
	writeErrParams(w, code, msg, nil)
}

// writeErrParams is writeErr for an error that has params: {"error", "code", "params"}. With no
// params the answer is the one writeErr writes and has no "params" field.
func writeErrParams(w http.ResponseWriter, code int, msg string, params map[string]any) {
	body := map[string]any{"error": msg, "code": errCode(code, msg)}
	if len(params) > 0 {
		body["params"] = params
	}
	writeJSON(w, code, body)
}

// writeRetryLater writes a 429 answer that tells when the request may be repeated.
// w is the response, msg the error text and wait the time left until a repeat is accepted,
// rounded up to whole seconds and never below one. The answer carries the Retry-After header and
// {"error", "code": "too_many_requests", "retry_after", "params": {"retry_after"}}. There is no error return.
func writeRetryLater(w http.ResponseWriter, msg string, wait time.Duration) {
	secs := int((wait + time.Second - 1) / time.Second)
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeJSON(w, http.StatusTooManyRequests, map[string]any{
		"error": msg, "code": errCode(http.StatusTooManyRequests, msg),
		"retry_after": secs, "params": map[string]any{"retry_after": secs},
	})
}

// errCode returns the stable machine-readable code of an error answer.
// status is the HTTP status and msg the error text. A message made by newAIError
// ("ai_stale: ...", "keitaro_running: ...") keeps its own code whatever the status.
// The messages that start
// with "action required: ", "cookie session required", msgKeyExpired and msgQueryTooHeavy have their own codes;
// 404 is not_found and 405 is method_not_allowed;
// every other answer gets one derived from status: 5xx other than 502 and 503
// is internal, and a 4xx without its own name is bad_request.
func errCode(status int, msg string) string {
	if c := aiCodeOf(msg); c != "" {
		return c
	}
	switch {
	case strings.HasPrefix(msg, "action required: "):
		return "action_required"
	case msg == "cookie session required":
		return "cookie_required"
	case msg == msgKeyExpired:
		return "key_expired"
	case msg == msgQueryTooHeavy:
		return "query_too_heavy"
	}
	switch status {
	case http.StatusUnauthorized:
		return "unauthorized"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusMethodNotAllowed:
		return "method_not_allowed"
	case http.StatusConflict:
		return "conflict"
	case http.StatusRequestEntityTooLarge:
		return "too_large"
	case http.StatusTooManyRequests:
		return "too_many_requests"
	case http.StatusBadGateway:
		return "bad_gateway"
	case http.StatusServiceUnavailable:
		return "unavailable"
	}
	if status >= http.StatusInternalServerError {
		return "internal"
	}
	return "bad_request"
}
