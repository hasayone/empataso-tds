package admin

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/hasayone/empataso-tds/internal/config"
)

// aiCampaignFullMax is the most ids of one list_campaigns call that still get whole campaigns with
// their streams; a longer id list, like a call without ids, gets summary rows.
const aiCampaignFullMax = 5

// aiCampaignViewKey is the request context key of the aiCampaignView of an assistant call.
type aiCampaignViewKey struct{}

// aiCampaignView is what the campaign list handler writes for the assistant: a summary row of
// every campaign, and the whole record, streams included, only for the ids in full.
type aiCampaignView struct {
	full map[string]bool
}

// aiCampaignRow is the summary of one campaign: enough to find it and to ask for it by id.
// ID is the slug of the page #/flows/{id}; Num is the number shown in the list.
type aiCampaignRow struct {
	ID            string   `json:"id"`
	Num           int      `json:"num,omitempty"`
	Name          string   `json:"name"`
	Status        bool     `json:"status"`
	Aliases       []string `json:"aliases,omitempty"`
	Folder        string   `json:"folder,omitempty"`
	BoardID       string   `json:"board_id,omitempty"`
	DomainID      string   `json:"domain_id,omitempty"`
	SourceID      string   `json:"source_id,omitempty"`
	StreamsTotal  int      `json:"streams_total"`
	StreamsActive int      `json:"streams_active"`
}

// rows returns one entry per group of groups, in order: the whole group when its id is in full,
// otherwise its aiCampaignRow.
func (v aiCampaignView) rows(groups []config.Group) []any {
	out := make([]any, 0, len(groups))
	for _, g := range groups {
		if v.full[g.ID] {
			out = append(out, g)
			continue
		}
		row := aiCampaignRow{
			ID: g.ID, Num: g.Num, Name: g.Name, Status: g.Status, Aliases: g.Aliases, Folder: g.Folder,
			BoardID: g.BoardID, DomainID: g.DomainID, SourceID: g.SourceID, StreamsTotal: len(g.Streams),
		}
		for _, st := range g.Streams {
			if st.Status {
				row.StreamsActive++
			}
		}
		out = append(out, row)
	}
	return out
}

// campaignView returns the view of a list_campaigns call: whole campaigns for at most
// aiCampaignFullMax asked-for ids, summaries for everything else.
func (c aiCatalogQuery) campaignView() aiCampaignView {
	if c.hasIDs && len(c.ids) > 0 && len(c.ids) <= aiCampaignFullMax {
		return aiCampaignView{full: c.ids}
	}
	return aiCampaignView{}
}

// aiCatalogQuery is the search and the page a catalog list call asks for. The executor applies it
// to the list the handler answered, so the user's own view is what gets searched.
type aiCatalogQuery struct {
	// q is the lower-case search text, empty for no search.
	q string
	// ids is the set of wanted ids; hasIDs tells an empty list from no list.
	ids    map[string]bool
	hasIDs bool
	// limit is the page size, 1..limitMax; offset is the number of matches skipped.
	limit, offset int
	// limitMax is the largest page size of the tool, and resultMax the size in bytes the page may
	// have: rows past it are left for the next page.
	limitMax, resultMax int
}

// aiPageBounds returns the default page size, the largest one and the result size of the catalog
// list tool t. A campaign list gives summaries without streams, which are small, so its page is
// larger than the 50 rows of the other catalogs.
func aiPageBounds(t aiTool) (def, limitMax, resultMax int) {
	if t.Resource == "campaigns" {
		return aiCampaignLimitDefault, aiCampaignLimitMax, aiCampaignResultMax
	}
	return aiCatalogLimitDefault, aiReadLimitMax, aiResultMax
}

