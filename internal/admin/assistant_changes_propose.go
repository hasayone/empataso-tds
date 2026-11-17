package admin

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/security"
)

// aiChgScope returns ctx carrying the session and the section of a panel request: the board
// scope that every handler of the panel runs under. board is the section header value, empty for
// the default one of the user. It returns a 403 error when the user may not open the section.
func (s *Server) aiChgScope(ctx context.Context, sess security.Session, board string) (context.Context, error) {
	req, err := http.NewRequestWithContext(context.WithValue(ctx, sessionKey, sess), http.MethodGet, "/", http.NoBody)
	if err != nil {
		return nil, aiPlainError(http.StatusInternalServerError, "scope error")
	}
	if board = strings.TrimSpace(board); board != "" {
		req.Header.Set(boardHeader, board)
	}
	var got context.Context
	w := newAIBoundedWriter(1 << 12)
	capture := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = r.Context() })
	if err := s.aiServe(s.boardScope(capture), w, req, "scope"); err != nil {
		return nil, aiPlainError(http.StatusInternalServerError, "scope error")
	}
	if got == nil {
		if w.status == http.StatusForbidden {
			return nil, aiPlainError(http.StatusForbidden, "forbidden: the section is not available")
		}
		return nil, aiPlainError(http.StatusInternalServerError, "scope error")
	}
	return got, nil
}

// aiFindRaw returns the stored record of st with the given id as the JSON the store holds, and
// whether it exists. A store that cannot be read is a 500 error.
func aiFindRaw[T any](ctx context.Context, st *JSONStore[T], id string, idOf func(T) string) (json.RawMessage, bool, error) {
	list, err := st.List(ctx)
	if err != nil {
		return nil, false, aiPlainError(http.StatusInternalServerError, "read error")
	}
	for _, item := range list {
		if idOf(item) == id {
			return aiRaw(item), true, nil
		}
	}
	return nil, false, nil
}

// aiChgStored returns the raw stored record of the resource with the given id and whether it
// exists. The raw JSON is the encoding of the stored record, the one recordHash hashes.
// A missing store or a read failure is a 500 error.
func (s *Server) aiChgStored(ctx context.Context, resource, id string) (json.RawMessage, bool, error) {
	switch resource {
	case "offers":
		return aiFindRaw(ctx, s.cfg.Offers, id, func(o config.Offer) string { return o.ID })
	case "landings":
		return aiFindRaw(ctx, s.cfg.Landings, id, func(l config.Landing) string { return l.ID })
	case "affiliate_networks":
		return aiFindRaw(ctx, s.cfg.Networks, id, func(n config.AffiliateNetwork) string { return n.ID })
	case "traffic_sources":
		return aiFindRaw(ctx, s.cfg.Sources, id, func(src config.TrafficSource) string { return src.ID })
	case "campaigns":
		if s.cfg.Groups == nil {
			return nil, false, aiPlainError(http.StatusInternalServerError, "read error")
		}
		list, err := s.cfg.Groups.List(ctx)
		if err != nil {
			return nil, false, aiPlainError(http.StatusInternalServerError, "read error")
		}
		g, ok := findGroup(list, id)
		if !ok {
			return nil, false, nil
		}
		return aiRaw(g), true, nil
	}
	return nil, false, aiBadPatch("unknown resource")
}

// aiChgVisible reports whether the user of sctx may see the stored record m, with the checks the
// read handlers make: the section of the record and the grants on it.
// sctx comes from aiChgScope and m is the decoded stored record.
func (s *Server) aiChgVisible(sctx context.Context, spec aiChgResource, m map[string]any) bool {
	str := func(k string) string { x, _ := m[k].(string); return x }
	v := s.viewerFrom(sctx)
	if !v.seesBoard(str("board_id")) {
		return false
	}
	return v.admin || v.canSee(spec.res, ref{id: str("id"), label: str(spec.label), owner: str("created_by"), board: str("board_id")})
}

