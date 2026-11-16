// Package cron is the background service of EmTDS: the periodic chores the
// engine must not do on its hot path and the admin should not do in a request.
//
// Jobs: refresh bot IP lists, refresh the geo databases, check domains with
// VirusTotal, watch free disk space, delete old keyword files, and announce
// conversions — with Telegram as the one notification channel. Besides the
// jobs, the service checks Redis memory every minute (see WatchRedis).
//
// The service talks to the rest of the system through files only. Its config
// is a JSON file the admin panel edits; its state is a JSON file the admin
// panel reads; a "run now" request is a line in a third file. What it
// produces are the same .dat, .mmdb and groups files the engine already
// hot-reloads, so nothing here needs a connection to the engine.
package cron

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/egerkuzma/emtds/internal/atomicfile"
	"github.com/egerkuzma/emtds/internal/detect"
)

// Job names, as used in the config, the status file and "run now" requests.
const (
	JobIPLists     = "ip_lists"
	JobGeoDB       = "geo_db"
	JobVirusTotal  = "virustotal"
	JobDisk        = "disk"
	JobCleanup     = "cleanup"
	JobConversions = "conversions"
	JobKeitaroSync = "keitaro_sync"
	// JobTelegramTest is not scheduled: it exists so the admin panel can ask
	// for a test message.
	JobTelegramTest = "telegram_test"
)

// Jobs lists the scheduled jobs in display order.
var Jobs = []string{JobIPLists, JobGeoDB, JobVirusTotal, JobDisk, JobCleanup, JobConversions}

// AdminJobs lists tasks configured in cron.json but executed only by the admin
// process (not by this cron runner).
var AdminJobs = []string{JobKeitaroSync}

// MaxKeitaroSyncInterval is the longest allowed keitaro_sync period, in minutes (7 days).
const MaxKeitaroSyncInterval = 10080

// Config is the whole cron configuration (one JSON file).
type Config struct {
	Telegram    Telegram    `json:"telegram"`
	IPLists     IPLists     `json:"ip_lists"`
	GeoDB       GeoDB       `json:"geo_db"`
	VirusTotal  VirusTotal  `json:"virustotal"`
	Disk        Disk        `json:"disk"`
	Cleanup     Cleanup     `json:"cleanup"`
	Conversions Conversions `json:"conversions"`
	KeitaroSync KeitaroSync `json:"keitaro_sync"`

	// Seeded lists the default sources already offered to this installation
	// (see the Seed constants). A source the user removed stays removed because
	// its key is recorded here.
	Seeded []string `json:"seeded,omitempty"`
}

// Kinds of a GeoSource: which database file the source replaces.
const (
	KindCity    = "city"
	KindASN     = "asn"
	KindDBIPASN = "dbip_asn"
)

// Keys of Config.Seeded: default sources added to configs written before they existed.
// A key names a default source, not a kind.
const (
	SeedDBIPASN      = "dbip_asn"
	SeedPrivateRelay = "ip_private_relay"
)

// DBIPASNURL is the URL template of the DB-IP ASN Lite database, the one source of it.
// {YYYY-MM} is the month; see MonthPlaceholder.
const DBIPASNURL = "https://download.db-ip.com/free/dbip-asn-lite-" + MonthPlaceholder + ".mmdb.gz"

// DBIPASNHost is the only host a dbip_asn source may download from or be redirected to.
const DBIPASNHost = "download.db-ip.com"

// MonthPlaceholder in a geo source URL stands for the current month, as YYYY-MM in UTC.
const MonthPlaceholder = "{YYYY-MM}"

var (
	// seedGeoSource is the DB-IP ASN source added under SeedDBIPASN.
	seedGeoSource = GeoSource{Kind: KindDBIPASN, URL: DBIPASNURL}
	// seedIPSource is the iCloud Private Relay source added under SeedPrivateRelay.
	seedIPSource = IPSource{URL: "https://mask-api.icloud.com/egress-ip-ranges.csv", Target: detect.ListPrivateRelay, Mode: "replace"}
)

// KeitaroSync — periodic log catch-up from Keitaro (admin executor only).
type KeitaroSync struct {
	Enabled      bool `json:"enabled"`
	EveryMinutes int  `json:"every_minutes"`
}

// Telegram — where notifications go.
type Telegram struct {
	Token  string `json:"token"`   // bot token (secret)
	ChatID string `json:"chat_id"` // chat, group or channel id
}

// Configured reports whether a message can be sent at all.
func (t Telegram) Configured() bool { return t.Token != "" && t.ChatID != "" }

