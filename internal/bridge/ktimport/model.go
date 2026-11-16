package ktimport

import (
	"bytes"
	"encoding/json"
)

// Snapshot — снимок конфигурации, который понимает маппер.
// Поля совпадают с фикстурой и с ответами GET Admin API.
type Snapshot struct {
	Campaigns         []Campaign           `json:"campaigns"`
	Streams           map[string][]KStream `json:"streams"`
	Offers            []Offer              `json:"offers"`
	Landings          []Landing            `json:"landings"`
	TrafficSources    []KSource            `json:"traffic_sources"`
	AffiliateNetworks []KNetwork           `json:"affiliate_networks"`
	Domains           []KDomain            `json:"domains"`
	Groups            []KGroup             `json:"groups"`
	Users             []KUser              `json:"users"`
	UsersDenied       bool                 `json:"-"`
}

// Campaign — кампания Keitaro, которая становится группой.
// CookiesTTL в часах, кроме 0 и 24, становится UniqSeconds.
// BindVisitors, CostAuto и сырые ключи уникальности читаются для отчёта и в UniqMethod не копируются.
type Campaign struct {
	ID               FlexInt         `json:"id"`
	Alias            string          `json:"alias"`
	Name             string          `json:"name"`
	State            string          `json:"state"`
	Type             string          `json:"type"`
	GroupID          FlexInt         `json:"group_id"`
	DomainID         FlexInt         `json:"domain_id"`
	TrafficSourceID  FlexInt         `json:"traffic_source_id"`
	CostType         string          `json:"cost_type"`
	CostValue        FlexFloat       `json:"cost_value"`
	CostCurrency     string          `json:"cost_currency"`
	CookiesTTL       FlexInt         `json:"cookies_ttl,omitempty"`
	BindVisitors     string          `json:"bind_visitors,omitempty"`
	CostAuto         FlexBool        `json:"cost_auto,omitempty"`
	Uniqueness       json.RawMessage `json:"uniqueness,omitempty"`
	UniquenessParam  json.RawMessage `json:"uniqueness_param,omitempty"`
	UniquenessMethod json.RawMessage `json:"uniqueness_method,omitempty"`
	Parameters       json.RawMessage `json:"parameters"`
	Postbacks        json.RawMessage `json:"postbacks"`
}

// KStream — поток Keitaro.
type KStream struct {
	ID            FlexInt        `json:"id"`
	Type          string         `json:"type"`
	Name          string         `json:"name"`
	Position      FlexInt        `json:"position"`
	Weight        FlexInt        `json:"weight"`
	State         string         `json:"state"`
	Schema        string         `json:"schema"`
	ActionType    string         `json:"action_type"`
	ActionPayload string         `json:"action_payload"`
	FilterOr      bool           `json:"filter_or"`
	Filters       []KFilter      `json:"filters"`
	Landings      []KStreamRef   `json:"landings"`
	Offers        []KStreamOffer `json:"offers"`
}

// KFilter — одно условие потока.
type KFilter struct {
	Name    string          `json:"name"`
	Mode    string          `json:"mode"`
	Payload json.RawMessage `json:"payload"`
}

// KStreamRef — ссылка потока на лендинг и его доля.
type KStreamRef struct {
	LandingID FlexInt `json:"landing_id"`
	Share     FlexInt `json:"share"`
	State     string  `json:"state"`
}

// KStreamOffer — ссылка потока на оффер и его доля.
type KStreamOffer struct {
	OfferID FlexInt `json:"offer_id"`
	Share   FlexInt `json:"share"`
	State   string  `json:"state"`
}

// Offer — оффер Keitaro. Локальный файл не скачивается.
type Offer struct {
	ID                 FlexInt     `json:"id"`
	Name               string      `json:"name"`
	GroupID            FlexInt     `json:"group_id"`
	AffiliateNetworkID FlexInt     `json:"affiliate_network_id"`
	ActionType         string      `json:"action_type"`
	ActionPayload      string      `json:"action_payload"`
	LocalPath          string      `json:"local_path"`
	OfferType          string      `json:"offer_type"`
	Country            FlexStrings `json:"country"`
	PayoutType         string      `json:"payout_type"`
	PayoutValue        FlexFloat   `json:"payout_value"`
	PayoutCurrency     string      `json:"payout_currency"`
	PayoutAuto         FlexBool    `json:"payout_auto"`
	State              string      `json:"state"`
}

