package admin

import (
	"context"
	"net/http"

	"github.com/hasayone/empataso-tds/internal/security"
)

// Levels of what the assistant may do on one resource. Each level includes the ones before it:
// read lists and opens records, edit also proposes new records and changes, delete also proposes
// deletes.
const (
	aiPermNone   = "none"
	aiPermRead   = "read"
	aiPermEdit   = "edit"
	aiPermDelete = "delete"
)

// Presets of the assistant permissions. Full gives every resource its highest level, so the
// assistant may do what the user who leads the chat may do; custom takes the level of each
// resource from aiPerms.Resources.
const (
	aiPresetRead   = "read"
	aiPresetEdit   = "edit"
	aiPresetFull   = "full"
	aiPresetCustom = "custom"
)

// aiErrForbidden is the code of a tool call or an apply the assistant permissions or the access of
// the user do not allow. Its params are resource and op ("" for a read).
const aiErrForbidden = "ai_forbidden"

// aiPermRank orders the levels.
var aiPermRank = map[string]int{aiPermNone: 0, aiPermRead: 1, aiPermEdit: 2, aiPermDelete: 3}

// aiPermResource is one resource the permissions name and the highest level it can take.
type aiPermResource struct {
	Name string `json:"name"`
	Max  string `json:"max"`
}

// aiPermResources lists the resources of the permissions in the order the panel shows them.
// Domains stop at read: the assistant has no domain write tools.
var aiPermResources = []aiPermResource{
	{"campaigns", aiPermDelete}, {"offers", aiPermDelete}, {"landings", aiPermDelete},
	{"affiliate_networks", aiPermDelete}, {"traffic_sources", aiPermDelete}, {"domains", aiPermRead},
}

// aiPerms is what the administrator lets the assistant do. Preset is one of the aiPreset values;
// Resources maps a resource of aiPermResources to its level and is kept only for the custom preset.
type aiPerms struct {
	Preset    string            `json:"preset"`
	Resources map[string]string `json:"resources,omitempty"`
}

// aiPermMax returns the highest level of res and whether res is one of aiPermResources.
func aiPermMax(res string) (string, bool) {
	for _, r := range aiPermResources {
		if r.Name == res {
			return r.Max, true
		}
	}
	return "", false
}

// aiPermLower returns the lower of the levels a and b.
func aiPermLower(a, b string) string {
	if aiPermRank[a] < aiPermRank[b] {
		return a
	}
	return b
}

// aiNormalizePerms makes stored permissions safe: an unknown or empty preset becomes edit, the
// default of the beta. A custom preset keeps only the resources of aiPermResources, a missing or
// unknown level becomes read and a level above the highest one of its resource is cut to it.
// Other presets drop Resources.
func aiNormalizePerms(p aiPerms) aiPerms {
	switch p.Preset {
	case aiPresetRead, aiPresetEdit, aiPresetFull:
		return aiPerms{Preset: p.Preset}
	case aiPresetCustom:
	default:
		return aiPerms{Preset: aiPresetEdit}
	}
	out := aiPerms{Preset: aiPresetCustom, Resources: make(map[string]string, len(aiPermResources))}
	for _, r := range aiPermResources {
		lv, ok := p.Resources[r.Name]
		if _, known := aiPermRank[lv]; !ok || !known {
			lv = aiPermRead
		}
		out.Resources[r.Name] = aiPermLower(lv, r.Max)
	}
	return out
}

// aiCleanPerms checks the permissions of a PUT body and returns them normalized. The preset must be
// known; a custom preset may name only resources of aiPermResources, each with a known level not
// above the highest one of the resource, and a resource it leaves out gets read. Anything else is
// ai_bad_request with field perms and reason invalid.
func aiCleanPerms(p aiPerms) (aiPerms, error) {
	bad := aiBadRequest("perms", aiReasonInvalid, "perms hold an unknown preset, resource or level")
	switch p.Preset {
	case aiPresetRead, aiPresetEdit, aiPresetFull:
		return aiPerms{Preset: p.Preset}, nil
	case aiPresetCustom:
	default:
		return aiPerms{}, bad
	}
	for res, lv := range p.Resources {
		max, ok := aiPermMax(res)
		rank, known := aiPermRank[lv]
		if !ok || !known || rank > aiPermRank[max] {
			return aiPerms{}, bad
		}
	}
	return aiNormalizePerms(p), nil
}

