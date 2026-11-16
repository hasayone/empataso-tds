package admin

import (
	"testing"

	"github.com/egerkuzma/emtds/internal/config"
	"github.com/emtds/bridge/ktimport"
)

func TestKeitaroFlagRulesReachConfig(t *testing.T) {
	in := ktimport.Rules{Bot: 1, HasReferer: 2, Proxy: 2, Unique: 1, IPVersion: 1}
	got, err := convert[config.Rules](in)
	if err != nil {
		t.Fatal(err)
	}
	if got.Bot != config.FlagA || got.HasReferer != config.FlagB || got.Proxy != config.FlagB || got.Unique != config.FlagA || got.IPVersion != config.FlagA {
		t.Fatalf("config rules = %+v", got)
	}
	back, err := convert[ktimport.Rules](got)
	if err != nil {
		t.Fatal(err)
	}
	if back.Bot != in.Bot || back.HasReferer != in.HasReferer || back.Proxy != in.Proxy || back.Unique != in.Unique || back.IPVersion != in.IPVersion {
		t.Fatalf("round trip = %+v, want %+v", back, in)
	}
}