// aiParseCatalogQuery reads q, ids, limit and offset of a catalog list call. A tool that is not a
// catalog list gives the zero query. A wrong type, a negative offset or more than aiCatalogIDsMax
// ids is a bad_request error; a limit outside 1..the page maximum of the tool is brought into it.
func aiParseCatalogQuery(t aiTool, raw json.RawMessage) (aiCatalogQuery, error) {
	if !t.catalog {
		return aiCatalogQuery{}, nil
	}
	args, err := aiDecodeArgs(raw)
	if err != nil {
		return aiCatalogQuery{}, err
	}
	def, limitMax, resultMax := aiPageBounds(t)
	c := aiCatalogQuery{limit: def, limitMax: limitMax, resultMax: resultMax}
	if text, ok, err := aiScalar(args["q"]); err != nil {
		return c, aiBadArg("bad value of q")
	} else if ok {
		c.q = strings.ToLower(strings.TrimSpace(text))
	}
	if list, isList := args["ids"].([]any); isList {
		if len(list) > aiCatalogIDsMax {
			return c, aiBadArg(fmt.Sprintf("ids holds more than %d ids", aiCatalogIDsMax))
		}
		c.hasIDs, c.ids = true, map[string]bool{}
		for _, item := range list {
			text, ok, err := aiScalar(item)
			if err != nil {
				return c, aiBadArg("bad value of ids")
			}
			if ok && text != "" {
				c.ids[text] = true
			}
		}
	} else if args["ids"] != nil {
		return c, aiBadArg("bad value of ids")
	}
	if text, ok, err := aiScalar(args["limit"]); err != nil {
		return c, aiBadArg("bad value of limit")
	} else if ok {
		n, err := strconv.Atoi(text)
		if err != nil {
			return c, aiBadArg("bad value of limit")
		}
		c.limit = min(max(n, 1), limitMax)
	}
	if text, ok, err := aiScalar(args["offset"]); err != nil {
		return c, aiBadArg("bad value of offset")
	} else if ok {
		n, err := strconv.Atoi(text)
		if err != nil || n < 0 {
			return c, aiBadArg("bad value of offset")
		}
		c.offset = n
	}
	if len(c.campaignView().full) > 0 {
		c.resultMax = aiResultMax
	}
	return c, nil
}

// aiPageEnvelope is the room in bytes kept free in a page for the fields around its items.
const aiPageEnvelope = 512

// apply keeps the records of the list v that match the query and returns the requested page as
// {items, total, offset, limit}, where total counts every match and limit is the size asked for.
// The page holds fewer rows than limit, and carries truncated=true, when the rows would pass
// resultMax; at least one row is always kept. While matches remain after the page, next_offset is the offset of the next page.
// A value that is not a list is returned as it is.
func (c aiCatalogQuery) apply(v any) any {
	rows, ok := v.([]any)
	if !ok {
		return v
	}
	kept := make([]any, 0, len(rows))
	for _, row := range rows {
		if c.match(row) {
			kept = append(kept, row)
		}
	}
	start := min(c.offset, len(kept))
	end := min(start+c.limit, len(kept))
	size, cut := aiPageEnvelope, false
	for i := start; i < end; i++ {
		size += aiJSONLen(kept[i]) + 1
		if size > c.resultMax && i > start {
			end, cut = i, true
			break
		}
	}
	page := map[string]any{"items": kept[start:end], "total": len(kept), "offset": c.offset, "limit": c.limit}
	if cut {
		page["truncated"] = true
	}
	if end < len(kept) {
		page["next_offset"] = end
	}
	return page
}

// match reports whether one record passes the ids and the search text. The text is a substring of
// the id, the name, the title, the host or the number, ignoring case.
func (c aiCatalogQuery) match(row any) bool {
	m, ok := row.(map[string]any)
	if !ok {
		return c.q == "" && !c.hasIDs
	}
	if c.hasIDs && !c.ids[aiRowText(m["id"])] {
		return false
	}
	if c.q == "" {
		return true
	}
	for _, key := range []string{"id", "name", "title", "host", "num"} {
		if strings.Contains(strings.ToLower(aiRowText(m[key])), c.q) {
			return true
		}
	}
	return false
}

// aiRowText returns the text of a string or number field of a decoded record, or "".
func aiRowText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	}
	return ""
}
