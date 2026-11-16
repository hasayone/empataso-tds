package admin

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/egerkuzma/emtds/internal/config"
)

// Statuses of a stored change that come before the write starts, end without one, or belong to a
// revert in progress. The other statuses (pending, done, aborted, reverted) are declared in
// assistant_types.go.
const (
	// aiChangeProposed marks a proposal that waits for the user to apply or reject it.
	aiChangeProposed = "proposed"
	// aiChangeRejected marks a proposal the user declined.
	aiChangeRejected = "rejected"
	// aiChangeReverting marks an applied change whose revert write has started and not finished.
	// Startup recovery settles it by what the store holds.
	aiChangeReverting = "reverting"
)

const (
	// aiErrBadPatch is the code of a proposal that names a forbidden or unknown field, a bad
	// value, or a change that does nothing (400).
	aiErrBadPatch = "ai_bad_patch"

	// aiChangesFile is the file of every proposal and change under the assistant directory.
	aiChangesFile = "changes.json"
	// aiBlobsDir is the directory of landing file snapshots under the assistant directory.
	aiBlobsDir = "blobs"
	// aiChangesKeep is how long a change set (and its snapshot) is kept.
	aiChangesKeep = 90 * 24 * time.Hour
	// aiChangesDirCap is the size the assistant directory may not grow past when a snapshot is added.
	aiChangesDirCap int64 = 256 << 20
	// aiChangeArgsMax is the most bytes of the arguments of one propose call.
	aiChangeArgsMax = 256 << 10
	// aiChangeLinksShown is how many dependents an ai_has_links message names.
	aiChangeLinksShown = 10
	// aiChangeAnswerMax is the most bytes of an answer an internal write may produce.
	aiChangeAnswerMax = 4 << 20

	// aiRouteApply and aiRouteRevert are the routes written into the audit of an applied
	// and of a reverted change.
	aiRouteApply  = "POST /api/ai/changes/{id}/apply"
	aiRouteRevert = "POST /api/ai/changes/{id}/revert"
)

// errAIChgNoDir is the answer of every change operation when the assistant directory is unset.
var errAIChgNoDir = &aiError{status: http.StatusServiceUnavailable, code: aiErrDisabled,
	msg: aiErrDisabled + ": the assistant directory is not configured"}

// aiApplyOpts carries the confirmations the user gave on the card.
// ConfirmDelete answers the second question of a delete; ConfirmHost accepts a new external
// address in a URL field.
type aiApplyOpts struct {
	ConfirmDelete bool
	ConfirmHost   bool
}

// aiDiffEntry is one changed field on a card: the value the user sees before and after.
// HostChanged is true when After brings a host that Before does not hold.
type aiDiffEntry struct {
	Field       string `json:"field"`
	Before      any    `json:"before"`
	After       any    `json:"after"`
	HostChanged bool   `json:"host_changed"`
}

// aiLinkView names one record that links to the record a proposal deletes. A proposal that has
// such a record is refused with an *aiLinksError, whose card lists the dependents; the card of a
// stored proposal never has links.
type aiLinkView struct {
	Resource string `json:"resource"`
	ID       string `json:"id"`
	Name     string `json:"name"`
}

// aiLinksError is the 409 ai_has_links refusal of a delete of a record that others link to.
// The embedded aiError carries the status, the code and the text (it is found by errors.As as
// an *aiError too); View is the card of the refused delete with status "rejected", the
// dependents in Links and the text in Error. The refusal stores nothing.
type aiLinksError struct {
	*aiError
	View aiChangeView
}

// Unwrap returns the plain error, so that errors.As finds the *aiError of the refusal.
func (e *aiLinksError) Unwrap() error { return e.aiError }

// aiLandingFiles counts the files of a landing a delete would archive.
type aiLandingFiles struct {
	Count int   `json:"count"`
	Bytes int64 `json:"bytes"`
}

// aiChangeView is the card of one proposal or change as the panel gets it. Times are UTC RFC 3339
// and empty while unset; Diff and Links are never null.
type aiChangeView struct {
	ID                 string          `json:"id"`
	ChatID             string          `json:"chat_id"`
	MessageID          string          `json:"message_id"`
	Resource           string          `json:"resource"`
	Op                 string          `json:"op"`
	EntityID           string          `json:"entity_id"`
	Title              string          `json:"title"`
	Status             string          `json:"status"`
	Diff               []aiDiffEntry   `json:"diff"`
	Links              []aiLinkView    `json:"links"`
	ExternalHostChange bool            `json:"external_host_change"`
	LandingFiles       *aiLandingFiles `json:"landing_files,omitempty"`
	Renumbered         bool            `json:"renumbered,omitempty"`
	Created            string          `json:"created"`
	Applied            string          `json:"applied,omitempty"`
	Reverted           string          `json:"reverted,omitempty"`
	Error              string          `json:"error,omitempty"`
	// ErrorCode and ErrorParams are the stable code and the params of Error, for a client that
	// builds its own text; Error stays the English fallback.
	ErrorCode   string         `json:"error_code,omitempty"`
	ErrorParams map[string]any `json:"error_params,omitempty"`
}