// aiChgNextFlowID returns a campaign id that no campaign holds as id or alias: flow<N>, with N
// counted up from the number of campaigns plus one, as the panel names a new campaign.
func (s *Server) aiChgNextFlowID(ctx context.Context) (string, error) {
	var list []config.Group
	if s.cfg.Groups != nil {
		var err error
		if list, err = s.cfg.Groups.List(ctx); err != nil {
			return "", aiPlainError(http.StatusInternalServerError, "read error")
		}
	}
	for n := len(list) + 1; ; n++ {
		id := "flow" + strconv.Itoa(n)
		if _, taken := findGroup(list, id); !taken && !aliasTakenAmong(list, "", id) {
			return id, nil
		}
	}
}

// aiChgDeps lists the records that link to the stored record of a resource: the campaigns that use
// an offer, a landing or a traffic source, the offers of an affiliate network, and the campaigns
// that hand visitors to a campaign by its id or one of its aliases (redirect "group").
// Names are redacted, because they reach the model in the text of an error.
//
// The check and the delete that follows it are one step only where the caller holds the lock of
// the list the links live in. A campaign list is covered: a catalog delete holds groupsMu, and a
// campaign delete repeats the check in OnBefore under groupsMu. The offers of a network are not:
// the offer store has its own lock, which a callback under the network store lock must not take,
// so an offer created for the network between the check and the delete is a narrow window that
// leaves an offer with a missing network. A revert of the delete reports it as ai_conflict.
// A list that cannot be read is a 500 error.
//
// secrets is the list of the turn that calls it; nil means the secrets of the process.
func (s *Server) aiChgDeps(ctx context.Context, resource, id string, secrets []string) ([]aiLinkView, error) {
	secrets = s.aiSecretsOr(secrets)
	name := func(n string) string { return aiRedactText(n, secrets) }
	var out []aiLinkView
	switch resource {
	case "offers", "landings", "traffic_sources", "campaigns":
		if s.cfg.Groups == nil {
			return nil, nil
		}
		groups, err := s.cfg.Groups.List(ctx)
		if err != nil {
			return nil, aiPlainError(http.StatusInternalServerError, "read error")
		}
		res, ids := aiChgResources[resource].res, []string{id}
		if resource == "campaigns" {
			if self, ok := findGroup(groups, id); ok {
				ids = append(ids, self.Aliases...)
			}
		}
		for _, g := range groups {
			if resource == "campaigns" && g.ID == id {
				continue
			}
			used := collectGroupLinks(g)[res]
			for _, want := range ids {
				if _, ok := used[want]; ok {
					out = append(out, aiLinkView{Resource: "campaigns", ID: g.ID, Name: name(g.Name)})
					break
				}
			}
		}
	case "affiliate_networks":
		if s.cfg.Offers == nil {
			return nil, nil
		}
		offers, err := s.cfg.Offers.List(ctx)
		if err != nil {
			return nil, aiPlainError(http.StatusInternalServerError, "read error")
		}
		for _, o := range offers {
			if o.NetworkID == id {
				out = append(out, aiLinkView{Resource: "offers", ID: o.ID, Name: name(o.Name)})
			}
		}
	}
	return out, nil
}

// aiChgLinksText names the first dependents of links for an error message: the resource, the id
// and the name of each, then how many more there are.
func aiChgLinksText(links []aiLinkView) string {
	names := make([]string, 0, aiChangeLinksShown)
	for i, l := range links {
		if i == aiChangeLinksShown {
			break
		}
		text := strings.TrimSuffix(l.Resource, "s") + " " + l.ID
		if l.Name != "" {
			text += " (" + l.Name + ")"
		}
		names = append(names, text)
	}
	text := strings.Join(names, ", ")
	if more := len(links) - len(names); more > 0 {
		text += " and " + strconv.Itoa(more) + " more"
	}
	return text
}

// aiChgHasLinks returns the 409 ai_has_links refusal of the delete rec describes, for a record
// that the records in links use. Its params are {"links": [{resource, id, name}]}. Its View is the
// card of rec (diff and title as computed so far) with status "rejected", the links, the error text
// and its code and params; nothing is stored.
func aiChgHasLinks(rec aiChangeRec, links []aiLinkView) *aiLinksError {
	ae := newAIErrorP(http.StatusConflict, aiErrHasLinks, "the record is used by "+aiChgLinksText(links),
		map[string]any{"links": links})
	v := rec.view()
	v.Status = aiChangeRejected
	v.Links = links
	v.Error, v.ErrorCode, v.ErrorParams = ae.Error(), aiErrHasLinks, ae.Params
	return &aiLinksError{aiError: ae, View: v}
}

