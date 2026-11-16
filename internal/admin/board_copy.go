package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/egerkuzma/emtds/internal/config"
	"github.com/egerkuzma/emtds/internal/security"
)

type boardPickBody struct {
	From      string   `json:"from"`
	To        string   `json:"to"`
	Campaigns []string `json:"campaigns"`
	Offers    []string `json:"offers"`
	Landings  []string `json:"landings"`
	Networks  []string `json:"networks"`
	Sources   []string `json:"sources"`
	S2S       []string `json:"s2s"`
}

type boardTransferCounts struct {
	Campaigns int `json:"campaigns"`
	Offers    int `json:"offers"`
	Landings  int `json:"landings"`
	Networks  int `json:"networks"`
	Sources   int `json:"sources"`
	S2S       int `json:"s2s"`
}

type boardIDMaps struct {
	Campaigns map[string]string `json:"campaigns,omitempty"`
	Offers    map[string]string `json:"offers,omitempty"`
	Landings  map[string]string `json:"landings,omitempty"`
	Networks  map[string]string `json:"networks,omitempty"`
	Sources   map[string]string `json:"sources,omitempty"`
	S2S       map[string]string `json:"s2s,omitempty"`
}

type boardDropped struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

type boardImportResponse struct {
	Copied  boardTransferCounts `json:"copied"`
	Map     boardIDMaps         `json:"map"`
	Dropped []boardDropped      `json:"dropped"`
}

type boardMoveResponse struct {
	Moved   boardTransferCounts `json:"moved"`
	Dropped []boardDropped      `json:"dropped"`
}

const boardTransferBodyMax = 1 << 20

