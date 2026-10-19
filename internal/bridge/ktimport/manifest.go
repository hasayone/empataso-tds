package ktimport

import (
	"sort"
	"strconv"
	"strings"
)

// Manifest — список записей, которые записал перенос.
type Manifest struct {
	Host      string            `json:"host,omitempty"`
	Groups    []string          `json:"groups,omitempty"`
	Campaigns map[string]string `json:"campaigns,omitempty"`
	Offers    []string          `json:"offers,omitempty"`
	Landings  []string          `json:"landings,omitempty"`
	Networks  []string          `json:"networks,omitempty"`
	Sources   []string          `json:"sources,omitempty"`
	Domains   []string          `json:"domains,omitempty"`
	Lists     []string          `json:"lists,omitempty"`
	Pages     []string          `json:"pages,omitempty"`
	Users     []string          `json:"users,omitempty"`
}

const (
	KindGroups   = "groups"
	KindOffers   = "offers"
	KindLandings = "landings"
	KindNetworks = "networks"
	KindSources  = "sources"
	KindDomains  = "domains"
	KindLists    = "lists"
	KindPages    = "pages"
	KindUsers    = "users"
)

// Add дописывает id в список вида, без повторов и без пустых строк.
//
// Параметры: kind — один из Kind*; ids — идентификаторы или имена файлов.
// Результат: новые id в конце, прежний порядок сохраняется. Неизвестный kind и nil ничего не меняют.
// Ошибки: нет.
func (m *Manifest) Add(kind string, ids ...string) {
	dst := m.slice(kind)
	if dst == nil {
		return
	}
	seen := map[string]bool{}
	for _, id := range *dst {
		seen[id] = true
	}
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		*dst = append(*dst, id)
	}
}

// Has сообщает, что id уже записан в этом виде.
//
// Параметры: kind — вид списка; id — идентификатор.
// Результат: true, если id есть в списке.
// Ошибки: нет.
func (m Manifest) Has(kind, id string) bool {
	for _, item := range m.list(kind) {
		if item == id {
			return true
		}
	}
	return false
}

// Imported возвращает проверку «id был в этом переносе».
//
// Параметры: kind — вид списка.
// Результат: функция над снимком id на момент вызова. Поздний Add её не меняет.
// Ошибки: нет.
func (m Manifest) Imported(kind string) func(id string) bool {
	set := map[string]bool{}
	for _, id := range m.list(kind) {
		if id != "" {
			set[id] = true
		}
	}
	return func(id string) bool { return set[id] }
}

// Empty сообщает, что манифест не содержит хост и ни одного id.
//
// Параметры: нет.
// Результат: true для нулевого значения и для манифеста без записей.
// Ошибки: нет.
func (m Manifest) Empty() bool {
	return m.Host == "" &&
		len(m.Groups) == 0 && len(m.Campaigns) == 0 && len(m.Offers) == 0 && len(m.Landings) == 0 &&
		len(m.Networks) == 0 && len(m.Sources) == 0 && len(m.Domains) == 0 &&
		len(m.Lists) == 0 && len(m.Pages) == 0 && len(m.Users) == 0
}

// Merge объединяет два манифеста.
//
// Параметры: other — второй список. Получатель не меняется.
// Результат: объединение без повторов, сначала записи получателя. Host берётся из other, если он задан, иначе остаётся прежний.
// Ошибки: нет.
func (m Manifest) Merge(other Manifest) Manifest {
	var out Manifest
	out.Host = m.Host
	if other.Host != "" {
		out.Host = other.Host
	}
	for _, kind := range []string{
		KindGroups, KindOffers, KindLandings, KindNetworks,
		KindSources, KindDomains, KindLists, KindPages, KindUsers,
	} {
		out.Add(kind, m.list(kind)...)
		out.Add(kind, other.list(kind)...)
	}
	out.Campaigns = mergeCampaignMaps(m.Campaigns, other.Campaigns)
	return out
}

// mergeCampaignMaps объединяет две карты кампаний.
//
// Параметры: base — уже записанные пары; newer — пары текущего прогона.
// Результат: копия base с наложением newer; при том же campaign_id побеждает newer.
// Каждый id группы встречается не больше одного раза: новая пара снимает старые campaign_id с тем же gid.
// Ошибки: нет.
func mergeCampaignMaps(base, newer map[string]string) map[string]string {
	if len(base) == 0 && len(newer) == 0 {
		return nil
	}
	out := map[string]string{}
	for k, v := range base {
		if k != "" && v != "" {
			out[k] = v
		}
	}
	for cid, gid := range newer {
		if cid == "" || gid == "" {
			continue
		}
		for k, v := range out {
			if v == gid {
				delete(out, k)
			}
		}
		out[cid] = gid
	}
	return out
}

