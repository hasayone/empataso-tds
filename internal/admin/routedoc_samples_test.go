package admin

import (
	"net/http"
	"slices"
	"strings"
	"testing"
)

// catalogDocTags lists the docs sections whose create, update and delete routes must carry samples.
var catalogDocTags = []string{"campaigns", "offers", "landings", "networks", "sources", "domains"}

// TestCatalogRoutesHaveSamples requires every catalog route of catalogDocTags to document its
// answer, and every create (POST on a collection), PUT and PATCH with a JSON body to carry both
// the body schema and an example. A POST under a record id is an action and may have no body.
func TestCatalogRoutesHaveSamples(t *testing.T) {
	for pattern, doc := range routeDocs {
		verb, muxPath, _ := strings.Cut(pattern, " ")
		if !slices.Contains(catalogDocTags, routeTag(muxPath)) || ruleOf(pattern).cookie {
			continue
		}
		if doc.Response == nil && doc.successStatus() != http.StatusNoContent {
			t.Errorf("%s has no response sample", pattern)
		}
		writes := verb == http.MethodPut || verb == http.MethodPatch || (verb == http.MethodPost && !strings.Contains(muxPath, "{"))
		if writes && !doc.RawBody && (doc.Body == nil || doc.Example == nil) {
			t.Errorf("%s has no body schema or example", pattern)
		}
	}
}
