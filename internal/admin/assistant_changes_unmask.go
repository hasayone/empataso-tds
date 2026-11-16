package admin

import (
	"maps"
	"sort"
	"strings"
)

// aiChgUnmask returns a copy of patch in which every value the model got in a redacted form and
// sent back unchanged is replaced by the stored one.
//
// patch is the decoded patch, shown the record as the model saw it (aiChgShow of before) and raw
// the stored record; the three are walked together. A string of the patch that holds a mark
// (aiHasRedactMark) and equals the string of shown at the same path takes the string of raw. The
// streams of a campaign are matched by name (a renamed one by its position, see aiChgMatchList),
// every other array by position and every object by key. A value that has no counterpart, or differs from what the model saw, keeps its mark, so a new
// masked value is still refused by aiChgCheckMarks. The stored values live only in the result and
// are never shown: a card is built from shown.
func aiChgUnmask(patch, shown, raw any) any {
	switch p := patch.(type) {
	case string:
		sh, okShown := shown.(string)
		rw, okRaw := raw.(string)
		if okShown && okRaw && sh == p && aiHasRedactMark(p) {
			return rw
		}
		return p
	case map[string]any:
		shm, _ := shown.(map[string]any)
		rwm, _ := raw.(map[string]any)
		out := make(map[string]any, len(p))
		for k, v := range p {
			out[k] = aiChgUnmask(v, shm[k], rwm[k])
		}
		return out
	case []any:
		shl, _ := shown.([]any)
		rwl, _ := raw.([]any)
		at := aiChgMatchList(p, shl)
		out := make([]any, len(p))
		for i, v := range p {
			var sv, rv any
			if j := at[i]; j >= 0 && j < len(rwl) {
				sv, rv = shl[j], rwl[j]
			}
			out[i] = aiChgUnmask(v, sv, rv)
		}
		return out
	}
	return patch
}

// aiChgMatchList returns, for each element of patch, the index of the element of shown it was
// copied from, or -1. A list whose elements all have a name (the streams of a campaign) is matched
// by name; an element whose name is new, a stream that was renamed, takes the element of shown at
// its own position when no other element of patch claimed that one by name. Any other list is
// matched by position.
func aiChgMatchList(patch, shown []any) []int {
	at := make([]int, len(patch))
	if !aiChgNamed(shown) {
		for i := range patch {
			at[i] = -1
			if i < len(shown) {
				at[i] = i
			}
		}
		return at
	}
	claimed := map[int]bool{}
	for i, v := range patch {
		at[i] = -1
		if m, ok := v.(map[string]any); ok {
			if name, ok := m["name"].(string); ok {
				for j, e := range shown {
					if e.(map[string]any)["name"] == name {
						at[i] = j
						claimed[j] = true
						break
					}
				}
			}
		}
	}
	for i, v := range patch {
		if _, isObject := v.(map[string]any); isObject && at[i] < 0 && i < len(shown) && !claimed[i] {
			at[i] = i
		}
	}
	return at
}

// aiChgNamed reports whether every element of list is an object with a string name: a list of
// streams, matched by name and not by position.
func aiChgNamed(list []any) bool {
	if len(list) == 0 {
		return false
	}
	for _, e := range list {
		m, ok := e.(map[string]any)
		if !ok {
			return false
		}
		if _, ok := m["name"].(string); !ok {
			return false
		}
	}
	return true
}

// aiChgMerge builds the record a write sends from the stored one and the patch.
// update starts from before and replaces the patched keys; create starts from the defaults of the
// resource (campaigns) or an empty record. id, when not empty, is set on the record. A campaign
// update that replaces streams keeps the hidden settings (CURL strings, [REMOTE]) the stored stream
// of the same name holds: the model never saw them, so a replaced list must not drop them.
// before and patch are not modified; the result is a new map.
func aiChgMerge(spec aiChgResource, op, id string, before, patch map[string]any) map[string]any {
	var m map[string]any
	switch {
	case op == aiOpUpdate:
		m = maps.Clone(before)
	case spec.name == "campaigns":
		m = aiChgCampaignDefaults()
	default:
		m = map[string]any{}
	}
	for k, v := range patch {
		m[k] = aiChgClone(v)
	}
	if id != "" {
		m["id"] = id
	}
	if op == aiOpUpdate && spec.name == "campaigns" {
		if _, set := patch["streams"]; set {
			aiChgKeepStreamSecrets(m, before)
		}
	}
	return m
}

// aiChgKeepStreamSecrets copies the hidden keys (curl, b_curl, remote; see aiCampaignHidden) from
// the streams of before into the streams of m that have the same name and no such key.
// m and before are decoded campaigns; m is changed.
func aiChgKeepStreamSecrets(m, before map[string]any) {
	oldByName := aiChgStreamsByName(before)
	news, _ := m["streams"].([]any)
	for _, e := range news {
		nm, ok := e.(map[string]any)
		if !ok {
			continue
		}
		name, _ := nm["name"].(string)
		for k, v := range oldByName[name] {
			if !aiCampaignHidden(k) {
				continue
			}
			if _, set := nm[k]; !set {
				nm[k] = aiChgClone(v)
			}
		}
	}
}

// aiChgStreamsByName indexes the streams of a decoded campaign by name. A name that two streams
// share points at the first. A stream that is not an object is skipped.
func aiChgStreamsByName(campaign map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	list, _ := campaign["streams"].([]any)
	for _, e := range list {
		if sm, ok := e.(map[string]any); ok {
			name, _ := sm["name"].(string)
			if _, dup := out[name]; !dup {
				out[name] = sm
			}
		}
	}
	return out
}

// aiChgHasContent reports whether a decoded JSON value holds something: not null, not an empty
// string, object or array.
func aiChgHasContent(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case string:
		return x != ""
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return true
}

// aiChgCheckStreamsKept refuses a streams patch that would silently lose hidden settings.
// before is the decoded stored campaign, patchStreams the "streams" value of the patch and red
// the redactor for the names in the message. Every stored stream that holds a hidden key with a
// value (curl, b_curl, remote) must appear in the patch under the same name, because the model
// never saw those settings and cannot send them back. It returns 400 ai_bad_patch naming the
// first stream that is missing or was renamed.
func aiChgCheckStreamsKept(before map[string]any, patchStreams any, red *aiRedactor) error {
	kept := map[string]bool{}
	list, _ := patchStreams.([]any)
	for _, e := range list {
		if sm, ok := e.(map[string]any); ok {
			name, _ := sm["name"].(string)
			kept[name] = true
		}
	}
	olds, _ := before["streams"].([]any)
	for _, e := range olds {
		om, ok := e.(map[string]any)
		if !ok {
			continue
		}
		name, _ := om["name"].(string)
		if kept[name] {
			continue
		}
		var hidden []string
		for k, v := range om {
			if aiCampaignHidden(k) && aiChgHasContent(v) {
				hidden = append(hidden, k)
			}
		}
		if len(hidden) == 0 {
			continue
		}
		sort.Strings(hidden)
		return aiBadPatchR(aiReasonStreamDropped, "the stream "+red.text(name)+" holds settings the assistant cannot see ("+
			strings.Join(hidden, ", ")+"); keep it in streams under the same name", "stream", red.text(name), "fields", hidden)
	}
	return nil
}