// AddCampaignsIfAbsent дописывает пары campaign_id → group_id без замены существующих.
//
// Параметры: add — новые пары Keitaro campaign_id → id группы kuztds.
// Результат: получатель дополнен только отсутствующими campaign_id, если gid ещё не занят другим campaign_id.
// Ошибки: нет.
func (m *Manifest) AddCampaignsIfAbsent(add map[string]string) {
	if m == nil || len(add) == 0 {
		return
	}
	if m.Campaigns == nil {
		m.Campaigns = map[string]string{}
	}
	for cid, gid := range add {
		if cid == "" || gid == "" {
			continue
		}
		if _, ok := m.Campaigns[cid]; ok {
			continue
		}
		if campaignGIDTaken(m.Campaigns, gid) {
			continue
		}
		m.Campaigns[cid] = gid
	}
}

// campaignGIDTaken сообщает, что gid уже привязан к какому-то campaign_id.
//
// Параметры: campaigns — карта манифеста; gid — id группы kuztds.
// Результат: true, если другой campaign_id уже указывает на этот gid.
// Ошибки: нет.
func campaignGIDTaken(campaigns map[string]string, gid string) bool {
	for _, v := range campaigns {
		if v == gid {
			return true
		}
	}
	return false
}

// LogCampaignAliases переводит карту манифеста в id кампании → id группы.
//
// Параметры: m — манифест с Campaigns.
// Результат: ключ — числовой id кампании Keitaro. Пары с нечисловым campaign_id пропускаются.
// Если один gid указан несколькими campaign_id, такие пары не попадают в результат.
// Ошибки: нет.
func (m Manifest) LogCampaignAliases() map[int]string {
	if len(m.Campaigns) == 0 {
		return nil
	}
	gidUses := map[string]int{}
	for _, gid := range m.Campaigns {
		gid = strings.TrimSpace(gid)
		if gid != "" {
			gidUses[gid]++
		}
	}
	out := make(map[int]string, len(m.Campaigns))
	for cid, gid := range m.Campaigns {
		gid = strings.TrimSpace(gid)
		n, err := strconv.Atoi(strings.TrimSpace(cid))
		if err != nil || n <= 0 || gid == "" || gidUses[gid] > 1 {
			continue
		}
		out[n] = gid
	}
	return out
}

// slice возвращает поле списка по виду.
//
// Параметры: kind — вид списка.
// Результат: указатель на срез или nil, если kind неизвестен либо получатель nil.
// Ошибки: нет.
func (m *Manifest) slice(kind string) *[]string {
	if m == nil {
		return nil
	}
	switch kind {
	case KindGroups:
		return &m.Groups
	case KindOffers:
		return &m.Offers
	case KindLandings:
		return &m.Landings
	case KindNetworks:
		return &m.Networks
	case KindSources:
		return &m.Sources
	case KindDomains:
		return &m.Domains
	case KindLists:
		return &m.Lists
	case KindPages:
		return &m.Pages
	case KindUsers:
		return &m.Users
	default:
		return nil
	}
}

// list возвращает id одного вида.
//
// Параметры: kind — вид списка.
// Результат: срез id или nil.
// Ошибки: нет.
func (m Manifest) list(kind string) []string {
	if p := (&m).slice(kind); p != nil {
		return *p
	}
	return nil
}

// attachManifest заполняет манифест id и именами файлов, которые есть в результате.
//
// Параметры: res — результат маппера.
// Результат: тот же результат с Manifest. Имена файлов идут по алфавиту.
// Ошибки: нет.
func attachManifest(res Result) Result {
	var m Manifest
	for _, item := range res.Offers {
		m.Add(KindOffers, item.ID)
	}
	for _, item := range res.Landings {
		m.Add(KindLandings, item.ID)
	}
	for _, item := range res.Networks {
		m.Add(KindNetworks, item.ID)
	}
	for _, item := range res.Sources {
		m.Add(KindSources, item.ID)
	}
	for _, item := range res.Domains {
		m.Add(KindDomains, item.ID)
	}
	for _, name := range sortedKeys(res.Lists) {
		m.Add(KindLists, name)
	}
	for _, name := range sortedKeys(res.Pages) {
		m.Add(KindPages, name)
	}
	for _, u := range res.Users {
		m.Add(KindUsers, u.ID)
	}
	res.Manifest = m
	return res
}

// sortedKeys возвращает ключи карты по алфавиту.
//
// Параметры: m — карта имя → текст.
// Результат: ключи. Пустая карта даёт nil.
// Ошибки: нет.
func sortedKeys(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// LocalLandingIDs возвращает числовые id локальных лендингов.
//
// Параметры: нет. Берёт Landings результата.
// Результат: id Keitaro в порядке каталога. Нечисловой id пропускается. Пустой список — nil.
// Ошибки: нет.
func (r Result) LocalLandingIDs() []int {
	var out []int
	for _, row := range r.Landings {
		if !row.Local {
			continue
		}
		n, err := strconv.Atoi(row.ID)
		if err != nil || n <= 0 {
			continue
		}
		out = append(out, n)
	}
	return out
}
