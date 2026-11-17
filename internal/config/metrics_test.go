package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestValidInstance checks which instance names may become a metrics file name.
func TestValidInstance(t *testing.T) {
	for _, ok := range []string{"engine", "engine2", "engine-2", "a", strings.Repeat("a", 32)} {
		if !ValidInstance(ok) {
			t.Errorf("%q must be valid", ok)
		}
	}
	for _, bad := range []string{"", "Engine", "-engine", "en gine", "en_gine", "../x", "a/b", "a.b", strings.Repeat("a", 33), "движок"} {
		if ValidInstance(bad) {
			t.Errorf("%q must be invalid", bad)
		}
	}
}

// TestInstanceAndMetricsDirEnv checks the defaults, that the retired variable names
// are not read, and the EMTDS_ names.
func TestInstanceAndMetricsDirEnv(t *testing.T) {
	t.Setenv("EMTDS_INSTANCE", "")
	t.Setenv("EMTDS_METRICS_DIR", "")
	if Instance() != DefaultInstance || MetricsDir() != DefaultMetricsDir {
		t.Fatalf("defaults: %q %q", Instance(), MetricsDir())
	}
	t.Setenv(retiredEnvName("INSTANCE"), " old ")
	t.Setenv(retiredEnvName("METRICS_DIR"), "/old")
	if Instance() != DefaultInstance || MetricsDir() != DefaultMetricsDir {
		t.Fatalf("retired names must be ignored: %q %q", Instance(), MetricsDir())
	}
	t.Setenv("EMTDS_INSTANCE", "engine2")
	t.Setenv("EMTDS_METRICS_DIR", "/new")
	if Instance() != "engine2" || MetricsDir() != "/new" {
		t.Fatalf("product names: %q %q", Instance(), MetricsDir())
	}
	if got, want := MetricsPath("/new", "engine2"), filepath.Join("/new", "engine2.json"); got != want {
		t.Fatalf("path %q, want %q", got, want)
	}
}