// aiChangeRec is one entry of changes.json: the proposal, the change that grows out of it
// (Change.ID equals Proposal.ID and Change.Status carries every state) and what the card shows.
// Diff, Files and ExternalHost are computed once at proposal time from redacted values.
// Renumbered is set when a revert gave a restored campaign a new number.
type aiChangeRec struct {
	Proposal     aiProposal      `json:"proposal"`
	Change       aiChange        `json:"change"`
	Title        string          `json:"title"`
	Diff         []aiDiffEntry   `json:"diff"`
	Files        *aiLandingFiles `json:"files,omitempty"`
	ExternalHost bool            `json:"external_host,omitempty"`
	Renumbered   bool            `json:"renumbered,omitempty"`
	// ErrorCode and ErrorParams describe Change.Error of an aborted entry (see aiChangeView).
	ErrorCode   string         `json:"error_code,omitempty"`
	ErrorParams map[string]any `json:"error_params,omitempty"`
}

// aiChangesDoc is the content of changes.json.
type aiChangesDoc struct {
	Changes []aiChangeRec `json:"changes"`
}

// aiRevertReport is the result of reverting every change of one reply or of one chat.
// Reverted lists the ids in the order they were rolled back; StoppedAt and Error name the
// change that stopped the run, with ErrorCode and ErrorParams for a client that builds its own
// text; Remaining lists the ids after it that the stopped run did not try, in run order.
type aiRevertReport struct {
	Reverted    []string       `json:"reverted"`
	Remaining   []string       `json:"remaining,omitempty"`
	StoppedAt   string         `json:"stopped_at,omitempty"`
	Error       string         `json:"error,omitempty"`
	ErrorCode   string         `json:"error_code,omitempty"`
	ErrorParams map[string]any `json:"error_params,omitempty"`
}

// aiChgResource describes what the assistant may change on one resource.
// name is the resource of the tool, res the access resource, base the route base of the catalog
// (campaigns use /api/groups), label the JSON key of the grant label, fields the only keys a
// patch may set, and check the strict check of a whole record.
type aiChgResource struct {
	name   string
	res    string
	base   string
	label  string
	fields map[string]bool
	check  func(merged map[string]any) string
}

// aiChgAlwaysForbidden lists the keys no patch may set on any resource: identity, owner, section
// and the number of a campaign.
var aiChgAlwaysForbidden = map[string]bool{"id": true, "created_by": true, "board_id": true, "num": true}

// aiChgFetchRedirect is the redirect mode in which the engine fetches the target on the server and
// sends the visitor's address, cookie data and agent to it. An assistant never sets it.
const aiChgFetchRedirect = "curl"

// aiChgResources is the single table of what an assistant change may touch, by tool resource.
var aiChgResources = map[string]aiChgResource{
	"offers": {
		name: "offers", res: resOffers, base: "/api/offers", label: "group",
		fields: aiFieldSet("name", "url", "status", "local", "network_id", "group", "countries",
			"payout_model", "payout", "payout_currency", "payout_param"),
		check: aiCheckAs(validOffer),
	},
	"landings": {
		name: "landings", res: resLandings, base: "/api/landings", label: "group",
		fields: aiFieldSet("name", "url", "status", "local", "entry", "group"),
		check:  aiCheckAs(validLanding),
	},
	"affiliate_networks": {
		name: "affiliate_networks", res: resNetworks, base: "/api/affiliate-networks", label: "group",
		fields: aiFieldSet("name", "status", "postback_url", "offer_param", "offer_query", "group"),
		check:  aiCheckAs(validNetwork),
	},
	"traffic_sources": {
		name: "traffic_sources", res: resSources, base: "/api/traffic-sources", label: "group",
		fields: aiFieldSet("name", "parameters", "group"),
		check:  aiCheckAs(validSource),
	},
	"campaigns": {
		name: "campaigns", res: resCampaigns, base: "/api/groups", label: "folder",
		fields: aiFieldSet("name", "status", "aliases", "redirect", "header", "out", "geo", "uniq_method",
			"uniq_by", "uniq_cookie", "uniq_seconds", "firewall", "save_keys", "save_keys_se", "weight", "sticky_stream", "domain_id",
			"source_id", "folder", "cost_param", "cost_token", "currency_param", "currency_token", "streams"),
		check: aiCheckAs[config.Group](nil),
	},
}