// Landing — лендинг Keitaro. Локальный PHP не скачивается.
type Landing struct {
	ID            FlexInt `json:"id"`
	Name          string  `json:"name"`
	GroupID       FlexInt `json:"group_id"`
	State         string  `json:"state"`
	LandingType   string  `json:"landing_type"`
	ActionType    string  `json:"action_type"`
	ActionPayload string  `json:"action_payload"`
}

// KParam — одно именованное поле источника трафика Keitaro.
type KParam struct {
	Name        string `json:"name"`
	Placeholder string `json:"placeholder"`
	Alias       string `json:"alias"`
}

// KSource — источник трафика Keitaro.
type KSource struct {
	ID          FlexInt           `json:"id"`
	Name        string            `json:"name"`
	Parameters  map[string]KParam `json:"parameters"`
	PostbackURL string            `json:"postback_url"`
	State       string            `json:"state"`
}

// KNetwork — партнёрская сеть Keitaro.
type KNetwork struct {
	ID          FlexInt `json:"id"`
	Name        string  `json:"name"`
	PostbackURL string  `json:"postback_url"`
	OfferParam  string  `json:"offer_param"`
	State       string  `json:"state"`
}

// KDomain — домен Keitaro. Name — это хост.
// DefaultCampaignID — кампания, на которую домен отдает корень. Ноль значит, что корень не назначен.
type KDomain struct {
	ID                FlexInt           `json:"id"`
	Name              string            `json:"name"`
	State             string            `json:"state"`
	DefaultCampaignID FlexInt           `json:"default_campaign_id"`
	GroupID           FlexInt           `json:"group_id"`
	Group             domainInlineGroup `json:"group"`
}

// domainInlineGroup — имя группы домена из поля group ответа Keitaro.
type domainInlineGroup string

// UnmarshalJSON читает имя группы из строки, null или объекта с полем name.
//
// Параметры: data — JSON-значение поля group.
// Результат: имя группы или пустая строка для null и неизвестной формы.
// Ошибки: нет; разбор не прерывает unmarshaling домена.
func (g *domainInlineGroup) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || string(data) == "null" {
		*g = ""
		return nil
	}
	if data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			*g = ""
			return nil
		}
		*g = domainInlineGroup(s)
		return nil
	}
	if data[0] == '{' {
		var obj struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(data, &obj); err != nil {
			*g = ""
			return nil
		}
		*g = domainInlineGroup(obj.Name)
		return nil
	}
	*g = ""
	return nil
}

// KGroup — группа сущностей Keitaro: кампании, офферы, лендинги или домены.
type KGroup struct {
	ID   FlexInt `json:"id"`
	Name string  `json:"name"`
	Type string  `json:"type"`
}

// KUser — пользователь Keitaro из GET /admin_api/v1/users.
type KUser struct {
	ID          FlexInt         `json:"id"`
	Login       string          `json:"login"`
	Name        string          `json:"name"`
	Type        string          `json:"type"`
	AccessData  json.RawMessage `json:"access_data"`
	Preferences KUserPrefs      `json:"preferences"`
}

// KUserPrefs — язык и часовой пояс пользователя Keitaro.
type KUserPrefs struct {
	Language string `json:"language"`
	Timezone string `json:"timezone"`
}

// CatUser — пользователь emtds после маппинга прав Keitaro.
type CatUser struct {
	ID       string              `json:"id"`
	Login    string              `json:"login"`
	Type     string              `json:"type"`
	Sections []string            `json:"sections,omitempty"`
	Grants   map[string]CatGrant `json:"grants,omitempty"`
	Hidden   []string            `json:"hidden,omitempty"`
	Lang     string              `json:"lang,omitempty"`
	TZ       string              `json:"tz,omitempty"`
}

// CatGrant — режим доступа к ресурсу каталога или кампаний.
type CatGrant struct {
	Mode   string   `json:"mode"`
	IDs    []string `json:"ids,omitempty"`
	Labels []string `json:"labels,omitempty"`
}

// Group — группа emtds, которую принимает PUT /api/groups.
// Num — номер в списке кампаний. Маппер пишет id кампании Keitaro. Ноль значит, что слияние ещё не выдало номер.
// PostbackIDs — id общих шаблонов каталога s2s. Тот же шаблон в Postbacks не дублируется.
type Group struct {
	ID       string   `json:"id"`
	Aliases  []string `json:"aliases,omitempty"`
	Name     string   `json:"name"`
	Status   bool     `json:"status"`
	Redirect string   `json:"redirect"`
	Out      string   `json:"out"`
	Folder   string   `json:"folder,omitempty"`
	DomainID string   `json:"domain_id,omitempty"`
	SourceID string   `json:"source_id,omitempty"`
	Weight   bool     `json:"weight,omitempty"`
	// UniqSeconds — окно уникальности в секундах. Ноль оставляет окно движка в 24 часа.
	UniqSeconds int        `json:"uniq_seconds,omitempty"`
	Num         int        `json:"num,omitempty"`
	Postbacks   []Postback `json:"postbacks,omitempty"`
	PostbackIDs []string   `json:"postback_ids,omitempty"`
	Streams     []Stream   `json:"streams"`
}

