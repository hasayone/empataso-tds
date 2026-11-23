package admin

import "github.com/hasayone/empataso-tds/internal/config"

// groupListDest is the destination the campaigns grid paints on a stream.
// Redirect is the kind (landing, offer, group, and the rest). Out is set only when
// Redirect is "group": the other campaign id is short, and a landing or offer URL is not.
type groupListDest struct {
	Redirect string `json:"redirect,omitempty"`
	Out      string `json:"out,omitempty"`
}

// groupListStream is one stream on the campaigns list.
// Name and Status are the columns and the search. Out uses the same shape as a stored
// stream destination, so the grid reads redirect and a campaign link without the rules.
type groupListStream struct {
	Name   string         `json:"name"`
	Status bool           `json:"status"`
	Out    *groupListDest `json:"out,omitempty"`
}

// groupListRow is one campaign on the campaigns list.
// It carries the fields the table filters, sorts and searches, and a stream head
// per stream. Rules, notes, parameters, postbacks and long destination URLs are absent.
// Streams is never null.
type groupListRow struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Status   bool              `json:"status"`
	Num      int               `json:"num,omitempty"`
	Folder   string            `json:"folder,omitempty"`
	SourceID string            `json:"source_id,omitempty"`
	Aliases  []string          `json:"aliases,omitempty"`
	DomainID string            `json:"domain_id,omitempty"`
	Redirect string            `json:"redirect,omitempty"`
	Out      string            `json:"out,omitempty"`
	BoardID  string            `json:"board_id,omitempty"`
	Streams  []groupListStream `json:"streams"`
}

// groupListRows builds the campaigns-list rows of groups.
// groups is the campaigns the caller may see, in list order. A nil or empty slice
// returns an empty, non-nil slice. It does not fail.
func groupListRows(groups []config.Group) []groupListRow {
	out := make([]groupListRow, 0, len(groups))
	for _, g := range groups {
		streams := make([]groupListStream, len(g.Streams))
		for i, s := range g.Streams {
			streams[i] = groupListStream{Name: s.Name, Status: s.Status}
			if s.Out.Redirect != "" {
				dest := &groupListDest{Redirect: s.Out.Redirect}
				if s.Out.Redirect == "group" {
					dest.Out = s.Out.Out
				}
				streams[i].Out = dest
			}
		}
		row := groupListRow{
			ID: g.ID, Name: g.Name, Status: g.Status, Num: g.Num,
			Folder: g.Folder, SourceID: g.SourceID, Aliases: append([]string(nil), g.Aliases...),
			DomainID: g.DomainID, Redirect: g.Redirect, BoardID: g.BoardID,
			Streams: streams,
		}
		if g.Redirect == "group" {
			row.Out = g.Out
		}
		out = append(out, row)
	}
	return out
}
