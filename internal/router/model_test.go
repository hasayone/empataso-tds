package router

import (
	"testing"

	"github.com/hasayone/empataso-tds/internal/config"
)

func TestSelect_Model(t *testing.T) {
	v := baseVisitor()
	v.Model = "SM-S918B"
	pick := func(flag config.Flag, vv Visitor, models ...string) bool {
		g := &config.Group{Streams: []config.Stream{stream("s", config.Rules{Model: list(flag, "", models...)})}}
		_, ok := Select(g, vv, Deps{})
		return ok
	}
	if !pick(config.FlagB, v, "sm-s918b", "Pixel 8") {
		t.Error("whitelist with the visitor model must let through, case-insensitive")
	}
	if pick(config.FlagB, v, "Pixel 8") {
		t.Error("whitelist without the visitor model must cut")
	}
	if pick(config.FlagA, v, "SM-S918B") {
		t.Error("blacklist with the visitor model must cut")
	}
	if !pick(config.FlagA, v, "Pixel 8") {
		t.Error("blacklist of another model must let through")
	}
	if pick(config.FlagB, v, "SM-S918") {
		t.Error("the match is exact, a prefix must not pass")
	}
	v.Model = ""
	if pick(config.FlagB, v, "SM-S918B") {
		t.Error("an unknown model never matches a whitelist")
	}
	if !pick(config.FlagA, v, "SM-S918B") {
		t.Error("an unknown model is not on a blacklist")
	}
}

func TestWhy_ModelNamedAndOr(t *testing.T) {
	v := baseVisitor()
	v.Model = "iPhone"
	s := stream("s", config.Rules{Model: list(config.FlagB, "", "Pixel 8")})
	if got := Why(&s, v, Deps{}); got != "model" {
		t.Errorf("Why = %q, want model", got)
	}
	s.FilterOr = true
	if got := Why(&s, v, Deps{}); got != "model" {
		t.Errorf("FilterOr Why = %q, want model", got)
	}
	s.Rules.Model = list(config.FlagB, "", "iPhone")
	if got := Why(&s, v, Deps{}); got != "" {
		t.Errorf("matching model Why = %q, want empty", got)
	}
}
