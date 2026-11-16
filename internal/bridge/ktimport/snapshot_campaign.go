package ktimport

import (
	"errors"
	"strconv"
)

// ErrCampaignNotFound — в трекере нет кампании с запрошенным id.
var ErrCampaignNotFound = errors.New("keitaro: кампания не найдена")

// findCampaign ищет кампанию по id Keitaro.
//
// Параметры: camps — кампании снимка; id — id кампании.
// Результат: кампания и true, если она есть.
// Ошибки: нет.
func findCampaign(camps []Campaign, id int) (Campaign, bool) {
	for _, c := range camps {
		if int(c.ID) == id {
			return c, true
		}
	}
	return Campaign{}, false
}

// ForCampaign оставляет в снимке то, что нужно для досинка одной кампании.
//
// Кампании остаются все: по ним собираются alias и id групп так же, как при полном переносе.
// Потоки остаются только у кампании id. Офферы и лендинги — те, на которые ссылаются её потоки,
// сети — те, на которые ссылаются эти офферы, источник и домен — те, что заданы у кампании.
// Группы Keitaro остаются все: из них берутся имена папок. Пользователи убираются.
//
// Параметры: id — id кампании Keitaro.
// Результат: новый снимок; исходный не меняется.
// Ошибки: нет. Неизвестный id даёт снимок без потоков и каталогов.
func (s Snapshot) ForCampaign(id int) Snapshot {
	key := strconv.Itoa(id)
	streams := s.Streams[key]
	out := Snapshot{
		Campaigns: s.Campaigns,
		Streams:   map[string][]KStream{key: streams},
		Groups:    s.Groups,
	}
	offerIDs, landingIDs := map[int]bool{}, map[int]bool{}
	for _, st := range streams {
		for _, ref := range st.Offers {
			offerIDs[int(ref.OfferID)] = true
		}
		for _, ref := range st.Landings {
			landingIDs[int(ref.LandingID)] = true
		}
	}
	netIDs := map[int]bool{}
	for _, o := range s.Offers {
		if offerIDs[int(o.ID)] {
			out.Offers = append(out.Offers, o)
			netIDs[int(o.AffiliateNetworkID)] = true
		}
	}
	for _, l := range s.Landings {
		if landingIDs[int(l.ID)] {
			out.Landings = append(out.Landings, l)
		}
	}
	for _, n := range s.AffiliateNetworks {
		if netIDs[int(n.ID)] {
			out.AffiliateNetworks = append(out.AffiliateNetworks, n)
		}
	}
	camp, ok := findCampaign(s.Campaigns, id)
	if !ok {
		return out
	}
	for _, src := range s.TrafficSources {
		if int(src.ID) > 0 && src.ID == camp.TrafficSourceID {
			out.TrafficSources = append(out.TrafficSources, src)
		}
	}
	for _, d := range s.Domains {
		if int(d.ID) > 0 && d.ID == camp.DomainID {
			out.Domains = append(out.Domains, d)
		}
	}
	return out
}