// Postback — исходящий шаблон конверсии кампании.
// ID и Name заполнены у записи общего каталога. У шаблона внутри кампании они пустые.
type Postback struct {
	ID       string   `json:"id,omitempty"`
	Name     string   `json:"name,omitempty"`
	URL      string   `json:"url"`
	Method   string   `json:"method"`
	Statuses []string `json:"statuses,omitempty"`
}

// Stream — поток emtds. Name — имя потока Keitaro, пустое становится stream и id. Comment хранит keitaro_stream_id.
type Stream struct {
	Name     string `json:"name"`
	Status   bool   `json:"status"`
	Comment  string `json:"comment,omitempty"`
	Rules    Rules  `json:"rules"`
	FilterOr bool   `json:"filter_or,omitempty"`
	Weight   int    `json:"weight,omitempty"`
	Out      Output `json:"out"`
}

// Rules — фильтры потока.
// Для списков флаг 1 исключает совпадение, флаг 2 оставляет только совпадение.
// Brand — бренд устройства, точное совпадение. Model — модель устройства, точное совпадение.
// Unique: 0 — любой визит и в JSON не пишется, 1 — только уникальные, 2 — только повторные.
// Bot: 0 — фильтр выключен, 1 — оставить ботов, 2 — оставить людей.
// HasReferer: 0 — фильтр выключен, 1 — только с Referer, 2 — только без Referer.
// Proxy: 0 — фильтр выключен и в JSON не пишется, 1 — только прокси, 2 — только не прокси.
// IPVersion: 0 — фильтр выключен и в JSON не пишется, 1 — только IPv4, 2 — только IPv6.
type Rules struct {
	Country    ListFilter   `json:"country,omitempty"`
	City       ListFilter   `json:"city,omitempty"`
	Region     ListFilter   `json:"region,omitempty"`
	Lang       ListFilter   `json:"lang,omitempty"`
	UAText     ListFilter   `json:"ua_text,omitempty"`
	Referer    ListFilter   `json:"referer_text,omitempty"`
	Key        ListFilter   `json:"key_text,omitempty"`
	OS         ListFilter   `json:"os,omitempty"`
	Browser    ListFilter   `json:"browser,omitempty"`
	Brand      ListFilter   `json:"brand,omitempty"`
	Model      ListFilter   `json:"model,omitempty"`
	Query      ListFilter   `json:"get,omitempty"`
	Timezone   ListFilter   `json:"timezone,omitempty"`
	ASN        ListFilter   `json:"asn,omitempty"`
	Org        ListFilter   `json:"org,omitempty"`
	IPList     IPListFilter `json:"ip_list,omitempty"`
	Schedule   Schedule     `json:"schedule,omitempty"`
	Computer   int          `json:"computer,omitempty"`
	Phone      int          `json:"phone,omitempty"`
	Tablet     int          `json:"tablet,omitempty"`
	Unique     int          `json:"unique,omitempty"`
	Bot        int          `json:"bot,omitempty"`
	HasReferer int          `json:"has_referer,omitempty"`
	Proxy      int          `json:"proxy,omitempty"`
	IPVersion  int          `json:"ip_version,omitempty"`

	// SubIDs — правила Sub ID источника по ключам sub_id_1..sub_id_12, точное совпадение.
	SubIDs map[string]SubIDFilter `json:"sub_ids,omitempty"`
}

// ListFilter — флаг и значения списка. 1 — исключать совпадение, 2 — только совпадение.
type ListFilter struct {
	Flag   int      `json:"flag,omitempty"`
	Raw    string   `json:"raw,omitempty"`
	Values []string `json:"values,omitempty"`
}

// SubIDFilter — правило одного Sub ID источника, зеркало config.SubIDFilter.
// Flag 1 исключает совпадение, Flag 2 оставляет только совпадение. Empty означает пустое значение.
type SubIDFilter struct {
	Flag   int      `json:"flag"`
	Values []string `json:"values,omitempty"`
	Empty  bool     `json:"empty,omitempty"`
}

