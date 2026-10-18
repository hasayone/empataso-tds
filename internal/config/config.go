// Package config provides the typed configuration for kuztds.
//
// Secrets are not hardcoded here: they come from
// environment variables / a secret file. The stream config is
// loaded separately into a RAM cache with hot-reload — see ARCHITECTURE.md.
package config

import (
	"strconv"
	"time"
)

// App is the static process configuration (from yaml + env).
type App struct {
	ListenAddr     string        `yaml:"listen_addr"`
	TrustedProxies []string      `yaml:"trusted_proxies"` // CIDRs we trust for XFF/CF
	RequestTimeout time.Duration `yaml:"request_timeout"`
	GeoDBPath      string        `yaml:"geo_db_path"` // mmdb
	DataDir        string        `yaml:"data_dir"`    // directory of .dat lists
	ReloadInterval time.Duration `yaml:"reload_interval"`

	ClickHouse ClickHouse `yaml:"clickhouse"`
	Redis      Redis      `yaml:"redis"`
	Secrets    Secrets    `yaml:"-"` // only from env, not from a file in VCS
}

type ClickHouse struct {
	Addr     string `yaml:"addr"`
	Database string `yaml:"database"`
	Username string `yaml:"username"`
}

type Redis struct {
	Addr string `yaml:"addr"`
	DB   int    `yaml:"db"`
}

// Secrets are read only from the environment (KUZTDS_*), never from a file in the repo.
type Secrets struct {
	ClickHousePassword string
	RedisPassword      string
	ParamHMACKey       string // signature of packed parameters [ex]
	AdminSessionKey    string
}

// MaxGroupHops bounds a chain of "group" links (a stream or a default that
// hands the visitor to another group), so that groups pointing at each other
// end in the trash answer instead of spinning. The engine enforces it; the
// admin's simulator follows the same limit.
const MaxGroupHops = 3

// Group is a group of streams.
// Empty optional fields are omitted from JSON. ID, Name and Status are always written.
type Group struct {
	ID       string   `json:"id"`
	Aliases  []string `json:"aliases,omitempty"` // alternative IDs for routing (http://tds/<alias>)
	Name     string   `json:"name"`
	Status   bool     `json:"status"`
	Redirect string   `json:"redirect,omitempty"`
	Header   string   `json:"header,omitempty"`
	Out      string   `json:"out,omitempty"`
	// Geo picks which country source wins when both answer: "cf" — the CDN's
	// CF-IPCountry header first, the geo database as a fallback; anything else
	// ("db", or the legacy "sypex") — the database first, the header as a
	// fallback. City, region, time zone and ASN always come from the database.
	Geo string `json:"geo,omitempty"`

	UniqMethod  string       `json:"uniq_method,omitempty"`  // "ip" (default) | "cookie"
	UniqSeconds int          `json:"uniq_seconds,omitempty"` // uniqueness window
	Firewall    FirewallRule `json:"firewall,omitzero"`      // antiflood; a zero rule is omitted
	SaveKeys    bool         `json:"save_keys,omitempty"`    // save keywords
	SaveKeysSE  bool         `json:"save_keys_se,omitempty"` // save keywords from search engines

	// Weight, when true, picks among matching streams by Stream.Weight
	// instead of taking the first match.
	Weight bool `json:"weight,omitempty"`
	// StickyStream, when true, sends a returning visitor to the stream they got
	// last time. The stream is remembered by name for UniqSeconds: in the cookie
	// zts_<group> with uniq_method=cookie, in Redis st:<group>:<ip> with
	// uniq_method=ip. It works only with Weight, in the engine's own mode (not the
	// api), and a stream that no longer matches the visitor is drawn again.
	StickyStream bool `json:"sticky_stream,omitempty"`
	// Num is the stable number shown in the campaign list. It is not the link.
	Num int `json:"num,omitempty"`
	// DomainID picks the parked host used as the public link base.
	// Empty uses the primary links domain, then the engine URL.
	DomainID string `json:"domain_id,omitempty"`
	// SourceID links a traffic source whose parameter map names query keys.
	SourceID string `json:"source_id,omitempty"`
	// Folder is the operator's grouping label for the campaign list and its filter. The engine ignores it.
	Folder string `json:"folder,omitempty"`
	// CreatedBy is the users.json id of the panel user who created this record.
	// Empty means the environment administrator or a record older than this field.
	// The engine does not read it.
	CreatedBy string `json:"created_by,omitempty"`
	// CostParam and CurrencyParam are the query names for a click price.
	// CostToken and CurrencyToken are written into the campaign link as the
	// values the traffic source replaces, for example {price}.
	CostParam     string `json:"cost_param,omitempty"`
	CostToken     string `json:"cost_token,omitempty"`
	CurrencyParam string `json:"currency_param,omitempty"`
	CurrencyToken string `json:"currency_token,omitempty"`
	// Postbacks are outbound URLs fired after an inbound conversion.
	Postbacks []OutboundPostback `json:"postbacks,omitempty"`
	// id шаблонов из общего каталога s2s.json.
	PostbackIDs []string `json:"postback_ids,omitempty"`

	Streams []Stream `json:"streams"`
}

