package store

import (
	"context"
	"fmt"
	"strings"
)

// CatalogRefs lists distinct catalog ids referenced by stored rows of one host.
// Offers, Landings, Networks and Sources hold non-empty ids from the offer_id,
// landing_id, network_id and source_id columns of events and conversions.
type CatalogRefs struct {
	Offers   []string
	Landings []string
	Networks []string
	Sources  []string
}

// CatalogRefs reads the distinct catalog ids that events and conversions of one host refer to.
// ctx bounds the two queries, which run under the analytics limits. domain is matched against the domain column; a blank domain
// returns empty lists without a query. It returns the ids from both tables merged, without
// duplicates and empty strings. An error comes from a nil client or a failed query.
func (c *CH) CatalogRefs(ctx context.Context, domain string) (CatalogRefs, error) {
	var out CatalogRefs
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return out, nil
	}
	if c == nil || c.conn == nil {
		return out, fmt.Errorf("clickhouse: nil client")
	}
	seen := [4]map[string]bool{{}, {}, {}, {}}
	dst := [4]*[]string{&out.Offers, &out.Landings, &out.Networks, &out.Sources}
	for _, table := range []string{"events", "conversions"} {
		var cols [4][]string
		q := "SELECT groupUniqArray(toString(offer_id)), groupUniqArray(toString(landing_id)), " +
			"groupUniqArray(toString(network_id)), groupUniqArray(toString(source_id)) FROM " + table + " WHERE domain = ?"
		if err := c.conn.QueryRow(c.analyticsCtx(ctx, 0), q, domain).Scan(&cols[0], &cols[1], &cols[2], &cols[3]); err != nil {
			return CatalogRefs{}, fmt.Errorf("clickhouse: catalog refs %s: %w", table, err)
		}
		for i, ids := range cols {
			for _, id := range ids {
				id = strings.TrimSpace(id)
				if id == "" || seen[i][id] {
					continue
				}
				seen[i][id] = true
				*dst[i] = append(*dst[i], id)
			}
		}
	}
	return out, nil
}
