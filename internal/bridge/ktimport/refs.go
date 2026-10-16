package ktimport

import (
	"strings"
)

// offerRefs собирает доли офферов и склеивает ссылки с одним URL.
//
// Параметры: st — поток; offers — справочник; losses — отчёт, куда пишется приближение.
// Результат: список числовых id. Первая ссылка остаётся, доля повтора прибавляется к ней. Пустой список — nil.
// Ошибки: нет. Удалённая ссылка и неположительный id не входят. Пустой URL не склеивается.
func offerRefs(st KStream, offers map[int]Offer, losses *[]Loss) []ShareRef {
	var items []shareItem
	for _, ref := range st.Offers {
		if strings.EqualFold(ref.State, "deleted") || int(ref.OfferID) <= 0 {
			continue
		}
		url := ""
		if o, ok := offers[int(ref.OfferID)]; ok {
			url = o.ActionPayload
		}
		items = append(items, shareItem{id: int(ref.OfferID), share: int(ref.Share), url: url})
	}
	return collapseShares(int(st.ID), "офферы", items, losses)
}

// landingRefs собирает доли лендингов и склеивает ссылки с одним URL.
//
// Параметры: st — поток; landings — справочник; losses — отчёт.
// Результат: список числовых id. Первая ссылка остаётся, доля повтора прибавляется к ней. Пустой список — nil.
// Ошибки: нет. Удалённая ссылка и неположительный id не входят. Пустой URL не склеивается.
func landingRefs(st KStream, landings map[int]Landing, losses *[]Loss) []ShareRef {
	var items []shareItem
	for _, ref := range st.Landings {
		if strings.EqualFold(ref.State, "deleted") || int(ref.LandingID) <= 0 {
			continue
		}
		url := ""
		if l, ok := landings[int(ref.LandingID)]; ok {
			url = l.ActionPayload
		}
		items = append(items, shareItem{id: int(ref.LandingID), share: int(ref.Share), url: url})
	}
	return collapseShares(int(st.ID), "лендинги", items, losses)
}

// shareItem — одна ссылка потока до склейки одинаковых URL.
type shareItem struct {
	id    int
	share int
	url   string
}

// collapseShares оставляет первую ссылку на каждый непустой URL и складывает доли.
//
// Параметры: streamID — id потока Keitaro; label — слово для отчёта; items — ссылки; losses — отчёт.
// Результат: доли kuztds. Повтор пишет одну строку approximated со списком id. Ссылка отчёта — stream и id потока.
// Ошибки: нет.
func collapseShares(streamID int, label string, items []shareItem, losses *[]Loss) []ShareRef {
	var out []ShareRef
	at := map[string]int{}
	var ids []string
	for _, item := range items {
		id := numID(item.id)
		key := strings.TrimSpace(item.url)
		if key != "" {
			if i, ok := at[key]; ok {
				out[i].Share += item.share
				ids = append(ids, out[i].ID, id)
				continue
			}
			at[key] = len(out)
		}
		out = append(out, ShareRef{ID: id, Share: item.share})
	}
	if len(ids) > 0 && losses != nil {
		*losses = append(*losses, Loss{
			Kind:   "approximated",
			Ref:    streamRef(streamID),
			Detail: label + " с одинаковым URL объединены: " + strings.Join(unique(ids), ", "),
		})
	}
	return out
}
