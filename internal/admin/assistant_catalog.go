package admin

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// aiCatalogQuery is the search and the page a catalog list call asks for. The executor applies it
// to the list the handler answered, so the user's own view is what gets searched.
type aiCatalogQuery struct {
	// q is the lower-case search text, empty for no search.
	q string
	// ids is the set of wanted ids; hasIDs tells an empty list from no list.
	ids    map[string]bool
	hasIDs bool
	// limit is the page size, 1..aiReadLimitMax; offset is the number of matches skipped.
	limit, offset int
}

// aiParseCatalogQuery reads q, ids, limit and offset of a catalog list call. A tool that is not a
// catalog list gives the zero query. A wrong type, a negative offset or more than aiCatalogIDsMax
// ids is a bad_request error; a limit outside 1..aiReadLimitMax is brought into it.
func aiParseCatalogQuery(t aiTool, raw json.RawMessage) (aiCatalogQuery, error) {
	if !t.catalog {
		return aiCatalogQuery{}, nil
	}
	args, err := aiDecodeArgs(raw)
	if err != nil {
		return aiCatalogQuery{}, err
	}
	c := aiCatalogQuery{limit: aiCatalogLimitDefault}
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
		c.limit = min(max(n, 1), aiReadLimitMax)
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
	return c, nil
}

// apply keeps the records of the list v that match the query and returns the requested page as
// {items, total, offset, limit}, where total counts every match. A value that is not a list is
// returned as it is.
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
	return map[string]any{"items": kept[start:end], "total": len(kept), "offset": c.offset, "limit": c.limit}
}

// match reports whether one record passes the ids and the search text. The text is a substring of
// the id, the name, the title or the host, ignoring case.
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
	for _, key := range []string{"id", "name", "title", "host"} {
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