// aiChgShow returns v as the card shows it: a copy without the hidden campaign fields (when
// campaign is true) and without secrets. red is the redactor of the proposal, made once from the
// secrets of the process.
func aiChgShow(red *aiRedactor, v any, campaign bool) any {
	c := aiChgClone(v)
	if campaign {
		c = aiStripKeys(c, aiCampaignHidden)
	}
	return red.value(c)
}

// aiChgTime formats t as UTC RFC 3339, or "" for the zero time.
func aiChgTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// view returns the card of the entry.
func (r aiChangeRec) view() aiChangeView {
	c := r.Change
	entity := c.EntityID
	if entity == "" {
		entity = r.Proposal.EntityID
	}
	v := aiChangeView{
		ID: r.Proposal.ID, ChatID: r.Proposal.ChatID, MessageID: r.Proposal.MessageID,
		Resource: r.Proposal.Resource, Op: r.Proposal.Op, EntityID: entity, Title: r.Title,
		Status: c.Status, Diff: r.Diff, ExternalHostChange: r.ExternalHost,
		LandingFiles: r.Files, Renumbered: r.Renumbered, Created: aiChgTime(r.Proposal.Created),
		Applied: aiChgTime(c.Applied), Reverted: aiChgTime(c.Reverted), Error: c.Error,
		ErrorCode: r.ErrorCode, ErrorParams: r.ErrorParams,
	}
	if v.Error != "" && v.ErrorCode == "" {
		v.ErrorCode = aiCodeOf(v.Error)
	}
	if v.Diff == nil {
		v.Diff = []aiDiffEntry{}
	}
	v.Links = []aiLinkView{}
	return v
}

// aiProposeReq is one propose call of the model: the user and the section it runs for, the reply
// that produced it (ChatID and MsgID), the propose tool of the registry and its arguments
// {"id"?, "patch"?}. Secrets is the list of the secrets of the turn (see aiTurn.secrets); nil means
// the secrets of the process are collected.
type aiProposeReq struct {
	Sess    security.Session
	Board   string
	ChatID  string
	MsgID   string
	Tool    aiTool
	Args    json.RawMessage
	Secrets []string
}

// aiPropose stores a change the model asked for and returns its card. Nothing is written to
// any catalog: the user applies the card later.
//
// req names the user of the turn, the section of the page, the reply that produced the call, a
// propose tool of the registry and its arguments.
// The patch may hold only the fields of aiChgResources: never id, created_by, board_id or num; a
// campaign never holds postbacks, postback_ids, curl, b_curl or an s2s key. A value with a
// redaction mark is refused. An update or delete needs a stored record the user may see; a create
// needs an id that is free (a campaign gets a free flow<N> id when none is named). A delete of a
// record that others link to is 409 ai_has_links, and one of a landing too large to archive is 409
// ai_landing_too_large. The card holds the diff of the visible values, a host_changed mark per
// field and external_host_change for a new URL host.
//
// It returns errAISessionGone for a user that is no longer valid, 403 for a user outside the beta,
// 400 ai_bad_patch or ai_redacted_value for a refused proposal, 404 for a missing or hidden
// record, 409 ai_conflict for a taken id, and a 5xx error when the files cannot be used.
func (s *Server) aiPropose(ctx context.Context, req aiProposeReq) (aiChangeView, error) {
	reg, spec, err := s.aiChgProposeTool(req)
	if err != nil {
		return aiChangeView{}, err
	}
	id, patch, err := aiChgParseArgs(reg.Op, req.Args)
	if err != nil {
		return aiChangeView{}, err
	}
	if err := aiChgCheckProposed(spec, reg.Op, patch); err != nil {
		return aiChangeView{}, err
	}
	sctx, err := s.aiChgScope(ctx, req.Sess, req.Board)
	if err != nil {
		return aiChangeView{}, err
	}
	propID, err := aiChgNewID()
	if err != nil {
		return aiChangeView{}, err
	}
	rec := aiChangeRec{Proposal: aiProposal{
		ID: propID, ChatID: req.ChatID, MessageID: req.MsgID, UID: s.aiUsageUID(req.Sess), Tool: reg.Name,
		Resource: reg.Resource, Op: reg.Op, Board: boardFrom(sctx), Created: time.Now().UTC(),
	}}
	var before map[string]any
	if reg.Op == aiOpCreate {
		id, err = s.aiChgFreeID(ctx, spec, id)
	} else {
		var raw json.RawMessage
		before, raw, err = s.aiChgExisting(ctx, sctx, spec, id)
		rec.Proposal.BeforeHash = rawHash(raw)
	}
	if err != nil {
		return aiChangeView{}, err
	}
	rec.Proposal.EntityID = id
	if err := s.aiChgFill(ctx, &rec, spec, req.Secrets, before, patch); err != nil {
		return aiChangeView{}, err
	}
	rec.Change = aiChange{
		ID: propID, ProposalID: propID, ChatID: req.ChatID, MessageID: req.MsgID,
		UID: rec.Proposal.UID, Resource: reg.Resource, EntityID: id, Op: reg.Op, Board: rec.Proposal.Board,
		Status: aiChangeProposed, Created: rec.Proposal.Created,
	}
	if err := s.aiChgMutate(func(recs *[]aiChangeRec) (bool, error) {
		*recs = append(*recs, rec)
		return true, nil
	}); err != nil {
		return aiChangeView{}, err
	}
	return rec.view(), nil
}

