package config

import "testing"

func TestEnvPrefersNewName(t *testing.T) {
	t.Setenv("EMTDS_ENVTEST_DIR", "/new")
	t.Setenv("KUZTDS_ENVTEST_DIR", "/old")
	if got := Env("EMTDS_ENVTEST_DIR"); got != "/new" {
		t.Fatalf("Env = %q, want /new", got)
	}
}

func TestEnvFallsBackToLegacyName(t *testing.T) {
	t.Setenv("KUZTDS_ENVTEST_DIR", "/old")
	if got := Env("EMTDS_ENVTEST_DIR"); got != "/old" {
		t.Fatalf("Env unset new = %q, want /old", got)
	}
	t.Setenv("EMTDS_ENVTEST_DIR", "")
	if got := Env("EMTDS_ENVTEST_DIR"); got != "/old" {
		t.Fatalf("Env empty new = %q, want /old", got)
	}
}

func TestEnvOtherKeysHaveNoFallback(t *testing.T) {
	t.Setenv("KUZTDS_ENVTEST_DIR", "/old")
	if got := Env("ENVTEST_DIR"); got != "" {
		t.Fatalf("Env without prefix = %q, want empty", got)
	}
	if got := Env("KUZTDS_ENVTEST_DIR"); got != "/old" {
		t.Fatalf("Env legacy key = %q, want /old", got)
	}
}

func TestEnvOr(t *testing.T) {
	if got := EnvOr("EMTDS_ENVTEST_MISSING", "def"); got != "def" {
		t.Fatalf("EnvOr = %q, want def", got)
	}
	t.Setenv("KUZTDS_ENVTEST_MISSING", "old")
	if got := EnvOr("EMTDS_ENVTEST_MISSING", "def"); got != "old" {
		t.Fatalf("EnvOr legacy = %q, want old", got)
	}
}