// handleBoardImportPost copies picked catalog rows from body.from into the request section.
// It answers 400 when from equals the current section, 403 when from is invisible or create is denied,
// and 200 with copied counts, id maps and dropped links on success.
func (s *Server) handleBoardImportPost(w http.ResponseWriter, r *http.Request) {
	var body boardPickBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, boardTransferBodyMax)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	v := s.viewerFrom(r.Context())
	cur := v.boardCurrent
	from := config.NormalizeBoardID(body.From)
	if config.SameBoard(from, cur) {
		writeErr(w, http.StatusBadRequest, "same section")
		return
	}
	sess := sessionFrom(r.Context())
	if ok, err := s.sessionCanOpenBoard(r.Context(), sess, from); err != nil {
		writeErr(w, http.StatusInternalServerError, "boards error")
		return
	} else if !ok {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	if msg := boardTransferCreateAccess(v, body); msg != "" {
		writeErr(w, http.StatusForbidden, msg)
		return
	}
	if s.cfg.Groups == nil {
		writeErr(w, http.StatusServiceUnavailable, "groups store unavailable")
		return
	}
	s.groupsMu.Lock()
	defer s.groupsMu.Unlock()
	out, code, msg := s.runBoardImport(r.Context(), v, from, cur, body, sess.UID)
	if code != 0 {
		writeErr(w, code, msg)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleBoardMovePost reassigns board_id on picked rows from the current section to body.to.
// It answers 400 when to equals the current section, 403 when to is invisible or create is denied,
// and 200 with moved counts and dropped cross-section links on success.
func (s *Server) handleBoardMovePost(w http.ResponseWriter, r *http.Request) {
	var body boardPickBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, boardTransferBodyMax)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	v := s.viewerFrom(r.Context())
	from := v.boardCurrent
	to := config.NormalizeBoardID(body.To)
	if config.SameBoard(from, to) {
		writeErr(w, http.StatusBadRequest, "same section")
		return
	}
	sess := sessionFrom(r.Context())
	if ok, err := s.sessionCanOpenBoard(r.Context(), sess, to); err != nil {
		writeErr(w, http.StatusInternalServerError, "boards error")
		return
	} else if !ok {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	if msg := boardTransferCreateAccess(v, body); msg != "" {
		writeErr(w, http.StatusForbidden, msg)
		return
	}
	if s.cfg.Groups == nil {
		writeErr(w, http.StatusServiceUnavailable, "groups store unavailable")
		return
	}
	s.groupsMu.Lock()
	defer s.groupsMu.Unlock()
	out, code, msg := s.runBoardMove(r.Context(), v, from, to, body)
	if code != 0 {
		writeErr(w, code, msg)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// sessionCanOpenBoard reports whether sess may use section board.
func (s *Server) sessionCanOpenBoard(ctx context.Context, sess security.Session, board string) (bool, error) {
	meta, err := s.boardMeta(ctx, sess)
	if err != nil {
		return false, err
	}
	return meta.canOpen(board), nil
}

// boardTransferCreateAccess checks create rights for every non-empty pick list.
func boardTransferCreateAccess(v viewer, body boardPickBody) string {
	check := func(res string, ids []string) string {
		if len(ids) == 0 {
			return ""
		}
		if !v.canCreate(res) {
			return "forbidden"
		}
		return ""
	}
	if msg := check(resCampaigns, body.Campaigns); msg != "" {
		return msg
	}
	if msg := check(resOffers, body.Offers); msg != "" {
		return msg
	}
	if msg := check(resLandings, body.Landings); msg != "" {
		return msg
	}
	if msg := check(resNetworks, body.Networks); msg != "" {
		return msg
	}
	if msg := check(resSources, body.Sources); msg != "" {
		return msg
	}
	if len(body.S2S) > 0 && !v.admin {
		return "forbidden"
	}
	return ""
}

// runBoardImport copies the picked records from board from onto board to.
// ctx bounds the catalog reads. v is the caller. owner is written on a copy that has none.
// The source records stay. Each copy gets a new id. Checker-managed postback templates are skipped.
// It returns the copy map, an HTTP status and a client message. A zero status means success.
func (s *Server) runBoardImport(ctx context.Context, v viewer, from, to string, body boardPickBody, owner string) (boardImportResponse, int, string) {
	var out boardImportResponse
	out.Map = boardIDMaps{
		Campaigns: map[string]string{},
		Offers:    map[string]string{},
		Landings:  map[string]string{},
		Networks:  map[string]string{},
		Sources:   map[string]string{},
		S2S:       map[string]string{},
	}
	var dropped []boardDropped

	groups, err := s.cfg.Groups.List(ctx)
	if err != nil {
		return out, http.StatusInternalServerError, "groups error"
	}

	wantCampaigns := pickSet(body.Campaigns)
	wantOffers := pickSet(body.Offers)
	wantLandings := pickSet(body.Landings)
	wantNetworks := pickSet(body.Networks)
	wantSources := pickSet(body.Sources)
	wantS2S := pickSet(body.S2S)

	targetOfferIDs := boardOfferIDs(s.cfg.Offers, to)
	targetLandingIDs := boardLandingIDs(s.cfg.Landings, to)
	targetNetworkIDs := boardNetworkIDs(s.cfg.Networks, to)
	targetSourceIDs := boardTrafficSourceIDs(s.cfg.Sources, to)
	targetS2SIDs := boardS2SIDs(s.cfg.S2S, to)

	if s.cfg.Networks != nil {
		list, err := s.cfg.Networks.List(ctx)
		if err != nil {
			return out, http.StatusInternalServerError, "read error"
		}
		var next []config.AffiliateNetwork
		for _, n := range list {
			if !config.SameBoard(n.BoardID, from) || !wantNetworks[n.ID] {
				next = append(next, n)
				continue
			}
			if !v.canSee(resNetworks, ref{id: n.ID, owner: n.CreatedBy, board: n.BoardID}) {
				return out, http.StatusForbidden, "forbidden"
			}
			if targetNetworkIDs[n.ID] {
				dropped = append(dropped, boardDropped{Kind: resNetworks, ID: n.ID, Reason: "already exists"})
				next = append(next, n)
				continue
			}
			copyN, err := cloneJSON(n)
			if err != nil {
				return out, http.StatusInternalServerError, "copy error"
			}
			newID, err := s.takeCatalogID(ctx, s.cfg.SeqNetworks, s.networkFloor)
			if err != nil {
				return out, http.StatusInternalServerError, "save error"
			}
			copyN.ID = newID
			copyN.BoardID = to
			if owner != "" && copyN.CreatedBy == "" {
				copyN.CreatedBy = owner
			}
			prepNetwork(&copyN)
			out.Map.Networks[n.ID] = newID
			out.Copied.Networks++
			next = append(next, n, copyN)
			targetNetworkIDs[newID] = true
		}
		if err := s.cfg.Networks.Save(ctx, next); err != nil {
			return out, http.StatusInternalServerError, "save error"
		}
	}

	if s.cfg.Sources != nil {
		list, err := s.cfg.Sources.List(ctx)
		if err != nil {
			return out, http.StatusInternalServerError, "read error"
		}
		var next []config.TrafficSource
		for _, src := range list {
			if !config.SameBoard(src.BoardID, from) || !wantSources[src.ID] {
				next = append(next, src)
				continue
			}
			if !v.canSee(resSources, ref{id: src.ID, owner: src.CreatedBy, board: src.BoardID}) {
				return out, http.StatusForbidden, "forbidden"
			}
			if targetSourceIDs[src.ID] {
				dropped = append(dropped, boardDropped{Kind: resSources, ID: src.ID, Reason: "already exists"})
				next = append(next, src)
				continue
			}
			copyS, err := cloneJSON(src)
			if err != nil {
				return out, http.StatusInternalServerError, "copy error"
			}
			newID, err := s.takeCatalogID(ctx, s.cfg.SeqSources, s.sourceFloor)
			if err != nil {
				return out, http.StatusInternalServerError, "save error"
			}
			copyS.ID = newID
			copyS.BoardID = to
			if owner != "" && copyS.CreatedBy == "" {
				copyS.CreatedBy = owner
			}
			prepSource(&copyS)
			out.Map.Sources[src.ID] = newID
			out.Copied.Sources++
			next = append(next, src, copyS)
			targetSourceIDs[newID] = true
		}
		if err := s.cfg.Sources.Save(ctx, next); err != nil {
			return out, http.StatusInternalServerError, "save error"
		}
	}

	if s.cfg.Offers != nil {
		list, err := s.cfg.Offers.List(ctx)
		if err != nil {
			return out, http.StatusInternalServerError, "read error"
		}
		var next []config.Offer
		for _, o := range list {
			if !config.SameBoard(o.BoardID, from) || !wantOffers[o.ID] {
				next = append(next, o)
				continue
			}
			if !v.canSee(resOffers, ref{id: o.ID, owner: o.CreatedBy, board: o.BoardID}) {
				return out, http.StatusForbidden, "forbidden"
			}
			if targetOfferIDs[o.ID] {
				dropped = append(dropped, boardDropped{Kind: resOffers, ID: o.ID, Reason: "already exists"})
				next = append(next, o)
				continue
			}
			copyO, err := cloneJSON(o)
			if err != nil {
				return out, http.StatusInternalServerError, "copy error"
			}
			newID, err := s.takeCatalogID(ctx, s.cfg.SeqOffers, s.offerFloor)
			if err != nil {
				return out, http.StatusInternalServerError, "save error"
			}
			copyO.ID = newID
			copyO.BoardID = to
			if mapped, ok := out.Map.Networks[o.NetworkID]; ok {
				copyO.NetworkID = mapped
			}
			if owner != "" && copyO.CreatedBy == "" {
				copyO.CreatedBy = owner
			}
			prepOffer(&copyO)
			out.Map.Offers[o.ID] = newID
			out.Copied.Offers++
			next = append(next, o, copyO)
			targetOfferIDs[newID] = true
		}
		if err := s.cfg.Offers.Save(ctx, next); err != nil {
			return out, http.StatusInternalServerError, "save error"
		}
	}

	if s.cfg.Landings != nil {
		list, err := s.cfg.Landings.List(ctx)
		if err != nil {
			return out, http.StatusInternalServerError, "read error"
		}
		var next []config.Landing
		for _, l := range list {
			if !config.SameBoard(l.BoardID, from) || !wantLandings[l.ID] {
				next = append(next, l)
				continue
			}
			if !v.canSee(resLandings, ref{id: l.ID, owner: l.CreatedBy, board: l.BoardID}) {
				return out, http.StatusForbidden, "forbidden"
			}
			if targetLandingIDs[l.ID] {
				dropped = append(dropped, boardDropped{Kind: resLandings, ID: l.ID, Reason: "already exists"})
				next = append(next, l)
				continue
			}
			copyL, err := cloneJSON(l)
			if err != nil {
				return out, http.StatusInternalServerError, "copy error"
			}
			newID, err := s.takeCatalogID(ctx, s.cfg.SeqLandings, s.landingFloor)
			if err != nil {
				return out, http.StatusInternalServerError, "save error"
			}
			copyL.ID = newID
			copyL.BoardID = to
			if owner != "" && copyL.CreatedBy == "" {
				copyL.CreatedBy = owner
			}
			prepLanding(&copyL)
			if err := copyLandingFiles(s.cfg.LandingFiles, l.ID, newID); err != nil {
				return out, http.StatusInternalServerError, "landing files error"
			}
			out.Map.Landings[l.ID] = newID
			out.Copied.Landings++
			next = append(next, l, copyL)
			targetLandingIDs[newID] = true
		}
		if err := s.cfg.Landings.Save(ctx, next); err != nil {
			return out, http.StatusInternalServerError, "save error"
		}
	}

	if s.cfg.S2S != nil && len(wantS2S) > 0 {
		list, err := s.cfg.S2S.List(ctx)
		if err != nil {
			return out, http.StatusInternalServerError, "read error"
		}
		usedIDs := s2sIDSet(list)
		var next []config.OutboundPostback
		for _, p := range list {
			if !config.SameBoard(p.BoardID, from) || !wantS2S[p.ID] || isCheckerManagedID(p.ID) {
				next = append(next, p)
				continue
			}
			if targetS2SIDs[p.ID] {
				dropped = append(dropped, boardDropped{Kind: "s2s", ID: p.ID, Reason: "already exists"})
				next = append(next, p)
				continue
			}
			copyP, err := cloneJSON(p)
			if err != nil {
				return out, http.StatusInternalServerError, "copy error"
			}
			copyP.BoardID = to
			copyP.ID = uniqueS2SID(usedIDs, copyP)
			usedIDs[copyP.ID] = struct{}{}
			out.Map.S2S[p.ID] = copyP.ID
			out.Copied.S2S++
			next = append(next, p, copyP)
		}
		if err := s.cfg.S2S.Save(ctx, next); err != nil {
			return out, http.StatusInternalServerError, "save error"
		}
	}

	campaignBoard := campaignBoardMap(groups)
	var copiedGroups []config.Group
	var copiedOldIDs []string
	for _, g := range groups {
		if !config.SameBoard(g.BoardID, from) || !wantCampaigns[g.ID] {
			continue
		}
		if !v.canSee(resCampaigns, campaignRef(g)) {
			return out, http.StatusForbidden, "forbidden"
		}
		copyG, err := cloneJSON(g)
		if err != nil {
			return out, http.StatusInternalServerError, "copy error"
		}
		oldID := g.ID
		copyG.ID = nextCampaignID(append(groups, copiedGroups...))
		copyG.BoardID = to
		copyG.Num = 0
		copyG.Aliases = nil
		if owner != "" && copyG.CreatedBy == "" {
			copyG.CreatedBy = owner
		}
		remapCtx := boardRemapCtx{
			to:          to,
			campaigns:   out.Map.Campaigns,
			offers:      out.Map.Offers,
			landings:    out.Map.Landings,
			sources:     out.Map.Sources,
			s2s:         out.Map.S2S,
			campaignLoc: campaignBoard,
		}
		dropped = remapCopiedGroup(&copyG, oldID, remapCtx, dropped)
		out.Map.Campaigns[oldID] = copyG.ID
		out.Copied.Campaigns++
		copiedGroups = append(copiedGroups, copyG)
		copiedOldIDs = append(copiedOldIDs, oldID)
		campaignBoard[copyG.ID] = to
	}
	for i := range copiedGroups {
		remapCtx := boardRemapCtx{
			to:          to,
			campaigns:   out.Map.Campaigns,
			offers:      out.Map.Offers,
			landings:    out.Map.Landings,
			sources:     out.Map.Sources,
			s2s:         out.Map.S2S,
			campaignLoc: campaignBoard,
		}
		dropped = finalizeCopiedCampaignLinks(&copiedGroups[i], copiedOldIDs[i], remapCtx, dropped)
	}

	if len(copiedGroups) > 0 {
		groups = append(groups, copiedGroups...)
		assignGroupNums(groups)
		if msg := prepareGroups(groups); msg != "" {
			return out, http.StatusInternalServerError, msg
		}
		if err := s.cfg.Groups.Save(ctx, groups); err != nil {
			return out, http.StatusInternalServerError, "save error"
		}
	}

	out.Dropped = dropped
	return out, 0, ""
}

func (s *Server) runBoardMove(ctx context.Context, v viewer, from, to string, body boardPickBody) (boardMoveResponse, int, string) {
	var out boardMoveResponse
	var dropped []boardDropped

	wantCampaigns := pickSet(body.Campaigns)
	wantOffers := pickSet(body.Offers)
	wantLandings := pickSet(body.Landings)
	wantNetworks := pickSet(body.Networks)
	wantSources := pickSet(body.Sources)
	wantS2S := pickSet(body.S2S)

	if s.cfg.Offers != nil && len(wantOffers) > 0 {
		list, err := s.cfg.Offers.List(ctx)
		if err != nil {
			return out, http.StatusInternalServerError, "read error"
		}
		for i := range list {
			o := &list[i]
			if !config.SameBoard(o.BoardID, from) || !wantOffers[o.ID] {
				continue
			}
			if !v.canSee(resOffers, ref{id: o.ID, owner: o.CreatedBy, board: o.BoardID}) || !v.canWrite(resOffers, ref{id: o.ID, owner: o.CreatedBy, board: o.BoardID}) {
				return out, http.StatusForbidden, "forbidden"
			}
			o.BoardID = to
			out.Moved.Offers++
		}
		if err := s.cfg.Offers.Save(ctx, list); err != nil {
			return out, http.StatusInternalServerError, "save error"
		}
	}

	if s.cfg.Landings != nil && len(wantLandings) > 0 {
		list, err := s.cfg.Landings.List(ctx)
		if err != nil {
			return out, http.StatusInternalServerError, "read error"
		}
		for i := range list {
			l := &list[i]
			if !config.SameBoard(l.BoardID, from) || !wantLandings[l.ID] {
				continue
			}
			if !v.canSee(resLandings, ref{id: l.ID, owner: l.CreatedBy, board: l.BoardID}) || !v.canWrite(resLandings, ref{id: l.ID, owner: l.CreatedBy, board: l.BoardID}) {
				return out, http.StatusForbidden, "forbidden"
			}
			l.BoardID = to
			out.Moved.Landings++
		}
		if err := s.cfg.Landings.Save(ctx, list); err != nil {
			return out, http.StatusInternalServerError, "save error"
		}
	}

	if s.cfg.Networks != nil && len(wantNetworks) > 0 {
		list, err := s.cfg.Networks.List(ctx)
		if err != nil {
			return out, http.StatusInternalServerError, "read error"
		}
		for i := range list {
			n := &list[i]
			if !config.SameBoard(n.BoardID, from) || !wantNetworks[n.ID] {
				continue
			}
			if !v.canSee(resNetworks, ref{id: n.ID, owner: n.CreatedBy, board: n.BoardID}) || !v.canWrite(resNetworks, ref{id: n.ID, owner: n.CreatedBy, board: n.BoardID}) {
				return out, http.StatusForbidden, "forbidden"
			}
			n.BoardID = to
			out.Moved.Networks++
		}
		if err := s.cfg.Networks.Save(ctx, list); err != nil {
			return out, http.StatusInternalServerError, "save error"
		}
	}

	if s.cfg.Sources != nil && len(wantSources) > 0 {
		list, err := s.cfg.Sources.List(ctx)
		if err != nil {
			return out, http.StatusInternalServerError, "read error"
		}
		for i := range list {
			src := &list[i]
			if !config.SameBoard(src.BoardID, from) || !wantSources[src.ID] {
				continue
			}
			if !v.canSee(resSources, ref{id: src.ID, owner: src.CreatedBy, board: src.BoardID}) || !v.canWrite(resSources, ref{id: src.ID, owner: src.CreatedBy, board: src.BoardID}) {
				return out, http.StatusForbidden, "forbidden"
			}
			src.BoardID = to
			out.Moved.Sources++
		}
		if err := s.cfg.Sources.Save(ctx, list); err != nil {
			return out, http.StatusInternalServerError, "save error"
		}
	}

	if s.cfg.S2S != nil && len(wantS2S) > 0 {
		list, err := s.cfg.S2S.List(ctx)
		if err != nil {
			return out, http.StatusInternalServerError, "read error"
		}
		for i := range list {
			p := &list[i]
			if !config.SameBoard(p.BoardID, from) || !wantS2S[p.ID] || isCheckerManagedID(p.ID) {
				continue
			}
			p.BoardID = to
			out.Moved.S2S++
		}
		if err := s.cfg.S2S.Save(ctx, list); err != nil {
			return out, http.StatusInternalServerError, "save error"
		}
	}

	groups, err := s.cfg.Groups.List(ctx)
	if err != nil {
		return out, http.StatusInternalServerError, "groups error"
	}
	campaignBoard := campaignBoardMap(groups)
	for i := range groups {
		g := &groups[i]
		if !config.SameBoard(g.BoardID, from) || !wantCampaigns[g.ID] {
			continue
		}
		if !v.canSee(resCampaigns, campaignRef(*g)) || !v.canWrite(resCampaigns, campaignRef(*g)) {
			return out, http.StatusForbidden, "forbidden"
		}
		g.BoardID = to
		campaignBoard[g.ID] = to
		out.Moved.Campaigns++
	}
	for i := range groups {
		dropped = stripCrossBoardCampaignLinks(&groups[i], campaignBoard, dropped)
	}
	if msg := prepareGroups(groups); msg != "" {
		return out, http.StatusInternalServerError, msg
	}
	if err := s.cfg.Groups.Save(ctx, groups); err != nil {
		return out, http.StatusInternalServerError, "save error"
	}
	out.Dropped = dropped
	return out, 0, ""
}

type boardRemapCtx struct {
	to          string
	campaigns   map[string]string
	offers      map[string]string
	landings    map[string]string
	sources     map[string]string
	s2s         map[string]string
	campaignLoc map[string]string
}

// finalizeCopiedCampaignLinks fixes campaign redirects after every copy id is known.
func finalizeCopiedCampaignLinks(g *config.Group, oldID string, ctx boardRemapCtx, dropped []boardDropped) []boardDropped {
	if g.Redirect == "group" {
		target := strings.TrimSpace(g.Out)
		if mapped, ok := ctx.campaigns[target]; ok {
			g.Out = mapped
		} else if newOut, drop := resolveCampaignTarget(target, ctx); drop {
			g.Redirect = ""
			g.Out = ""
			dropped = append(dropped, boardDropped{Kind: resCampaigns, ID: oldID, Reason: "campaign not copied"})
		} else {
			g.Out = newOut
		}
	}
	for i := range g.Streams {
		if g.Streams[i].Out.Redirect != "group" {
			continue
		}
		target := strings.TrimSpace(g.Streams[i].Out.Out)
		if mapped, ok := ctx.campaigns[target]; ok {
			g.Streams[i].Out.Out = mapped
			continue
		}
		if newOut, drop := resolveCampaignTarget(target, ctx); drop {
			g.Streams[i].Out.Redirect = ""
			g.Streams[i].Out.Out = ""
			dropped = append(dropped, boardDropped{Kind: resCampaigns, ID: oldID, Reason: "campaign not copied"})
		} else {
			g.Streams[i].Out.Out = newOut
		}
	}
	return dropped
}

// remapCopiedGroup rewires catalog links on a copied campaign and reports dropped redirects.
func remapCopiedGroup(g *config.Group, oldID string, ctx boardRemapCtx, dropped []boardDropped) []boardDropped {
	if mapped, ok := ctx.sources[g.SourceID]; ok {
		g.SourceID = mapped
	}
	g.PostbackIDs = remapStringIDs(g.PostbackIDs, ctx.s2s)
	if g.Redirect == "group" {
		dropped = resolveGroupRedirect(g, g.Out, oldID, ctx, dropped)
	}
	for i := range g.Streams {
		st := &g.Streams[i]
		st.Out.Offers = remapShareRefs(st.Out.Offers, ctx.offers)
		st.Out.Landings = remapShareRefs(st.Out.Landings, ctx.landings)
		if st.Out.Redirect == "group" {
			out, drop := resolveStreamGroupRedirect(st.Out.Out, oldID, ctx, dropped)
			dropped = drop
			st.Out.Out = out
			if out == "" {
				st.Out.Redirect = ""
			}
		}
	}
	return dropped
}

func resolveGroupRedirect(g *config.Group, target, oldID string, ctx boardRemapCtx, dropped []boardDropped) []boardDropped {
	target = strings.TrimSpace(target)
	if target == "" {
		return dropped
	}
	newOut, drop := resolveCampaignTarget(target, ctx)
	if drop {
		g.Redirect = ""
		g.Out = ""
		dropped = append(dropped, boardDropped{Kind: resCampaigns, ID: oldID, Reason: "campaign not copied"})
		return dropped
	}
	g.Out = newOut
	return dropped
}

func resolveStreamGroupRedirect(target, oldID string, ctx boardRemapCtx, dropped []boardDropped) (string, []boardDropped) {
	target = strings.TrimSpace(target)
	if target == "" {
		return target, dropped
	}
	newOut, drop := resolveCampaignTarget(target, ctx)
	if drop {
		dropped = append(dropped, boardDropped{Kind: resCampaigns, ID: oldID, Reason: "campaign not copied"})
		return "", dropped
	}
	return newOut, dropped
}

func resolveCampaignTarget(target string, ctx boardRemapCtx) (string, bool) {
	if mapped, ok := ctx.campaigns[target]; ok {
		return mapped, false
	}
	board := ctx.campaignLoc[target]
	if board == "" || config.SameBoard(board, ctx.to) {
		return target, false
	}
	return "", true
}

// stripCrossBoardCampaignLinks clears redirect=group targets outside the campaign board.
func stripCrossBoardCampaignLinks(g *config.Group, campaignBoard map[string]string, dropped []boardDropped) []boardDropped {
	board := campaignBoard[g.ID]
	if g.Redirect == "group" {
		target := strings.TrimSpace(g.Out)
		if target != "" {
			if tb := campaignBoard[target]; tb == "" || !config.SameBoard(tb, board) {
				g.Redirect = ""
				g.Out = ""
				dropped = append(dropped, boardDropped{Kind: resCampaigns, ID: g.ID, Reason: "campaign not copied"})
			}
		}
	}
	for i := range g.Streams {
		if g.Streams[i].Out.Redirect != "group" {
			continue
		}
		target := strings.TrimSpace(g.Streams[i].Out.Out)
		if target == "" {
			continue
		}
		if tb := campaignBoard[target]; tb == "" || !config.SameBoard(tb, board) {
			g.Streams[i].Out.Redirect = ""
			g.Streams[i].Out.Out = ""
			dropped = append(dropped, boardDropped{Kind: resCampaigns, ID: g.ID, Reason: "campaign not copied"})
		}
	}
	return dropped
}

func remapShareRefs(refs []config.ShareRef, m map[string]string) []config.ShareRef {
	if len(refs) == 0 || len(m) == 0 {
		return refs
	}
	out := make([]config.ShareRef, 0, len(refs))
	for _, r := range refs {
		if mapped, ok := m[r.ID]; ok {
			r.ID = mapped
			out = append(out, r)
		}
	}
	return out
}

func remapStringIDs(ids []string, m map[string]string) []string {
	if len(ids) == 0 {
		return ids
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if mapped, ok := m[id]; ok {
			out = append(out, mapped)
			continue
		}
		out = append(out, id)
	}
	return out
}

func pickSet(ids []string) map[string]bool {
	m := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id != "" {
			m[id] = true
		}
	}
	return m
}

func boardCatalogIDs[T any](store *JSONStore[T], board string, idOf func(T) string, boardOf func(T) string) map[string]bool {
	m := map[string]bool{}
	if store == nil {
		return m
	}
	list, err := store.List(context.Background())
	if err != nil {
		return m
	}
	for _, item := range list {
		if config.SameBoard(boardOf(item), board) {
			m[idOf(item)] = true
		}
	}
	return m
}

func boardOfferIDs(store *JSONStore[config.Offer], board string) map[string]bool {
	return boardCatalogIDs(store, board, func(o config.Offer) string { return o.ID }, func(o config.Offer) string { return o.BoardID })
}

func boardLandingIDs(store *JSONStore[config.Landing], board string) map[string]bool {
	return boardCatalogIDs(store, board, func(l config.Landing) string { return l.ID }, func(l config.Landing) string { return l.BoardID })
}

func boardNetworkIDs(store *JSONStore[config.AffiliateNetwork], board string) map[string]bool {
	return boardCatalogIDs(store, board, func(n config.AffiliateNetwork) string { return n.ID }, func(n config.AffiliateNetwork) string { return n.BoardID })
}

func boardTrafficSourceIDs(store *JSONStore[config.TrafficSource], board string) map[string]bool {
	return boardCatalogIDs(store, board, func(s config.TrafficSource) string { return s.ID }, func(s config.TrafficSource) string { return s.BoardID })
}

func boardS2SIDs(store *JSONStore[config.OutboundPostback], board string) map[string]bool {
	m := map[string]bool{}
	if store == nil {
		return m
	}
	list, err := store.List(context.Background())
	if err != nil {
		return m
	}
	for _, p := range list {
		if config.SameBoard(p.BoardID, board) {
			m[p.ID] = true
		}
	}
	return m
}

func s2sIDSet(list []config.OutboundPostback) map[string]struct{} {
	m := map[string]struct{}{}
	for _, p := range list {
		if p.ID != "" {
			m[p.ID] = struct{}{}
		}
	}
	return m
}

func uniqueS2SID(used map[string]struct{}, p config.OutboundPostback) string {
	base := config.PostbackID(p.Signature())
	if base == "" {
		base = "pb"
	}
	id := base
	for n := 2; ; n++ {
		if _, ok := used[id]; !ok {
			return id
		}
		id = fmt.Sprintf("%s_%d", base, n)
	}
}

func cloneJSON[T any](v T) (T, error) {
	var out T
	b, err := json.Marshal(v)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return out, err
	}
	return out, nil
}

func (s *Server) takeCatalogID(_ context.Context, seq *idSeq, floorFn func() (int, error)) (string, error) {
	floor := 1
	if floorFn != nil {
		n, err := floorFn()
		if err != nil {
			return "", err
		}
		floor = n
	}
	if seq != nil {
		seq.mu.Lock()
		defer seq.mu.Unlock()
		n, err := seq.take(floor)
		if err != nil {
			return "", err
		}
		return strconv.Itoa(n), nil
	}
	return strconv.Itoa(floor), nil
}

func campaignBoardMap(groups []config.Group) map[string]string {
	m := map[string]string{}
	for _, g := range groups {
		m[g.ID] = config.NormalizeBoardID(g.BoardID)
	}
	return m
}

func nextCampaignID(groups []config.Group) string {
	taken := map[string]bool{}
	for _, g := range groups {
		taken[g.ID] = true
		for _, a := range g.Aliases {
			if a != "" {
				taken[a] = true
			}
		}
	}
	n := len(groups) + 1
	for {
		id := "flow" + strconv.Itoa(n)
		if !taken[id] {
			return id
		}
		n++
	}
}

// copyLandingFiles duplicates a local landing tree when the store is set.
func copyLandingFiles(files LandingFiles, fromID, toID string) error {
	if files == nil {
		return nil
	}
	infos, err := files.List(fromID)
	if err != nil {
		if errors.Is(err, errBadLandingID) {
			return nil
		}
		return err
	}
	for _, info := range infos {
		data, err := files.Read(fromID, info.Path)
		if err != nil {
			return err
		}
		if err := files.Write(toID, info.Path, data); err != nil {
			return err
		}
	}
	return nil
}