// aiChgProposeTool checks that the tool of req is a propose tool of the registry and that the
// session may use the assistant. It returns the registry entry and the table row of its resource,
// 400 ai_bad_patch for a tool that cannot propose, errAISessionGone for a user that is no longer
// valid, or 403 for a user outside the beta.
func (s *Server) aiChgProposeTool(req aiProposeReq) (aiTool, aiChgResource, error) {
	t := req.Tool
	reg, ok := aiToolNamed(t.Name)
	spec, known := aiChgResources[reg.Resource]
	if !ok || !known || reg.Mode != asstPropose || reg.Pattern != t.Pattern || reg.Resource != t.Resource || reg.Op != t.Op {
		return aiTool{}, aiChgResource{}, aiBadPatch("this tool cannot propose a change")
	}
	if !s.aiSessionValid(req.Sess) {
		return aiTool{}, aiChgResource{}, errAISessionGone
	}
	if !s.aiAllowed(req.Sess) {
		return aiTool{}, aiChgResource{}, aiPlainError(http.StatusForbidden, "forbidden")
	}
	return reg, spec, nil
}

// aiChgCheckProposed checks the patch of a create against the keys and the marks of spec and the
// patch of an update against its keys; a delete has no patch. The marks of an update are checked
// later, after the values the model got in a redacted form are put back (aiChgUnmask).
// It returns a 400 ai_bad_patch or ai_redacted_value error.
func aiChgCheckProposed(spec aiChgResource, op string, patch map[string]any) error {
	switch op {
	case aiOpCreate:
		return aiChgCheckPatch(spec, patch)
	case aiOpUpdate:
		return aiChgCheckKeys(spec, patch)
	}
	return nil
}

// aiChgFreeID returns the id a created record will have. id is the id the model named, or "".
// A campaign without an id gets a free flow<N> id; a catalog record without one gets "" and the
// catalog hands one out at the write. A named or chosen id must be free: not the id of a stored
// record and, for a campaign, not the alias of another one (409 ai_conflict). A list that
// cannot be read is a 500 error.
func (s *Server) aiChgFreeID(ctx context.Context, spec aiChgResource, id string) (string, error) {
	if id == "" && spec.name == "campaigns" {
		next, err := s.aiChgNextFlowID(ctx)
		if err != nil {
			return "", err
		}
		id = next
	}
	if id == "" {
		return "", nil
	}
	_, exists, err := s.aiChgStored(ctx, spec.name, id)
	if err != nil {
		return "", err
	}
	if exists {
		return "", aiChgConflict(aiReasonIDTaken, "the id already exists", "id", id)
	}
	if spec.name == "campaigns" && s.cfg.Groups != nil {
		groups, err := s.cfg.Groups.List(ctx)
		if err != nil {
			return "", aiPlainError(http.StatusInternalServerError, "read error")
		}
		if aliasTakenAmong(groups, "", id) {
			return "", aiChgConflict(aiReasonSlugTaken, "the id is used as an alias", "slug", id)
		}
	}
	return id, nil
}