// aiFieldSet returns the set of names.
func aiFieldSet(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// aiCheckAs returns a record check for the type T: the record must decode into T without an
// unknown key or a value of the wrong type, and then pass valid when it is not nil.
// The check returns "" for an acceptable record, otherwise the client message.
func aiCheckAs[T any](valid func(T) string) func(map[string]any) string {
	return func(m map[string]any) string {
		raw, err := json.Marshal(m)
		if err != nil {
			return "the record cannot be encoded"
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		var v T
		if err := dec.Decode(&v); err != nil {
			return "bad field value: " + err.Error()
		}
		if valid == nil {
			return ""
		}
		return valid(v)
	}
}

// put returns the mux pattern of the replace route of the resource.
func (r aiChgResource) put() string { return "PUT " + r.base + "/{id}" }

// del returns the mux pattern of the delete route of the resource.
func (r aiChgResource) del() string { return "DELETE " + r.base + "/{id}" }

// createPattern returns the mux pattern a create goes through: the upsert PUT for a campaign,
// POST of the collection for a catalog.
func (r aiChgResource) createPattern() string {
	if r.name == "campaigns" {
		return r.put()
	}
	return "POST " + r.base
}

// isCatalog reports whether the resource is a catalog that campaigns and offers link to.
func (r aiChgResource) isCatalog() bool { return r.name != "campaigns" }

// aiChgCampaignDefaults returns the fields a new campaign starts with, as the panel's "new flow" does.
func aiChgCampaignDefaults() map[string]any {
	return map[string]any{
		"status": true, "redirect": "stop", "header": "text/html", "geo": "cf",
		"uniq_method": "ip", "uniq_seconds": json.Number("86400"),
		"firewall": map[string]any{"enabled": false, "queries": json.Number("100"), "seconds": json.Number("60")},
		"streams":  []any{},
	}
}

// aiPlainError returns an aiError with a status and a message that carries no ai_* code.
func aiPlainError(status int, msg string) *aiError {
	return &aiError{status: status, msg: msg}
}

// aiBadPatch returns the 400 ai_bad_patch error with text.
func aiBadPatch(text string) *aiError {
	return newAIError(http.StatusBadRequest, aiErrBadPatch, text)
}

// aiAutoApply decides whether a proposal is applied without the user pressing Apply.
// The proposal is ignored. It always returns false: the first release applies nothing on its
// own. It is the only place a later policy of safe automatic changes is switched on, and a
// caller that wants to apply must go through applyChange after asking it.
func aiAutoApply(_ aiProposal) bool { return false }

// aiChgNewID returns a random 24-character id of a change, a chat or a message.
// It returns a 500 error when the system has no randomness; no id is made then.
func aiChgNewID() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", aiPlainError(http.StatusInternalServerError, "an id cannot be generated")
	}
	return hex.EncodeToString(b[:]), nil
}

// aiChgAsObject returns v as a decoded JSON object, or nil when v is not one.
func aiChgAsObject(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// aiChgObject decodes raw as one JSON object with numbers kept exact.
// It returns an error for anything but an object.
func aiChgObject(raw json.RawMessage) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil || m == nil {
		return nil, errors.New("not a JSON object")
	}
	return m, nil
}

// aiChgClone returns a deep copy of a decoded JSON value, made by encoding and decoding it.
func aiChgClone(v any) any {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var out any
	if dec.Decode(&out) != nil {
		return nil
	}
	return out
}

// aiChgCanon returns the canonical hash of any decoded JSON value, so two equal values compare equal.
func aiChgCanon(v any) string { return rawHash(aiRaw(v)) }

// aiChgValidID checks an entity id that came from a model. It must be 1-128 characters with no
// slash, backslash, question mark, hash or control character: it becomes a path segment.
func aiChgValidID(id string) error {
	if id == "" || len(id) > 128 || strings.ContainsAny(id, "/\\?#") ||
		strings.ContainsFunc(id, func(r rune) bool { return r < ' ' || r == 0x7f }) {
		return aiBadPatch("bad id")
	}
	return nil
}

