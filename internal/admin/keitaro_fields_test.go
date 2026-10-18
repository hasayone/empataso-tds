package admin

import (
	"testing"

	"github.com/egerkuzma/kuztds/internal/config"
)

func TestKeepLocalFieldsDomain(t *testing.T) {
	old := config.Domain{
		ID: "1", Host: "a", Group: "g", Disabled: true,
		OK: true, Reason: "", Checked: "t", CreatedBy: "u",
	}
	next := config.Domain{ID: "1", Host: "a", Role: "links"}
	got := keepLocalFields(old, next)
	if !got.Disabled || !got.OK || got.Checked != "t" || got.Group != "g" || got.CreatedBy != "u" {
		t.Fatalf("got = %+v", got)
	}
}