// IPLists — refresh .dat lists from URLs.
type IPLists struct {
	Enabled      bool       `json:"enabled"`
	EveryMinutes int        `json:"every_minutes"`
	Sources      []IPSource `json:"sources"`
}

// IPSource is one URL.
//
// With Target set, the whole response goes into that list: a plain text list
// (one IP, CIDR or range per line, "# label" lines kept — that is how wap.dat
// names its operators), or the JSON Google and Bing publish for their
// crawlers ({"prefixes":[{"ipv4Prefix":…}]}).
//
// With Target empty, the response is a sectioned text file: "# google",
// "# yandex"… each start a section, and section <name> goes to ip_<name>.
type IPSource struct {
	URL    string `json:"url"`
	Target string `json:"target"` // list name without .dat, or "" for a sectioned file
	Mode   string `json:"mode"`   // "replace" (default) | "merge"
}

// GeoDB — refresh the MaxMind-format databases.
type GeoDB struct {
	Enabled      bool        `json:"enabled"`
	EveryMinutes int         `json:"every_minutes"`
	Sources      []GeoSource `json:"sources"`
}

// GeoSource is one database download. Kind says which file it replaces: the
// paths themselves come from the service's environment (EMTDS_GEO_DB,
// EMTDS_ASN_DB, EMTDS_DBIP_ASN_DB), not from this config — the admin panel can
// change what is downloaded, never where it is written.
type GeoSource struct {
	Kind     string `json:"kind"`     // KindCity | KindASN | KindDBIPASN
	URL      string `json:"url"`      // .mmdb, .mmdb.gz or .tar.gz; may contain {YYYY-MM}
	User     string `json:"user"`     // basic auth (MaxMind: account id)
	Password string `json:"password"` // basic auth (MaxMind: license key; secret)
}

// VirusTotal — check domains the streams send traffic to.
type VirusTotal struct {
	Enabled        bool     `json:"enabled"`
	EveryMinutes   int      `json:"every_minutes"`
	APIKey         string   `json:"api_key"`         // secret
	Domains        []string `json:"domains"`         // checked always
	FromGroups     bool     `json:"from_groups"`     // plus every host found in group/stream outputs
	Threshold      int      `json:"threshold"`       // malicious+suspicious verdicts that count as flagged (min 1)
	DisableStreams bool     `json:"disable_streams"` // switch off streams that send to a flagged domain
}

// Disk — warn when free space runs low.
type Disk struct {
	Enabled        bool     `json:"enabled"`
	EveryMinutes   int      `json:"every_minutes"`
	Paths          []string `json:"paths"`            // empty = the data directory
	MinFreePercent int      `json:"min_free_percent"` // alert below this (default 10)
}

// Cleanup — delete collected keyword files older than KeysDays.
type Cleanup struct {
	Enabled      bool `json:"enabled"`
	EveryMinutes int  `json:"every_minutes"`
	KeysDays     int  `json:"keys_days"`
}

// Conversions — announce new postbacks.
type Conversions struct {
	Enabled      bool   `json:"enabled"`
	EveryMinutes int    `json:"every_minutes"`
	Template     string `json:"template"` // [PROFIT] [GROUP] [STREAM] [COUNTRY] [CITY] [DEVICE] [DOMAIN] [CID]
}

// DefaultConversionTemplate is used when the template is empty.
const DefaultConversionTemplate = "💰 [PROFIT] — [GROUP] / [STREAM] · [COUNTRY] · [DEVICE]"