// IPListFilter — файл списка IP в каталоге данных движка.
// Flag 1 исключает совпадение, Flag 2 оставляет только совпадение.
type IPListFilter struct {
	Flag int    `json:"flag"`
	File string `json:"file"`
}

// Schedule — дни работы потока. Индекс 0 — воскресенье.
type Schedule struct {
	Enabled bool    `json:"enabled"`
	Days    [7]bool `json:"days"`
}

// Output — выход потока. Chance 100 — поток отдаёт цель всегда.
type Output struct {
	Redirect     string     `json:"redirect"`
	Out          string     `json:"out"`
	Chance       int        `json:"chance"`
	Distribution string     `json:"distribution,omitempty"`
	Offers       []ShareRef `json:"offers,omitempty"`
	Landings     []ShareRef `json:"landings,omitempty"`
}

// ShareRef — оффер или лендинг потока и его доля.
type ShareRef struct {
	ID    string `json:"id"`
	Share int    `json:"share"`
}

// CatOffer — оффер каталога emtds.
type CatOffer struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	URL            string   `json:"url"`
	Status         bool     `json:"status"`
	NetworkID      string   `json:"network_id,omitempty"`
	Group          string   `json:"group,omitempty"`
	Countries      []string `json:"countries,omitempty"`
	PayoutModel    string   `json:"payout_model,omitempty"`
	Payout         float64  `json:"payout,omitempty"`
	PayoutCurrency string   `json:"payout_currency,omitempty"`
	PayoutParam    bool     `json:"payout_param,omitempty"`
	Local          bool     `json:"local,omitempty"`
}

// CatLanding — лендинг каталога emtds.
// Entry — относительный путь входного файла локального лендинга. Импорт оставляет его пустым.
type CatLanding struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	URL    string `json:"url"`
	Status bool   `json:"status"`
	Local  bool   `json:"local,omitempty"`
	Entry  string `json:"entry,omitempty"`
	Group  string `json:"group,omitempty"`
}

// CatNetwork — партнёрская сеть каталога emtds.
type CatNetwork struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Status      bool   `json:"status"`
	PostbackURL string `json:"postback_url"`
	OfferParam  string `json:"offer_param,omitempty"`
	OfferQuery  string `json:"offer_query,omitempty"`
}

// CatSource — источник трафика каталога emtds.
// Parameters принимает только keyword и sub_id_1..sub_id_15.
type CatSource struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Parameters map[string]string `json:"parameters,omitempty"`
}

// CatDomain — припаркованный хост. Role для импорта всегда links.
type CatDomain struct {
	ID    string `json:"id"`
	Host  string `json:"host"`
	Role  string `json:"role"`
	Group string `json:"group,omitempty"`
}

// Loss — строка отчёта о потере, выключении или приближении.
type Loss struct {
	Kind   string `json:"kind"`
	Ref    string `json:"ref"`
	Detail string `json:"detail"`
}

// Result — каталоги, группы и отчёт одного прогона.
// Pages — имя файла HTML → текст. В out потока пишется @page: и это имя.
// Manifest — id и имена файлов, которые записал этот прогон.
type Result struct {
	Groups   []Group           `json:"groups"`
	Offers   []CatOffer        `json:"offers"`
	Landings []CatLanding      `json:"landings"`
	Networks []CatNetwork      `json:"networks"`
	Sources  []CatSource       `json:"sources"`
	Domains  []CatDomain       `json:"domains"`
	Lists    map[string]string `json:"lists,omitempty"`
	Pages    map[string]string `json:"pages,omitempty"`
	Users    []CatUser         `json:"users,omitempty"`
	Manifest Manifest          `json:"manifest"`
	Losses   []Loss            `json:"losses"`
}

// Selection решает, какие каталоги и кампании попадают в результат.
// Ссылка из кампании на id всё равно строится, даже если каталог выключен.
type Selection struct {
	Domains   bool
	Networks  bool
	Offers    bool
	Landings  bool
	Sources   bool
	Campaigns bool
	Users     bool
	// Known сообщает, что id каталога уже переносился раньше (из prev-манифеста).
	Known func(kind, id string) bool
	// Campaign — id кампании Keitaro для досинка одной кампании; 0 переносит все кампании.
	Campaign int
}

// MaxBody — предел тела PUT /api/groups в emtds.
const MaxBody = 8 << 20

// MaxHops — предел цепочки group → group в emtds.
const MaxHops = 3
