package admin

import (
	"testing"

	"github.com/egerkuzma/kuztds/internal/security"
)

func TestViewerMode(t *testing.T) {
	admin := viewer{admin: true}
	if admin.mode(resOffers) != security.ModeFull {
		t.Fatal("admin mode")
	}
	noGrant := viewer{grants: map[string]security.Grant{}}
	if noGrant.mode(resOffers) != security.ModeRead {
		t.Fatal("missing grant -> read")
	}
	sel := viewer{grants: map[string]security.Grant{
		resOffers: {Mode: security.ModeSelected, IDs: []string{"a"}},
	}}
	if sel.mode(resOffers) != security.ModeSelected {
		t.Fatal("selected mode")
	}
}

func TestCanSeeBranches(t *testing.T) {
	r := ref{id: "1", label: "L", owner: "u1"}
	admin := viewer{admin: true}
	if !admin.canSee(resOffers, r) || !admin.canWrite(resOffers, r) || !admin.canCreate(resOffers) {
		t.Fatal("admin see/write/create")
	}
	read := viewer{grants: map[string]security.Grant{resOffers: {Mode: security.ModeRead}}}
	if !read.canSee(resOffers, r) || read.canWrite(resOffers, r) || read.canCreate(resOffers) {
		t.Fatal("read only")
	}
	full := viewer{grants: map[string]security.Grant{resOffers: {Mode: security.ModeFull}}}
	if !full.canSee(resOffers, r) || !full.canWrite(resOffers, r) || !full.canCreate(resOffers) {
		t.Fatal("full")
	}
	sel := viewer{grants: map[string]security.Grant{resOffers: {Mode: security.ModeSelected, IDs: []string{"1"}}}}
	if !sel.canSee(resOffers, r) || !sel.canWrite(resOffers, r) {
		t.Fatal("selected by id")
	}
	sel2 := viewer{grants: map[string]security.Grant{resOffers: {Mode: security.ModeSelected, Labels: []string{"L"}}}}
	if !sel2.canSee(resOffers, r) {
		t.Fatal("selected by label")
	}
	selEmpty := viewer{grants: map[string]security.Grant{resOffers: {Mode: security.ModeSelected}}}
	if selEmpty.canSee(resOffers, r) {
		t.Fatal("selected empty lists see nothing")
	}
	created := viewer{uid: "u1", grants: map[string]security.Grant{resOffers: {Mode: security.ModeCreated}}}
	if !created.canSee(resOffers, r) || !created.canWrite(resOffers, r) || !created.canCreate(resOffers) {
		t.Fatal("created own")
	}
	other := viewer{uid: "u2", grants: map[string]security.Grant{resOffers: {Mode: security.ModeCreated}}}
	if other.canSee(resOffers, r) || other.canWrite(resOffers, r) {
		t.Fatal("created other uid")
	}
	createdExtra := viewer{uid: "u2", grants: map[string]security.Grant{resOffers: {Mode: security.ModeCreated, IDs: []string{"1"}}}}
	if createdExtra.canSee(resOffers, r) || createdExtra.canWrite(resOffers, r) {
		t.Fatal("created: an id list opens nothing, only own records")
	}
	createdLabel := viewer{uid: "u2", grants: map[string]security.Grant{resOffers: {Mode: security.ModeCreated, Labels: []string{"L"}}}}
	if createdLabel.canSee(resOffers, r) {
		t.Fatal("created: a label list opens nothing")
	}
	emptyLabel := viewer{grants: map[string]security.Grant{resOffers: {Mode: security.ModeSelected, Labels: []string{""}}}}
	if emptyLabel.canSee(resOffers, ref{id: "x", label: ""}) {
		t.Fatal("empty label must not match empty labels entry")
	}
}