// Default returns the configuration a fresh installation starts with:
// everything off, intervals and sources filled in so that turning a job on
// is one switch.
func Default() Config {
	return Config{
		IPLists: IPLists{EveryMinutes: 24 * 60, Sources: []IPSource{
			{URL: "https://developers.google.com/static/crawling/ipranges/common-crawlers.json", Target: "ip_google", Mode: "replace"},
			{URL: "https://developers.google.com/static/crawling/ipranges/special-crawlers.json", Target: "ip_google", Mode: "merge"},
			{URL: "https://developers.google.com/static/crawling/ipranges/user-triggered-fetchers.json", Target: "ip_google", Mode: "merge"},
			{URL: "https://www.bing.com/toolbox/bingbot.json", Target: "ip_bing", Mode: "replace"},
			{URL: "https://search.developer.apple.com/applebot.json", Target: "ip_others", Mode: "merge"},
			{URL: "https://openai.com/gptbot.json", Target: "ip_others", Mode: "merge"},
			{URL: "https://openai.com/searchbot.json", Target: "ip_others", Mode: "merge"},
			{URL: "https://openai.com/chatgpt-user.json", Target: "ip_others", Mode: "merge"},
			{URL: "https://openai.com/adsbot.json", Target: "ip_others", Mode: "merge"},
			{URL: "https://claude.com/crawling/bots.json", Target: "ip_others", Mode: "merge"},
			{URL: "https://www.perplexity.ai/perplexitybot.json", Target: "ip_others", Mode: "merge"},
			{URL: "https://www.perplexity.ai/perplexity-user.json", Target: "ip_others", Mode: "merge"},
			{URL: "https://duckduckgo.com/duckduckbot.json", Target: "ip_others", Mode: "merge"},
			{URL: "https://duckduckgo.com/duckassistbot.json", Target: "ip_others", Mode: "merge"},
			{URL: "https://index.commoncrawl.org/ccbot.json", Target: "ip_others", Mode: "merge"},
			{URL: "https://api.ahrefs.com/v3/public/crawler-ip-ranges", Target: "ip_others", Mode: "merge"},
			{URL: "https://raw.githubusercontent.com/X4BNet/lists_vpn/main/output/datacenter/ipv4.txt", Target: "ip_proxy", Mode: "replace"},
			{URL: "https://raw.githubusercontent.com/X4BNet/lists_vpn/main/output/vpn/ipv4.txt", Target: "ip_proxy", Mode: "merge"},
			{URL: "https://iplists.firehol.org/files/proxylists_1d.ipset", Target: "ip_proxy", Mode: "merge"},
			{URL: "https://iplists.firehol.org/files/socks_proxy_1d.ipset", Target: "ip_proxy", Mode: "merge"},
			{URL: "https://iplists.firehol.org/files/tor_exits.ipset", Target: "ip_proxy", Mode: "merge"},
			seedIPSource,
		}},
		GeoDB: GeoDB{EveryMinutes: 3 * 24 * 60, Sources: []GeoSource{
			{Kind: KindCity, URL: "https://download.maxmind.com/geoip/databases/GeoLite2-City/download?suffix=tar.gz"},
			{Kind: KindASN, URL: "https://download.maxmind.com/geoip/databases/GeoLite2-ASN/download?suffix=tar.gz"},
			seedGeoSource,
		}},
		VirusTotal:  VirusTotal{EveryMinutes: 12 * 60, FromGroups: true, Threshold: 1},
		Disk:        Disk{EveryMinutes: 10, MinFreePercent: 10},
		Cleanup:     Cleanup{EveryMinutes: 24 * 60, KeysDays: 30},
		Conversions: Conversions{EveryMinutes: 1, Template: DefaultConversionTemplate},
		KeitaroSync: KeitaroSync{EveryMinutes: 15},
		Seeded:      []string{SeedDBIPASN, SeedPrivateRelay},
	}
}

// seed adds, in memory, each default source whose key is missing from Seeded
// and records the key. A source already present (same kind and URL, or same
// target and URL) is not added twice. The receiver is changed in place.
func (c *Config) seed() {
	has := func(key string) bool {
		for _, k := range c.Seeded {
			if k == key {
				return true
			}
		}
		return false
	}
	if !has(SeedDBIPASN) {
		found := false
		for _, s := range c.GeoDB.Sources {
			found = found || (s.Kind == seedGeoSource.Kind && cleanURL(s.URL) == seedGeoSource.URL)
		}
		if !found {
			c.GeoDB.Sources = append(append([]GeoSource{}, c.GeoDB.Sources...), seedGeoSource)
		}
		c.Seeded = append(c.Seeded, SeedDBIPASN)
	}
	if !has(SeedPrivateRelay) {
		found := false
		for _, s := range c.IPLists.Sources {
			found = found || (s.Target == seedIPSource.Target && cleanURL(s.URL) == seedIPSource.URL)
		}
		if !found {
			c.IPLists.Sources = append(append([]IPSource{}, c.IPLists.Sources...), seedIPSource)
		}
		c.Seeded = append(c.Seeded, SeedPrivateRelay)
	}
}

