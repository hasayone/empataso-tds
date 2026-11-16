package ktimport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// CatalogIDs — id офферов, лендингов, партнёрских сетей, источников и доменов Keitaro.
type CatalogIDs struct {
	Offers   []int
	Landings []int
	Networks []int
	Sources  []int
	Domains  []int
}

// Count возвращает число id во всех видах.
//
// Параметры: нет.
// Результат: сумма длин пяти списков.
// Ошибки: нет.
func (ids CatalogIDs) Count() int {
	return len(ids.Offers) + len(ids.Landings) + len(ids.Networks) + len(ids.Sources) + len(ids.Domains)
}

// FetchCatalogByID читает записи каталога Keitaro по одной, только GET /{вид}/{id}.
//
// Параметры: ctx — отмена; ids — какие записи читать. Неположительные id пропускаются.
// Результат: снимок с найденными офферами, лендингами, сетями, источниками и доменами; для офферов,
// лендингов и доменов ещё группы type=offers, type=landings и type=domains, чтобы маппер взял имя папки.
// Второй результат — id, на которые трекер ответил 404. Записи, прочитанные до ошибки, остаются в снимке.
// Ошибки: путь вне allowlist, сеть, статус не 200 и не 404, тело не JSON-объект. Чтение останавливается на первой ошибке.
func (c *Client) FetchCatalogByID(ctx context.Context, ids CatalogIDs) (Snapshot, CatalogIDs, error) {
	var snap Snapshot
	var gone CatalogIDs
	var groupKinds []string
	if len(ids.Offers) > 0 || len(ids.Landings) > 0 {
		groupKinds = append(groupKinds, "offers", "landings")
	}
	if len(ids.Domains) > 0 {
		groupKinds = append(groupKinds, "domains")
	}
	for _, kind := range groupKinds {
		var part []KGroup
		if err := c.getOptionalArray(ctx, "/admin_api/v1/groups?type="+kind, &part); err != nil {
			return snap, gone, err
		}
		for i := range part {
			if part[i].Type == "" {
				part[i].Type = kind
			}
		}
		snap.Groups = append(snap.Groups, part...)
	}
	if err := fetchEach(c, ctx, "offers", ids.Offers, &snap.Offers, &gone.Offers); err != nil {
		return snap, gone, err
	}
	if err := fetchEach(c, ctx, "landing_pages", ids.Landings, &snap.Landings, &gone.Landings); err != nil {
		return snap, gone, err
	}
	if err := fetchEach(c, ctx, "affiliate_networks", ids.Networks, &snap.AffiliateNetworks, &gone.Networks); err != nil {
		return snap, gone, err
	}
	if err := fetchEach(c, ctx, "traffic_sources", ids.Sources, &snap.TrafficSources, &gone.Sources); err != nil {
		return snap, gone, err
	}
	if err := fetchEach(c, ctx, "domains", ids.Domains, &snap.Domains, &gone.Domains); err != nil {
		return snap, gone, err
	}
	return snap, gone, nil
}

// fetchEach читает записи одного ресурса по id.
//
// Параметры: c — клиент; ctx — отмена; resource — имя ресурса Admin API; ids — id записей;
// dest — куда дописать разобранные записи; gone — куда дописать id с ответом 404.
// Результат: dest и gone дополнены.
// Ошибки: сеть, статус не 200 и не 404, тело не JSON-объект.
func fetchEach[T any](c *Client, ctx context.Context, resource string, ids []int, dest *[]T, gone *[]int) error {
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		path := fmt.Sprintf("/admin_api/v1/%s/%d", resource, id)
		code, body, err := c.request(ctx, http.MethodGet, path, nil, Allow)
		if err != nil {
			return err
		}
		if code == http.StatusNotFound {
			*gone = append(*gone, id)
			continue
		}
		if code != http.StatusOK {
			return fmt.Errorf("keitaro: GET %s вернул %d", path, code)
		}
		var row T
		body = bytes.TrimSpace(body)
		if len(body) == 0 || body[0] != '{' || json.Unmarshal(body, &row) != nil {
			return fmt.Errorf("keitaro: %s/%d не объект", resource, id)
		}
		*dest = append(*dest, row)
	}
	return nil
}
