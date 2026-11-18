package config

import "slices"

// minSharedListBytes is the smallest list filter, in bytes of Raw plus Values, that ShareListFilters and
// HeavyListFilter treat as worth sharing. Shorter filters cost less than the bookkeeping.
const minSharedListBytes = 1024

// NamedListFilter is one list filter of a stream's rules together with its JSON key in Rules.
type NamedListFilter struct {
	Key    string
	Filter *ListFilter
}

// ListFilterFields returns every ListFilter field of r with its JSON key, in declaration order.
// The pointers address r itself, so a caller may read or replace the filters in place.
func (r *Rules) ListFilterFields() []NamedListFilter {
	return []NamedListFilter{
		{"lang", &r.Lang}, {"country", &r.Country}, {"city", &r.City}, {"region", &r.Region},
		{"ua_text", &r.UAText}, {"referer_text", &r.Referer}, {"domain_text", &r.Domain}, {"key_text", &r.Key},
		{"os", &r.OS}, {"browser", &r.Browser}, {"brand", &r.Brand}, {"model", &r.Model},
		{"asn", &r.ASN}, {"org", &r.Org}, {"timezone", &r.Timezone}, {"get", &r.Query},
	}
}

// HeavyListFilter reports whether f is long enough to be shared: Raw and Values together reach
// minSharedListBytes.
func HeavyListFilter(f ListFilter) bool {
	n := len(f.Raw)
	for _, v := range f.Values {
		n += len(v)
		if n >= minSharedListBytes {
			return true
		}
	}
	return n >= minSharedListBytes
}

// ShareListFilters makes equal long list filters of all streams point at one Raw string and one Values
// slice. Imported campaigns repeat the same list of thousands of values in every stream; after sharing
// the process keeps one copy instead of one per stream. Flags stay per stream.
// Filters are equal when Raw and Values match; shorter ones (see HeavyListFilter) are left alone.
// Callers must treat Values as read-only afterwards: an element written in place would change every
// stream that shares the slice. Replacing the whole filter is safe.
func ShareListFilters(groups []Group) {
	seen := map[string][]ListFilter{}
	for gi := range groups {
		for si := range groups[gi].Streams {
			rules := &groups[gi].Streams[si].Rules
			for _, nf := range rules.ListFilterFields() {
				f := nf.Filter
				if !HeavyListFilter(*f) {
					continue
				}
				shared := false
				for _, c := range seen[f.Raw] {
					if slices.Equal(c.Values, f.Values) {
						f.Raw, f.Values = c.Raw, c.Values
						shared = true
						break
					}
				}
				if !shared {
					seen[f.Raw] = append(seen[f.Raw], *f)
				}
			}
		}
	}
}