// aiChgExisting loads the stored record an update or a delete is about. sctx is the board scope
// of the user (aiChgScope). It returns the decoded record and its raw JSON, 404 for a record that
// is missing, not an object or hidden from the user, or a 5xx error when the store cannot be read.
func (s *Server) aiChgExisting(ctx, sctx context.Context, spec aiChgResource, id string) (map[string]any, json.RawMessage, error) {
	raw, exists, err := s.aiChgStored(ctx, spec.name, id)
	if err != nil {
		return nil, nil, err
	}
	var before map[string]any
	if exists {
		before, err = aiChgObject(raw)
	}
	if !exists || err != nil || !s.aiChgVisible(sctx, spec, before) {
		return nil, nil, aiPlainError(http.StatusNotFound, "not found")
	}
	return before, raw, nil
}

// aiChgFill computes what a card shows and the proposal holds, and checks the merged record.
// rec is the entry being built (Proposal is already set), secrets the list of the turn (nil: the
// secrets of the process), before the decoded stored record (nil for a create) and patch the
// decoded patch. It sets the patch, the before view, the title, the
// diff, the landing statistics and the external-host mark on rec.
//
// Everything a card or the model sees is built from the redacted view of the stored record
// (secrets cut out, hidden campaign fields dropped): the fields of a delete diff come from that
// view, never from the raw record. A campaign streams patch must keep every stored stream that
// holds hidden settings under its name.
//
// It returns 400 ai_bad_patch for a record that fails its checks, an update that changes nothing
// or a streams list that drops hidden settings, 409 ai_has_links (naming the dependents in the
// text) or ai_landing_too_large for a delete that cannot be undone, and a 5xx error when a list
// cannot be read.
func (s *Server) aiChgFill(ctx context.Context, rec *aiChangeRec, spec aiChgResource, secrets []string, before, patch map[string]any) error {
	p := &rec.Proposal
	secrets = s.aiSecretsOr(secrets)
	red := newAIRedactor(secrets, false)
	var shown map[string]any
	if before != nil {
		shown = aiChgAsObject(aiChgShow(red, before, spec.name == "campaigns"))
	}
	if p.Op != aiOpDelete {
		if err := aiChgFillPatch(rec, spec, red, shown, before, patch); err != nil {
			return err
		}
	}
	aiChgFillBefore(rec, shown)
	rec.Title = red.text(aiChgTitleOf(p.EntityID, before, patch))
	if p.Op != aiOpDelete {
		return nil
	}
	return s.aiChgFillDelete(ctx, rec, spec, secrets)
}

// aiChgActualPatch returns the patch a write will carry. For an update it puts back the stored
// values the model sent back in the redacted form it saw (aiChgUnmask), then refuses a mark that
// is left and, for a campaign, a streams list that drops hidden settings. A create has no stored
// values, so its patch is returned as it is. shown is the redacted stored record and before the
// raw one. It returns a 400 ai_bad_patch or ai_redacted_value error.
func aiChgActualPatch(spec aiChgResource, op string, red *aiRedactor, shown, before, patch map[string]any) (map[string]any, error) {
	if op != aiOpUpdate {
		return patch, nil
	}
	actual := aiChgAsObject(aiChgUnmask(patch, shown, before))
	if err := aiChgCheckMarks(actual); err != nil {
		return nil, err
	}
	if streams, set := actual["streams"]; spec.name == "campaigns" && set {
		if err := aiChgCheckStreamsKept(before, streams, red); err != nil {
			return nil, err
		}
	}
	return actual, nil
}

