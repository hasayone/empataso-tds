package ktimport

import "testing"

// TestEnvIgnoresRetiredPrefix проверяет, что Env читает только заданное имя:
// переменная со снятым префиксом (он собран из частей) значения не даёт.
func TestEnvIgnoresRetiredPrefix(t *testing.T) {
	t.Setenv("KU"+"ZTDS_KTENV_URL", "http://old")
	if got := Env("EMTDS_KTENV_URL"); got != "" {
		t.Fatalf("Env = %q, want empty", got)
	}
	t.Setenv("EMTDS_KTENV_URL", "http://new")
	if got := Env("EMTDS_KTENV_URL"); got != "http://new" {
		t.Fatalf("Env = %q, want new value", got)
	}
}