// aiChgParseArgs decodes the arguments of a propose call: {"id"?, "patch"?}.
// op decides what is required: update needs both, create needs a patch and may name an id,
// delete needs an id and no patch. It returns the id and the patch (nil when absent), or a 400
// ai_bad_patch error for an unknown argument, a bad id or a missing or extra part.
func aiChgParseArgs(op string, raw json.RawMessage) (string, map[string]any, error) {
	if len(raw) > aiChangeArgsMax {
		return "", nil, aiBadPatch("the arguments are too large")
	}
	top := map[string]json.RawMessage{}
	if t := bytes.TrimSpace(raw); len(t) > 0 && !bytes.Equal(t, []byte("null")) {
		if err := json.Unmarshal(t, &top); err != nil {
			return "", nil, aiBadPatch("the arguments are not a JSON object")
		}
	}
	for k := range top {
		if k != "id" && k != "patch" {
			return "", nil, aiBadPatch("unknown argument " + k)
		}
	}
	var id string
	if v, ok := top["id"]; ok {
		if err := json.Unmarshal(v, &id); err != nil {
			return "", nil, aiBadPatch("id must be a string")
		}
		id = strings.TrimSpace(id)
		if id != "" {
			if err := aiChgValidID(id); err != nil {
				return "", nil, err
			}
		}
	}
	var patch map[string]any
	if v, ok := top["patch"]; ok && !bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
		m, err := aiChgObject(v)
		if err != nil {
			return "", nil, aiBadPatch("patch must be an object")
		}
		patch = m
	}
	switch op {
	case aiOpUpdate:
		if id == "" || len(patch) == 0 {
			return "", nil, aiBadPatch("update needs an id and a non-empty patch")
		}
	case aiOpCreate:
		if len(patch) == 0 {
			return "", nil, aiBadPatch("create needs a non-empty patch")
		}
	case aiOpDelete:
		if id == "" {
			return "", nil, aiBadPatch("delete needs an id")
		}
		if len(patch) != 0 {
			return "", nil, aiBadPatch("delete takes no patch")
		}
	default:
		return "", nil, aiBadPatch("unknown operation")
	}
	return id, patch, nil
}

// aiChgCampaignRefusal returns the 400 ai_bad_patch error for a campaign patch v that may not be
// proposed, or nil when it may. It looks at every depth, in sorted key order: a hidden key
// (aiCampaignHidden) is refused (reason hidden_field), and so is the value "curl" in any key called
// "redirect" (the campaign, a stream, its bots), which makes the engine fetch a URL from the server
// (reason fetch_redirect). The params name the field.
func aiChgCampaignRefusal(v any) *aiError {
	switch x := v.(type) {
	case map[string]any:
		for _, k := range slices.Sorted(maps.Keys(x)) {
			if aiCampaignHidden(k) {
				return aiBadPatchR(aiReasonHiddenField, "the key "+k+" cannot be set by the assistant", "field", k)
			}
			if s, ok := x[k].(string); ok && strings.EqualFold(k, "redirect") &&
				strings.EqualFold(strings.TrimSpace(s), aiChgFetchRedirect) {
				return aiBadPatchR(aiReasonFetchRedirect, "the redirect mode "+aiChgFetchRedirect+
					" fetches a page on the server and cannot be set by the assistant", "field", k)
			}
			if bad := aiChgCampaignRefusal(x[k]); bad != nil {
				return bad
			}
		}
	case []any:
		for _, e := range x {
			if bad := aiChgCampaignRefusal(e); bad != nil {
				return bad
			}
		}
	}
	return nil
}

// aiChgCheckPatch checks the keys and the values of a patch against the table of spec: the
// checks of aiChgCheckKeys and of aiChgCheckMarks. It returns nil for an acceptable patch,
// otherwise a 400 ai_bad_patch or ai_redacted_value error.
func aiChgCheckPatch(spec aiChgResource, patch map[string]any) error {
	if err := aiChgCheckKeys(spec, patch); err != nil {
		return err
	}
	return aiChgCheckMarks(patch)
}

// aiChgCheckMarks refuses a patch that carries a redaction mark, a masked address or a truncation
// mark: such a value would overwrite the real one. It returns a 400 ai_redacted_value error whose
// params.field is the first field (in sorted order) that holds one.
func aiChgCheckMarks(patch map[string]any) error {
	for _, k := range slices.Sorted(maps.Keys(patch)) {
		if aiHasRedactMark(patch[k]) {
			return newAIErrorP(http.StatusBadRequest, aiErrRedactedValue,
				"the patch holds a redacted value; send the real value or leave the field out", map[string]any{"field": k})
		}
	}
	return nil
}

// aiChgCheckKeys checks the keys of a patch against the table of spec. It refuses a server-owned
// key, a key outside the white list, and a campaign key or redirect mode that starts an outbound
// request (aiChgCampaignRefusal). It returns a 400 ai_bad_patch error.
func aiChgCheckKeys(spec aiChgResource, patch map[string]any) error {
	if spec.name == "campaigns" {
		if bad := aiChgCampaignRefusal(patch); bad != nil {
			return bad
		}
	}
	for _, k := range slices.Sorted(maps.Keys(patch)) {
		if aiChgAlwaysForbidden[strings.ToLower(k)] {
			return aiBadPatchR(aiReasonServerField, "the field "+k+" is set by the server and cannot be changed", "field", k)
		}
		if !spec.fields[k] {
			return aiBadPatchR(aiReasonNotEditable, "the field "+k+" cannot be edited on "+spec.name, "field", k, "resource", spec.name)
		}
	}
	return nil
}
