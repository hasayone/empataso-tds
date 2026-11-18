package admin

import (
	"slices"

	"github.com/hasayone/empataso-tds/internal/config"
)

// sharedGroupList is the GET /api/groups?shared=1 answer.
// Groups are the campaigns with every long list filter (config.HeavyListFilter) cut down to its flag.
// Lists holds each distinct long filter once. Refs puts them back: each entry is
// [group index, stream index, rules key, Lists index].
type sharedGroupList struct {
	Groups []config.Group `json:"groups"`
	Lists  []sharedList   `json:"lists"`
	Refs   [][4]any       `json:"refs"`
}

// sharedList is the text of one long list filter without its flag.
type sharedList struct {
	Raw    string   `json:"raw,omitempty"`
	Values []string `json:"values,omitempty"`
}

// shareGroupLists builds the shared form of groups. Imported campaigns repeat a list of thousands of
// device models in every stream, which makes the plain answer hundreds of megabytes; here it is sent once.
// groups is not changed: a group with a long filter gets its own copy of Streams.
// It returns empty, non-nil Lists and Refs when no filter is long.
func shareGroupLists(groups []config.Group) sharedGroupList {
	out := sharedGroupList{Groups: make([]config.Group, len(groups)), Lists: []sharedList{}, Refs: [][4]any{}}
	byRaw := map[string][]int{}
	for gi, g := range groups {
		cloned := false
		for si := range g.Streams {
			for k, nf := range g.Streams[si].Rules.ListFilterFields() {
				if !config.HeavyListFilter(*nf.Filter) {
					continue
				}
				if !cloned {
					g.Streams = slices.Clone(g.Streams)
					cloned = true
				}
				f := g.Streams[si].Rules.ListFilterFields()[k].Filter
				out.Refs = append(out.Refs, [4]any{gi, si, nf.Key, sharedListIndex(&out.Lists, byRaw, *f)})
				f.Raw, f.Values = "", nil
			}
		}
		out.Groups[gi] = g
	}
	return out
}

// sharedListIndex returns the index of f's text in lists, appending it when it is new.
// byRaw maps a Raw string to the indexes of lists entries with that Raw.
func sharedListIndex(lists *[]sharedList, byRaw map[string][]int, f config.ListFilter) int {
	for _, i := range byRaw[f.Raw] {
		if slices.Equal((*lists)[i].Values, f.Values) {
			return i
		}
	}
	*lists = append(*lists, sharedList{Raw: f.Raw, Values: f.Values})
	i := len(*lists) - 1
	byRaw[f.Raw] = append(byRaw[f.Raw], i)
	return i
}