// FirewallRule is a request limit from a single IP per window.
type FirewallRule struct {
	Enabled bool `json:"enabled"`
	Queries int  `json:"queries"` // g_f_queries
	Seconds int  `json:"seconds"` // g_f_time
}

// Stream is a stream with filtering rules and output parameters.
// The rules are factored out into Rules as data — the router runs them in a loop
// (declarative rules executed by the router in a loop).
// Zero rules, output, bots, separation, remote and api_mac are omitted.
// Name and Status are always written.
type Stream struct {
	Name    string `json:"name"`
	Status  bool   `json:"status"`
	Comment string `json:"comment,omitempty"` // operator's note, not used by the engine
	Rules   Rules  `json:"rules,omitzero"`
	// FilterOr matches the stream when any one configured filter passes.
	FilterOr bool `json:"filter_or,omitempty"`
	// Weight is the campaign-weight share of this stream. Used only when the
	// group has Weight set.
	Weight     int        `json:"weight,omitempty"`
	Out        Output     `json:"out,omitzero"`
	Bots       Bots       `json:"bots,omitzero"`
	Separation Separation `json:"separation,omitzero"`
	Curl       string     `json:"curl,omitempty"`   // find/replace for CURL redirect
	BotCurl    string     `json:"b_curl,omitempty"` // find/replace for CURL to bots
	Remote     Remote     `json:"remote,omitzero"`  // [REMOTE] fetch
	APIMac     APIMac     `json:"api_mac,omitzero"` // mac code in the API response
}

// Remote fetches external content into [REMOTE] (with caching and regex parsing).
// Empty fields are omitted from JSON.
type Remote struct {
	Enabled  bool   `json:"enabled,omitempty"`
	URL      string `json:"url,omitempty"`      // supports [IP][COUNTRY][CITY][LANG][KEY]
	Regexp   string `json:"regexp,omitempty"`   // /.../ — take the first group; otherwise the whole response
	Reserved string `json:"reserved,omitempty"` // fallback on error/empty response
	Cache    int    `json:"cache,omitempty"`    // cache seconds
}

// APIMac is a mac code returned in an api-type response with probability Prob %.
// Empty fields are omitted from JSON.
type APIMac struct {
	Enabled bool   `json:"enabled,omitempty"`
	Code    string `json:"code,omitempty"`
	Prob    int    `json:"prob,omitempty"`
}

// Bots holds settings for bot detection and serving them separately. Detection
// runs AFTER stream selection, based on its toggles.
// False toggles and empty strings are omitted from JSON.
type Bots struct {
	CheckUA   bool `json:"ch_ua,omitempty"`         // check UA/referer signatures
	EmptyUA   bool `json:"ch_empty_ua,omitempty"`   // empty UA = bot
	EmptyRef  bool `json:"ch_empty_ref,omitempty"`  // empty referer = bot
	EmptyLang bool `json:"ch_empty_lang,omitempty"` // empty language = bot
	IPv6      bool `json:"ch_ipv6,omitempty"`       // IPv6 = bot
	PTR       bool `json:"ch_ptr,omitempty"`        // reverse DNS
	ListUA    bool `json:"ch_list_ua,omitempty"`    // ua_blacklist.dat
	Proxy     bool `json:"ch_proxy,omitempty"`      // visitor from a proxy, VPN or hosting network — ip_proxy list or proxy_org.dat
	NonMobile bool `json:"ch_non_mobile,omitempty"` // device type is neither phone nor tablet
	IPBaidu   bool `json:"ch_bot_ip_baidu,omitempty"`
	IPBing    bool `json:"ch_bot_ip_bing,omitempty"`
	IPGoogle  bool `json:"ch_bot_ip_google,omitempty"`
	IPMail    bool `json:"ch_bot_ip_mail,omitempty"`
	IPYahoo   bool `json:"ch_bot_ip_yahoo,omitempty"`
	IPYandex  bool `json:"ch_bot_ip_yandex,omitempty"`
	IPOthers  bool `json:"ch_bot_ip_others,omitempty"`
	SaveIP    bool `json:"save_ip,omitempty"` // append bot IP to ip_<se>.dat

	Redirect string `json:"redirect,omitempty"` // bot_redirect; "skip" = normal stream
	Out      string `json:"out,omitempty"`      // out_bot
	Header   string `json:"header,omitempty"`   // b_header (Content-Type for bots)
}