// KeepSeeded returns c with Seeded set to the union of its own keys and the
// stored ones, so a save from a client that does not send the field cannot
// make a removed default source come back.
func (c Config) KeepSeeded(stored Config) Config {
	seen := map[string]bool{}
	var out []string
	for _, list := range [][]string{stored.Seeded, c.Seeded} {
		for _, k := range list {
			if !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	c.Seeded = out
	return c
}

// ExpandMonth replaces every {YYYY-MM} in rawURL with the month of t in UTC.
// It returns rawURL unchanged when there is no placeholder.
func ExpandMonth(rawURL string, t time.Time) string {
	return strings.ReplaceAll(rawURL, MonthPlaceholder, t.UTC().Format("2006-01"))
}

// geoURLs returns the URLs to try for a geo source at time now, in order: the
// URL with the current month and, when it has the placeholder, the previous
// month, because a monthly file is published some days into the month.
func geoURLs(rawURL string, now time.Time) []string {
	rawURL = cleanURL(rawURL)
	if !strings.Contains(rawURL, MonthPlaceholder) {
		return []string{rawURL}
	}
	now = now.UTC()
	prev := time.Date(now.Year(), now.Month()-1, 1, 0, 0, 0, 0, time.UTC)
	return []string{ExpandMonth(rawURL, now), ExpandMonth(rawURL, prev)}
}

// dbipURLAllowed reports whether u may be fetched for a dbip_asn source:
// https and exactly the DB-IP download host.
func dbipURLAllowed(u *url.URL) bool {
	return u != nil && u.Scheme == "https" && u.Host == DBIPASNHost
}

// Floors returns a copy of minimum schedule intervals in minutes for each job.
// The ceiling for keitaro_sync is MaxKeitaroSyncInterval, enforced in CheckIntervals.
func Floors() map[string]int {
	out := make(map[string]int, len(minInterval))
	for job, m := range minInterval {
		out[job] = m
	}
	return out
}

// CheckIntervals rejects enabled jobs whose every_minutes is below the floor.
// keitaro_sync is also capped at MaxKeitaroSyncInterval when enabled.
// Validate does not call this; the admin uses it before saving cron.json.
// Returns nil when every enabled job is within bounds.
func CheckIntervals(cfg Config) error {
	for _, job := range append(append([]string{}, Jobs...), AdminJobs...) {
		if !cfg.Enabled(job) {
			continue
		}
		m := cfg.intervalMinutes(job)
		if lo := minInterval[job]; m < lo {
			return fmt.Errorf("%s: interval %d min is below minimum %d min", job, m, lo)
		}
		if job == JobKeitaroSync && m > MaxKeitaroSyncInterval {
			return fmt.Errorf("%s: interval %d min exceeds maximum %d min", job, m, MaxKeitaroSyncInterval)
		}
	}
	return nil
}

// intervalMinutes reads every_minutes from cfg for job without clamping.
func (c Config) intervalMinutes(job string) int {
	switch job {
	case JobIPLists:
		return c.IPLists.EveryMinutes
	case JobGeoDB:
		return c.GeoDB.EveryMinutes
	case JobVirusTotal:
		return c.VirusTotal.EveryMinutes
	case JobDisk:
		return c.Disk.EveryMinutes
	case JobCleanup:
		return c.Cleanup.EveryMinutes
	case JobConversions:
		return c.Conversions.EveryMinutes
	case JobKeitaroSync:
		return c.KeitaroSync.EveryMinutes
	default:
		return 0
	}
}

// minimum intervals: a typo must not turn a job into a tight loop against
// someone else's API.
var minInterval = map[string]int{
	JobIPLists: 10, JobGeoDB: 60, JobVirusTotal: 30, JobDisk: 1, JobCleanup: 60, JobConversions: 1,
	JobKeitaroSync: 5,
}

// Interval returns the job's period, clamped to its floor and, for
// keitaro_sync, to MaxKeitaroSyncInterval.
func (c Config) Interval(job string) time.Duration {
	m := c.intervalMinutes(job)
	if lo := minInterval[job]; m < lo {
		m = lo
	}
	if job == JobKeitaroSync && m > MaxKeitaroSyncInterval {
		m = MaxKeitaroSyncInterval
	}
	return time.Duration(m) * time.Minute
}

// Enabled reports whether the job is switched on.
func (c Config) Enabled(job string) bool {
	switch job {
	case JobIPLists:
		return c.IPLists.Enabled
	case JobGeoDB:
		return c.GeoDB.Enabled
	case JobVirusTotal:
		return c.VirusTotal.Enabled
	case JobDisk:
		return c.Disk.Enabled
	case JobCleanup:
		return c.Cleanup.Enabled
	case JobConversions:
		return c.Conversions.Enabled
	case JobKeitaroSync:
		return c.KeitaroSync.Enabled
	}
	return false
}

var listNameRe = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

// Validate rejects a configuration the service could not run safely. It is
// called by the admin before saving and by the service after loading, so a
// file edited by hand gets the same checks.
func (c Config) Validate() error {
	for i, s := range c.IPLists.Sources {
		if err := httpURL(s.URL); err != nil {
			return fmt.Errorf("ip list source %d: %w", i+1, err)
		}
		if s.Target != "" && !listNameRe.MatchString(s.Target) {
			return fmt.Errorf("ip list source %d: target %q must be a list name like ip_google", i+1, s.Target)
		}
		if s.Mode != "" && s.Mode != "replace" && s.Mode != "merge" {
			return fmt.Errorf("ip list source %d: mode must be replace or merge", i+1)
		}
	}
	for i, s := range c.GeoDB.Sources {
		if s.Kind != KindCity && s.Kind != KindASN && s.Kind != KindDBIPASN {
			return fmt.Errorf("geo source %d: kind must be city, asn or dbip_asn", i+1)
		}
		if err := httpURL(ExpandMonth(s.URL, time.Now())); err != nil {
			return fmt.Errorf("geo source %d: %w", i+1, err)
		}
		if s.Kind == KindDBIPASN {
			if u, err := url.Parse(cleanURL(ExpandMonth(s.URL, time.Now()))); err != nil || !dbipURLAllowed(u) {
				return fmt.Errorf("geo source %d: dbip_asn must be an https URL on %s", i+1, DBIPASNHost)
			}
		}
	}
	for _, d := range c.VirusTotal.Domains {
		if !domainRe.MatchString(d) {
			return fmt.Errorf("virustotal: %q is not a domain", d)
		}
	}
	if p := c.Disk.MinFreePercent; p < 0 || p > 99 {
		return errors.New("disk: min free percent must be 0..99")
	}
	if c.Cleanup.KeysDays < 0 {
		return errors.New("cleanup: keys days must not be negative")
	}
	return nil
}

var domainRe = regexp.MustCompile(`^(?i)[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)

// cleanURL returns a source URL as it is used: without the spaces a pasted
// value often carries. Validation and every download go through it.
func cleanURL(raw string) string { return strings.TrimSpace(raw) }

func httpURL(raw string) error {
	u, err := url.Parse(cleanURL(raw))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("%q is not an http(s) URL", raw)
	}
	return nil
}

// Load reads the config file. A missing file is not an error: it yields the
// defaults, which is what a fresh installation has. A file written before a
// default source existed gets that source added in memory (see Config.Seeded);
// the file itself is not rewritten.
func Load(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Default(), nil
		}
		return Config{}, err
	}
	c := Default()
	// What the file has not been offered yet is decided by the file alone.
	c.Seeded = nil
	// Slices in the file replace the default ones wholesale (that is how
	// encoding/json treats them), so removing a default source sticks.
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("cron config %s: %w", path, err)
	}
	c.seed()
	return c, nil
}

// Credentials returns the user and password to send with a download of s.
// A dbip_asn source gets none, whatever the source holds: DB-IP is public, and
// a license key must never reach it. This is the only place that decides.
func (s GeoSource) Credentials() (user, password string) {
	if s.Kind == KindDBIPASN {
		return "", ""
	}
	return s.User, s.Password
}
// Save writes the config file atomically, readable by the owner only: it
// holds a bot token and API keys.
func Save(path string, c Config) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(path, b, 0o600)
}

// SecretMask stands in for a stored secret in what the admin API returns.
// Sending it back unchanged means "keep what is stored".
const SecretMask = "********"

func mask(s string) string {
	if s == "" {
		return ""
	}
	return SecretMask
}

// Redacted returns a copy with every secret replaced by SecretMask.
func (c Config) Redacted() Config {
	c.Telegram.Token = mask(c.Telegram.Token)
	c.VirusTotal.APIKey = mask(c.VirusTotal.APIKey)
	src := make([]GeoSource, len(c.GeoDB.Sources))
	for i, s := range c.GeoDB.Sources {
		s.Password = mask(s.Password)
		src[i] = s
	}
	c.GeoDB.Sources = src
	return c
}

// KeepSecrets fills every secret the client sent back as SecretMask from the
// stored config. A geo source is matched by kind and URL, not by position:
// the client may have reordered or removed rows, and handing one source's
// license key to another URL would send it to the wrong host.
func (c Config) KeepSecrets(stored Config) Config {
	if c.Telegram.Token == SecretMask {
		c.Telegram.Token = stored.Telegram.Token
	}
	if c.VirusTotal.APIKey == SecretMask {
		c.VirusTotal.APIKey = stored.VirusTotal.APIKey
	}
	src := make([]GeoSource, len(c.GeoDB.Sources))
	for i, s := range c.GeoDB.Sources {
		if s.Password == SecretMask {
			s.Password = ""
			for _, old := range stored.GeoDB.Sources {
				if old.Kind == s.Kind && old.URL == s.URL {
					s.Password = old.Password
					break
				}
			}
		}
		src[i] = s
	}
	c.GeoDB.Sources = src
	return c
}