// level returns the level of res under p: the level of the preset, or of Resources for custom,
// never above the highest one of the resource. A resource outside aiPermResources has none.
func (p aiPerms) level(res string) string {
	max, ok := aiPermMax(res)
	if !ok {
		return aiPermNone
	}
	lv := aiPermEdit
	switch p.Preset {
	case aiPresetRead:
		lv = aiPermRead
	case aiPresetFull:
		lv = aiPermDelete
	case aiPresetCustom:
		lv = p.Resources[res]
	}
	return aiPermLower(lv, max)
}

// levels returns the level of every resource of aiPermResources under p.
func (p aiPerms) levels() map[string]string {
	out := make(map[string]string, len(aiPermResources))
	for _, r := range aiPermResources {
		out[r.Name] = p.level(r.Name)
	}
	return out
}

// allows reports whether p lets the assistant use a tool on resource res with operation op: ""
// for a read, or create, update or delete. A read of an area outside aiPermResources (statistics,
// clicks, reports) is always allowed, as the access of the user still applies to it; a write there
// never is.
func (p aiPerms) allows(res, op string) bool {
	need := map[string]string{"": aiPermRead, aiOpCreate: aiPermEdit, aiOpUpdate: aiPermEdit, aiOpDelete: aiPermDelete}[op]
	if need == "" {
		return false
	}
	if _, ok := aiPermMax(res); !ok {
		return op == ""
	}
	return aiPermRank[p.level(res)] >= aiPermRank[need]
}

// aiForbiddenErr is the 403 ai_forbidden error of operation op ("" for a read) on resource res.
func aiForbiddenErr(res, op string) *aiError {
	return newAIErrorP(http.StatusForbidden, aiErrForbidden,
		"the assistant may not do this: "+joinNonEmpty(" ", op, res), map[string]any{"resource": res, "op": op})
}

// aiUserMayOp reports whether the access v of the user who leads the chat could use tool t on the
// section board: a read always (aiTools already checked the section), a create only with the right
// to create in the resource, an update or a delete only without read-only access to it, and no
// write where the member role of the section is read. It does not return an error.
func aiUserMayOp(v viewer, t aiTool, board string) bool {
	if t.Op == "" {
		return true
	}
	spec, ok := aiChgResources[t.Resource]
	if !ok || !v.boardRoleWrite(spec.res, ref{board: board}) {
		return false
	}
	if t.Op == aiOpCreate {
		return v.canCreate(spec.res)
	}
	return v.mode(spec.res) != security.ModeRead
}

// aiToolsFor returns the tools of a turn of sess on the section board: the tools of aiTools that
// the assistant permissions p allow and that the access of the user (read from ctx) could use, so
// the assistant never gets more than the person. The result is sorted by name.
func (s *Server) aiToolsFor(ctx context.Context, sess security.Session, board string, p aiPerms) []aiTool {
	v := s.viewerFrom(ctx)
	var out []aiTool
	for _, t := range s.aiTools(sess) {
		if p.allows(t.Resource, t.Op) && aiUserMayOp(v, t, board) {
			out = append(out, t)
		}
	}
	return out
}

// aiPermCheck refuses an apply of operation op on resource res that the stored assistant
// permissions no longer allow: the settings may have changed since the proposal. It returns the
// 403 ai_forbidden error, or nil. A revert is never checked: undoing a change is the right of the
// person.
func (s *Server) aiPermCheck(res, op string) error {
	st, _ := s.aiLoad()
	if !st.Perms.allows(res, op) {
		return aiForbiddenErr(res, op)
	}
	return nil
}