// Output holds the stream output parameters (used at the render phase).
// Empty strings, a zero chance and empty share slices are omitted from JSON.
type Output struct {
	Redirect     string     `json:"redirect,omitempty"`
	Out          string     `json:"out,omitempty"`
	Chance       int        `json:"chance,omitempty"`
	Distribution string     `json:"distribution,omitempty"` // random | rotator | evenly (for out with |||)
	Header       string     `json:"header,omitempty"`       // Content-Type override; empty = the group's
	Offers       []ShareRef `json:"offers,omitempty"`
	Landings     []ShareRef `json:"landings,omitempty"`
	// OutShares is the weight of each part of Out split on "|||".
	// An empty slice or a non-positive sum keeps Distribution.
	OutShares []int `json:"out_shares,omitempty"`
}

// Separation substitutes the output by a keyword from a .dat file (format "key;out").
// Empty fields are omitted from JSON.
type Separation struct {
	Enabled bool   `json:"enabled,omitempty"`
	File    string `json:"file,omitempty"`
}

// Flag holds 0/1/2 filter flags. The exact meaning depends on the filter
// (see router), but the general principle is: 2 = filter disabled.
type Flag int

const (
	// FlagOff means the filter is disabled. It is the zero value of Flag, so an unset
	// rules field is safely treated as "disabled".
	FlagOff Flag = iota
	// FlagA means, for list filters: "exclude on match" (blacklist).
	// For devices/operators: "block".
	FlagA
	// FlagB means, for list filters: "allow only on match" (whitelist).
	// For operators: "whitelist".
	FlagB
)

// ListFilter holds a flag + filter values. Raw stores the original string (for
// detecting /regex/ and the contains semantics of lang/country); Values is the same
// string split by comma and trimmed (for element-wise checks).
// Empty Raw and Values are omitted. Flag is written when the filter itself is present.
type ListFilter struct {
	Flag   Flag     `json:"flag"`
	Raw    string   `json:"raw,omitempty"`
	Values []string `json:"values,omitempty"`
}

// MaxRuleSubID is the highest sub id a stream rule can filter on.
const MaxRuleSubID = 12

// SubIDFilter is one sub id rule. Flag is FlagA (exclude on match) or FlagB
// (only on match); any other flag disables it. Values are compared exactly,
// ignoring case. Empty makes an absent or blank sub id part of the matching set.
// The rule is applied when Values is not empty or Empty is set.
type SubIDFilter struct {
	Flag   Flag     `json:"flag"`
	Values []string `json:"values,omitempty"`
	Empty  bool     `json:"empty,omitempty"`
}

// Active reports whether the rule filters anything: the flag is FlagA or FlagB
// and there is a value or the empty marker.
func (f SubIDFilter) Active() bool {
	return (f.Flag == FlagA || f.Flag == FlagB) && (len(f.Values) > 0 || f.Empty)
}

// SubIDRuleKey returns the Rules.SubIDs key of sub id n, "sub_id_<n>".
// n outside 1..MaxRuleSubID gives a key no rule uses.
func SubIDRuleKey(n int) string {
	return "sub_id_" + strconv.Itoa(n)
}

// IPListFilter filters by an IP list (.dat in DataDir).
type IPListFilter struct {
	Flag Flag   `json:"flag"`
	File string `json:"file"` // file name, not a user-supplied path
}

