package admin

import (
	"net/http"
	"strings"
	"testing"
)

func TestCampaignSourceSwitchRefusedForHiddenQueryName(t *testing.T) {
	rig := newRolesRig(t, "")
	rig.seedSource(t, "alias", map[string]string{"sub_id_1": "sub_id_3"})
	rig.seedSource(t, "alias2", map[string]string{"sub_id_1": "sub_id_3", "site": "x"})
	rig.seedSource(t, "kw", map[string]string{"sub_id_2": "q"})
	rig.seedSource(t, "clean", map[string]string{"sub_id_1": "p1"})
	rig.seedSource(t, "plain", map[string]string{})
	base := plainCampaign("")

	for _, c := range []struct {
		name   string
		hidden []string
		from   string
		to     string
		want   int
	}{
		{"sub_id_1 reads the plain name of hidden sub_id_3", []string{"sub_id_3"}, "", "alias", http.StatusBadRequest},
		{"from another source", []string{"sub_id_3"}, "clean", "alias", http.StatusBadRequest},
		{"hidden keyword read through q", []string{"keyword"}, "", "kw", http.StatusBadRequest},
		{"a source without the collision", []string{"sub_id_3"}, "", "clean", http.StatusOK},
		{"a source with no mapping", []string{"sub_id_3"}, "", "plain", http.StatusOK},
		{"between two sources that both collide", []string{"sub_id_3"}, "alias", "alias2", http.StatusOK},
		{"back from the colliding source", []string{"sub_id_3"}, "alias", "clean", http.StatusOK},
		{"the hidden field is another one", []string{"sub_id_4"}, "", "alias", http.StatusOK},
		{"a role without the query field hidden", []string{"ip"}, "", "alias", http.StatusOK},
		{"nothing hidden", nil, "", "alias", http.StatusOK},
	} {
		g := base
		g.SourceID = c.from
		rig.resetCampaign(g)
		rig.setRole(t, editorAccess(c.hidden...))
		next := g
		next.SourceID = c.to
		if got := rig.putCampaign(t, next); got != c.want {
			t.Errorf("%s: %d, want %d", c.name, got, c.want)
		}
	}

	rig.setRole(t, editorAccess("sub_id_3"))
	stored := base
	stored.SourceID = "alias"
	rig.resetCampaign(stored)
	renamed := stored
	renamed.Name = "renamed"
	if got := rig.putCampaign(t, renamed); got != http.StatusOK {
		t.Errorf("a campaign saved on the source stays savable: %d", got)
	}

	rig.resetCampaign(base)
	next := base
	next.SourceID = "alias"
	if code, raw := rig.api(t, rig.admin, rig.csrf, http.MethodPut, "/api/groups/c1", next); code != http.StatusOK {
		t.Errorf("administrator: %d %s", code, raw)
	}
}

func TestCampaignSourceSwitchRefusalDoesNotNameTheHiddenField(t *testing.T) {
	rig := newRolesRig(t, "")
	rig.seedSource(t, "alias", map[string]string{"sub_id_1": "sub_id_3"})
	rig.resetCampaign(plainCampaign(""))
	rig.setRole(t, editorAccess("sub_id_3"))
	next := plainCampaign("")
	next.SourceID = "alias"
	code, raw := rig.api(t, rig.user, rig.ucsrf, http.MethodPut, "/api/groups/c1", next)
	if code != http.StatusBadRequest || !strings.Contains(string(raw), "source maps sub_id_1, a hidden field") {
		t.Fatalf("%d %s", code, raw)
	}
	if strings.Contains(string(raw), "sub_id_3") {
		t.Errorf("the answer names the hidden field: %s", raw)
	}
}

func TestSourceCheckReadsCampaignsOnlyOnChangedParameters(t *testing.T) {
	rig := newRolesRig(t, "")
	rig.resetCampaign(priceCampaign(""))
	rig.seedSource(t, "s1", map[string]string{"sub_id_1": "p1"})
	groups := rig.storedGroups()

	// lists runs one PUT of the source and returns how many campaign reads it made.
	lists := func(c *http.Client, csrf string, params map[string]string) int32 {
		before := groups.lists.Load()
		if code, body := rig.putSourceAs(t, c, csrf, "s1", params); code != http.StatusOK {
			t.Fatalf("put %v: %d %s", params, code, body)
		}
		return groups.lists.Load() - before
	}
	unchanged := map[string]string{"sub_id_1": "p1"}
	base := lists(rig.admin, rig.csrf, unchanged)

	rig.setRole(t, editorAccess("cost"))
	if got := lists(rig.user, rig.ucsrf, map[string]string{"sub_id_1": " p1 "}); got != base {
		t.Errorf("a re-save with the same parameters read the campaigns %d times, a plain save reads %d", got, base)
	}
	if got := lists(rig.user, rig.ucsrf, map[string]string{"sub_id_1": "p2"}); got <= base {
		t.Errorf("a changed parameter did not read the campaigns for the price names: %d, a plain save reads %d", got, base)
	}

	rig.setRole(t, editorAccess("keyword"))
	if got := lists(rig.user, rig.ucsrf, map[string]string{"sub_id_1": "p3"}); got != base {
		t.Errorf("with the price visible the campaigns are not needed: %d reads, a plain save reads %d", got, base)
	}
}

func TestSourceCheckKeepsStoredRead(t *testing.T) {
	rig := newRolesRig(t, "")
	rig.seedSource(t, "s1", map[string]string{"sub_id_1": "sub_id_3"})
	rig.setRole(t, editorAccess("sub_id_3"))
	if code, body := rig.putSourceAs(t, rig.user, rig.ucsrf, "s1", map[string]string{"sub_id_1": "sub_id_3"}); code != http.StatusOK {
		t.Errorf("the stored binding as it is: %d %s", code, body)
	}
	if code, body := rig.putSourceAs(t, rig.user, rig.ucsrf, "s1", map[string]string{"sub_id_1": "sub_id_3", "site": "s"}); code != http.StatusOK {
		t.Errorf("an unrelated field next to the stored binding: %d %s", code, body)
	}
	if code, body := rig.putSourceAs(t, rig.user, rig.ucsrf, "s1", map[string]string{"sub_id_1": "sub_id_3", "sub_id_2": "sub_id_3"}); code != http.StatusBadRequest {
		t.Errorf("a second field on the hidden name: %d %s", code, body)
	}
}
