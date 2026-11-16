package ktimport

import "testing"

func TestEnvFallsBackToLegacyName(t *testing.T) {
	t.Setenv("KUZTDS_KTENV_URL", "http://old")
	if got := Env("EMTDS_KTENV_URL"); got != "http://old" {
		t.Fatalf("Env = %q, want legacy value", got)
	}
	t.Setenv("EMTDS_KTENV_URL", "http://new")
	if got := Env("EMTDS_KTENV_URL"); got != "http://new" {
		t.Fatalf("Env = %q, want new value", got)
	}
}
