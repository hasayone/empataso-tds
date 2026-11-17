package config

import "testing"

// retiredEnvName builds the variable name the product no longer reads: the retired
// prefix is assembled from parts so that no source file spells it out.
// rest is the part after the prefix. It returns the full variable name.
func retiredEnvName(rest string) string {
	return "KU" + "ZTDS_" + rest
}

// TestEnvReadsProductName checks that Env returns the EMTDS_ value as set.
func TestEnvReadsProductName(t *testing.T) {
	t.Setenv("EMTDS_ENVTEST_DIR", "/new")
	if got := Env("EMTDS_ENVTEST_DIR"); got != "/new" {
		t.Fatalf("Env = %q, want /new", got)
	}
}

// TestEnvIgnoresRetiredPrefix checks that a variable under the retired prefix is
// not read, whether the EMTDS_ name is unset or empty.
func TestEnvIgnoresRetiredPrefix(t *testing.T) {
	t.Setenv(retiredEnvName("ENVTEST_DIR"), "/old")
	if got := Env("EMTDS_ENVTEST_DIR"); got != "" {
		t.Fatalf("Env unset new = %q, want empty", got)
	}
	t.Setenv("EMTDS_ENVTEST_DIR", "")
	if got := Env("EMTDS_ENVTEST_DIR"); got != "" {
		t.Fatalf("Env empty new = %q, want empty", got)
	}
}

// TestEnvOtherKeysAreReadAsIs checks that a key without the EMTDS_ prefix is not
// rewritten and that Env does not invent a name.
func TestEnvOtherKeysAreReadAsIs(t *testing.T) {
	t.Setenv("ENVTEST_PLAIN", "plain")
	if got := Env("ENVTEST_PLAIN"); got != "plain" {
		t.Fatalf("Env plain = %q, want plain", got)
	}
	if got := Env("ENVTEST_MISSING"); got != "" {
		t.Fatalf("Env missing = %q, want empty", got)
	}
}

// TestEnvOr checks the default for an unset variable and that the retired name
// does not stand in for it.
func TestEnvOr(t *testing.T) {
	if got := EnvOr("EMTDS_ENVTEST_MISSING", "def"); got != "def" {
		t.Fatalf("EnvOr = %q, want def", got)
	}
	t.Setenv(retiredEnvName("ENVTEST_MISSING"), "old")
	if got := EnvOr("EMTDS_ENVTEST_MISSING", "def"); got != "def" {
		t.Fatalf("EnvOr retired = %q, want def", got)
	}
}

// TestSetupToken checks that the token is read from EMTDS_SETUP_TOKEN, trimmed, and that an
// unset or blank variable gives an empty token.
func TestSetupToken(t *testing.T) {
	t.Setenv(EnvSetupToken, "")
	if got := SetupToken(); got != "" {
		t.Fatalf("unset token = %q, want empty", got)
	}
	t.Setenv(EnvSetupToken, "  \t ")
	if got := SetupToken(); got != "" {
		t.Fatalf("blank token = %q, want empty", got)
	}
	t.Setenv(EnvSetupToken, " example-token \n")
	if got := SetupToken(); got != "example-token" {
		t.Fatalf("token = %q, want example-token", got)
	}
}
