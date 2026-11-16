package admin

import (
	"strings"
	"testing"
)

// checkerNameCases are real campaign names with the keyword and the bundle id read from them.
var checkerNameCases = []struct {
	name, keyword, bundle string
}{
	{"❌ SpinDog - Play Cobra - com.playcobra.fasterthink", "SpinDog", "com.playcobra.fasterthink"},
	{"✅ Swiss Casinos  - Win Dash - com.dashcars.neon - #MR BIT - #SWISS CASINOS", "Swiss Casinos", "com.dashcars.neon"},
	{"❄️ BravoPlay - Win Score - com.winkrjuac.bravoplayjichsw", "BravoPlay", "com.winkrjuac.bravoplayjichsw"},
	{"❌ Betclic - Digit Twins (Digit Twins) - com.findgamestore.findpairapp", "Betclic", "com.findgamestore.findpairapp"},
	{"❌ BetFury - Fortune Myth -  com.fortunemyth.truthquiz", "BetFury", "com.fortunemyth.truthquiz"},
	{"❌ Casino Baden-Baden - Win Chik - com.chater.winchik", "Casino Baden-Baden", "com.chater.winchik"},
	{"❄️ Lolajack - com.jmerbvlolajack.lolajackwefbpy - Developer : @V Killa", "Lolajack", "com.jmerbvlolajack.lolajackwefbpy"},
	{"❌ ! Hommerson - Win Quest - com.winfebsoe.hommersonrggfsu", "Hommerson", "com.winfebsoe.hommersonrggfsu"},
	{"❌  Bet365 -Planetar Win(Planetar Win) - com.dodgecom.planetarwin", "Bet365", "com.dodgecom.planetarwin"},
	{"❌XliveBet - com.ejsxqxxlivebet.xlivebetmfsbvt - Developer : @RAGNAR NAVR", "XliveBet", "com.ejsxqxxlivebet.xlivebetmfsbvt"},
	{"⛓️ Wild Spin - Lucky Run - com.luckyrun.wildspin", "Wild Spin", "com.luckyrun.wildspin"},
	{"✅❄️ Mega Bet - Go Win - com.gowin.megabet", "Mega Bet", "com.gowin.megabet"},
	{"❄️ Win", "", ""},
	{"❌ Num Blast! (Num Blast!) - com.bapp.application2003_1 - #7", "Num Blast! (Num Blast!)", "com.bapp.application2003_1"},
	{"⚡️ White - Glowline - com.arcvector.glowlinedash", "White", "com.arcvector.glowlinedash"},
	{"❄️ 10Cric - Win Court - com.wincctftq.k10crictunard", "10Cric", "com.wincctftq.k10crictunard"},
	{"❄️ Me88 - Win - com.au04l8.xsy92ohbo", "Me88", "com.au04l8.xsy92ohbo"},
}

func TestCheckerNameParsing(t *testing.T) {
	for _, c := range checkerNameCases {
		if got := checkerKeywordFromName(c.name); got != c.keyword {
			t.Errorf("keyword of %q = %q, want %q", c.name, got, c.keyword)
		}
		if got := checkerBundleFromName(c.name); got != c.bundle {
			t.Errorf("bundle of %q = %q, want %q", c.name, got, c.bundle)
		}
	}
}

func TestCheckerKeywordFromNameRejects(t *testing.T) {
	for name, why := range map[string]string{
		"com.example.app - Something":     "a bundle id is no keyword",
		"https://example.com/x - Some":    "an address is no keyword",
		"HTTPcasino - Some":               "starts with http",
		"Bet:ter - Some - com.a.b":        "a colon",
		"":                                "empty name",
		"❌ ❄️ - com.a.b":                  "nothing before the separator",
		strings.Repeat("a", 101) + " - x": "over 100 runes",
	} {
		if got := checkerKeywordFromName(name); got != "" {
			t.Errorf("%s: %q gave %q", why, name, got)
		}
	}
	if got := checkerKeywordFromName(strings.Repeat("я", 100) + " - x"); got == "" {
		t.Error("100 runes must be allowed")
	}
}

func TestCheckerBundleFromNameSkipsAddresses(t *testing.T) {
	if got := checkerBundleFromName("A - https://play.google.com/store.apps - B"); got != "" {
		t.Errorf("address taken as bundle: %q", got)
	}
	if got := checkerBundleFromName("A - Com.Mixed.Case"); got != "com.mixed.case" {
		t.Errorf("bundle must be lower case: %q", got)
	}
	if got := checkerBundleFromName("A - com.first.one - com.last.one"); got != "com.last.one" {
		t.Errorf("the last token wins: %q", got)
	}
}

func TestCheckerFieldProblems(t *testing.T) {
	for _, kw := range []string{"a:b", "a\nb", "a\rb", strings.Repeat("x", 101)} {
		if checkerKeywordProblem(kw) == "" {
			t.Errorf("keyword %q accepted", kw)
		}
	}
	for _, kw := range []string{"", "Betsson", "Casino Baden-Baden", strings.Repeat("я", 100)} {
		if msg := checkerKeywordProblem(kw); msg != "" {
			t.Errorf("keyword %q refused: %s", kw, msg)
		}
	}
	for _, b := range []string{"com.bapp.application2003_1", "nodot", "Com.Upper.Case", "com.a b", strings.Repeat("a.", 130)} {
		if checkerBundleProblem(b) == "" {
			t.Errorf("bundle %q accepted", b)
		}
	}
	if msg := checkerBundleProblem("com.playcobra.fasterthink"); msg != "" {
		t.Errorf("good bundle refused: %s", msg)
	}
	if got := checkerAppName(11381, "SpinDog"); got != "11381 - SpinDog" {
		t.Errorf("name = %q", got)
	}
	if got := checkerAppName(0, "SpinDog"); got != "SpinDog" {
		t.Errorf("name without a number = %q", got)
	}
}