// Schedule is the stream's working schedule by day of week.
// Days is indexed by time.Weekday: [0]=Sunday … [6]=Saturday.
// When Enabled=true the stream is active only on the marked days.
type Schedule struct {
	Enabled bool    `json:"enabled"`
	Days    [7]bool `json:"days"`
}

// LimitRule is a stream impression limit (the check is delegated to Limiter at phase 4).
type LimitRule struct {
	Enabled bool `json:"enabled"`
	Type    int  `json:"type"`    // 1 = per day, 2 = per period (Seconds)
	Count   int  `json:"count"`   // threshold
	Seconds int  `json:"seconds"` // window for Type==2
}

// Rules is the full set of stream filtering predicates.
// A zero list filter, schedule, IP list or limit is omitted from JSON.
type Rules struct {
	Lang    ListFilter `json:"lang,omitzero"`    // contains semantics
	Country ListFilter `json:"country,omitzero"` // contains semantics
	City    ListFilter `json:"city,omitzero"`    // element-wise exact match
	Region  ListFilter `json:"region,omitzero"`  // element-wise exact match
	UAText  ListFilter `json:"ua_text,omitzero"` // /regex/ or substring
	Referer ListFilter `json:"referer_text,omitzero"`
	Domain  ListFilter `json:"domain_text,omitzero"` // /regex/ or exact match
	Key     ListFilter `json:"key_text,omitzero"`    // /regex/ or substring

	OS      ListFilter `json:"os,omitzero"`      // by "name version", substring (Android, Android 13, iOS 16)
	Browser ListFilter `json:"browser,omitzero"` // by "name version", substring (Chrome, Safari 16)
	Brand   ListFilter `json:"brand,omitzero"`   // by device brand, exact (Apple, Samsung, Xiaomi)
	Model   ListFilter `json:"model,omitzero"`   // by device model, exact (SM-S918B, Pixel 8)

	// By what the IP says about the network and the clock (needs the geo databases).
	ASN      ListFilter `json:"asn,omitzero"`      // AS number, exact ("15169" or "AS15169")
	Org      ListFilter `json:"org,omitzero"`      // AS organization, /regex/ or substring
	Timezone ListFilter `json:"timezone,omitzero"` // UTC offset ("+3", "+5:30") or IANA name

	// By a query-string variable: "name" (present) or "name=value".
	Query ListFilter `json:"get,omitzero"`

	// SubIDs filters by the traffic source's sub ids: the key is sub_id_1
	// through sub_id_12. A key is checked against the value the campaign's
	// traffic source maps to that sub id, not against a fixed query name.
	SubIDs map[string]SubIDFilter `json:"sub_ids,omitempty"`

	Schedule Schedule `json:"schedule,omitzero"` // stream working schedule by day of week

	IPList IPListFilter `json:"ip_list,omitzero"`

	// Devices: FlagA(0) = block this type; otherwise allow.
	Computer Flag `json:"computer,omitempty"`
	Phone    Flag `json:"phone,omitempty"`
	Tablet   Flag `json:"tablet,omitempty"`

	// Operators (beeline,megafon,mts,tele2,azerbaijan,belarus,kazakhstan,
	// ukraine,wap-1,wap-2,wap-3): FlagA(0) = block this operator;
	// FlagB(1) = whitelist (if at least one is set — allow only those).
	Operators map[string]Flag `json:"operators,omitempty"`

	Unique     Flag `json:"unique,omitempty"`      // 0=unique only,1=non-unique only,2=any
	YaBrowser  Flag `json:"yabrowser,omitempty"`   // 0=exclude YaBrowser,1=only it,2=any
	HasReferer Flag `json:"has_referer,omitempty"` // 0=require referer,1=require no referer,2=any
	// Bot is the visitor class from the installation profile.
	// FlagA keeps bots, FlagB keeps everyone else, FlagOff does not filter.
	Bot Flag `json:"bot,omitempty"`
	// Proxy is whether the visitor comes from a proxy, VPN, or hosting network.
	// FlagA keeps proxies, FlagB keeps everyone else, FlagOff does not filter.
	Proxy Flag `json:"proxy,omitempty"`
	// IPVersion is the visitor address family.
	// FlagA keeps IPv4, FlagB keeps IPv6, FlagOff does not filter.
	IPVersion Flag `json:"ip_version,omitempty"`

	Limit LimitRule `json:"limit,omitzero"`
}