// aiChgFillPatch checks the merged record of a create or an update and sets the patch, the diff
// and the external-host mark on rec. patch stays what the model sent and is what the proposal
// stores, so no secret reaches changes.json or a card; the checks and the host test use the
// patch with the stored values put back. shown is the redacted stored record, before the raw one
// (both nil for a create). It returns 400 ai_bad_patch for a record that fails its checks or an
// update that changes nothing, or the error of aiChgActualPatch.
func aiChgFillPatch(rec *aiChangeRec, spec aiChgResource, red *aiRedactor, shown, before, patch map[string]any) error {
	p := &rec.Proposal
	actual, err := aiChgActualPatch(spec, p.Op, red, shown, before, patch)
	if err != nil {
		return err
	}
	merged := aiChgMerge(spec, p.Op, p.EntityID, before, actual)
	probe := merged
	if _, hasID := probe["id"]; !hasID {
		probe = maps.Clone(merged)
		probe["id"] = "0"
	}
	if msg := spec.check(probe); msg != "" {
		return aiBadPatch(msg)
	}
	patchRaw, err := aiMarshal(patch)
	if err != nil {
		return aiBadPatch("the patch cannot be encoded")
	}
	p.Patch = patchRaw
	keys := aiChgChangedKeys(p.Op, patch, merged, before)
	if p.Op == aiOpUpdate && len(keys) == 0 {
		return aiBadPatchR(aiReasonNoChange, "the patch changes nothing")
	}
	campaign := spec.name == "campaigns"
	for _, k := range keys {
		e := aiDiffEntry{Field: k, Before: shown[k], After: aiChgShow(red, patch[k], campaign), HostChanged: aiChgHostChanged(k, before[k], actual[k])}
		rec.Diff = append(rec.Diff, e)
		rec.ExternalHost = rec.ExternalHost || e.HostChanged
	}
	return nil
}

// aiChgChangedKeys returns, sorted, the keys of patch the card lists. An update lists only the
// keys whose merged value differs from the stored one; a create lists every key.
func aiChgChangedKeys(op string, patch, merged, before map[string]any) []string {
	keys := make([]string, 0, len(patch))
	for _, k := range slices.Sorted(maps.Keys(patch)) {
		if op == aiOpUpdate && aiChgCanon(merged[k]) == aiChgCanon(before[k]) {
			continue
		}
		keys = append(keys, k)
	}
	return keys
}

// aiChgFillBefore sets the before view of the stored record on rec and, for a delete, lists every
// field of it in the diff. shown is the redacted stored record, nil for a create.
func aiChgFillBefore(rec *aiChangeRec, shown map[string]any) {
	if shown == nil {
		return
	}
	if view, err := aiMarshal(shown); err == nil {
		rec.Proposal.BeforeView = view
	}
	if rec.Proposal.Op != aiOpDelete {
		return
	}
	for _, k := range slices.Sorted(maps.Keys(shown)) {
		rec.Diff = append(rec.Diff, aiDiffEntry{Field: k, Before: shown[k]})
	}
}

// aiChgTitleOf returns the name a card carries: the name in the patch, else the stored name, else
// the id of the record.
func aiChgTitleOf(entityID string, before, patch map[string]any) string {
	if name, _ := patch["name"].(string); name != "" {
		return name
	}
	if name, _ := before["name"].(string); name != "" {
		return name
	}
	return entityID
}

// aiChgFillDelete runs the checks that belong to a delete and sets the landing statistics on
// rec. It returns 409 ai_has_links when other records use the target, 409 ai_landing_too_large
// for a landing that cannot be archived, or a 5xx error when a list or the files cannot be read.
// secrets is the list of the turn, passed on to the dependents lookup.
func (s *Server) aiChgFillDelete(ctx context.Context, rec *aiChangeRec, spec aiChgResource, secrets []string) error {
	id := rec.Proposal.EntityID
	links, err := s.aiChgDeps(ctx, spec.name, id, secrets)
	if err != nil {
		return err
	}
	if len(links) > 0 {
		return aiChgHasLinks(*rec, links)
	}
	if spec.name == "landings" {
		stats, err := s.aiLandingScan(id)
		if err != nil {
			return err
		}
		rec.Files = &stats
	}
	return nil
}
