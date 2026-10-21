package cron

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultIPProxySources(t *testing.T) {
	c := Default()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	var proxy []IPSource
	for _, s := range c.IPLists.Sources {
		if strings.Contains(s.URL, "firehol_proxies.netset") {
			t.Fatalf("oversized list still configured: %s", s.URL)
		}
		if s.Target == "ip_proxy" {
			proxy = append(proxy, s)
		}
	}
	if len(proxy) != 5 {
		t.Fatalf("ip_proxy sources = %d", len(proxy))
	}
	if proxy[0].Mode != "replace" {
		t.Fatalf("first ip_proxy mode = %s", proxy[0].Mode)
	}
	for i, s := range proxy[1:] {
		if s.Mode != "merge" {
			t.Fatalf("ip_proxy source %d mode = %s", i+2, s.Mode)
		}
	}
}

func TestLoadConfigWithoutKeitaroSync(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cron.json")
	if err := os.WriteFile(path, []byte(`{"conversions":{"enabled":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.KeitaroSync.EveryMinutes != 15 {
		t.Fatalf("default every_minutes = %d", c.KeitaroSync.EveryMinutes)
	}
	if c.KeitaroSync.Enabled {
		t.Fatal("keitaro_sync must default to disabled")
	}
}

func TestValidateAllowsEnabledKeitaroSync(t *testing.T) {
	c := Default()
	c.KeitaroSync.Enabled = true
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestCheckIntervalsFloors(t *testing.T) {
	c := Default()
	c.KeitaroSync.Enabled = true
	c.KeitaroSync.EveryMinutes = 1
	if err := CheckIntervals(c); err == nil || !strings.Contains(err.Error(), "keitaro_sync") {
		t.Fatalf("expected floor error, got %v", err)
	}
	c.KeitaroSync.EveryMinutes = MaxKeitaroSyncInterval + 1
	if err := CheckIntervals(c); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected cap error, got %v", err)
	}
	c.KeitaroSync.EveryMinutes = 15
	if err := CheckIntervals(c); err != nil {
		t.Fatal(err)
	}
	if Floors()[JobKeitaroSync] != 5 {
		t.Fatalf("floor = %d", Floors()[JobKeitaroSync])
	}
}

func TestKeitaroSyncNotInCronJobs(t *testing.T) {
	for _, j := range Jobs {
		if j == JobKeitaroSync {
			t.Fatal("keitaro_sync must not be in Jobs")
		}
	}
	found := false
	for _, j := range AdminJobs {
		if j == JobKeitaroSync {
			found = true
		}
	}
	if !found {
		t.Fatal("keitaro_sync missing from AdminJobs")
	}
}

func TestTickDoesNotRunKeitaroSync(t *testing.T) {
	e := newEnv(t)
	cfg := Default()
	cfg.KeitaroSync.Enabled = true
	cfg.Conversions.Enabled = false
	cfg.IPLists.Enabled = false
	cfg.GeoDB.Enabled = false
	cfg.VirusTotal.Enabled = false
	cfg.Disk.Enabled = false
	cfg.Cleanup.Enabled = false
	if err := Save(e.r.paths.Config, cfg); err != nil {
		t.Fatal(err)
	}
	e.r.Tick(context.Background())
	e.r.Wait()
	st, err := ReadStatus(StatusPath(e.r.paths.Config))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := st.Jobs[JobKeitaroSync]; ok {
		t.Fatalf("cron status must not track keitaro_sync: %+v", st.Jobs[JobKeitaroSync])
	}
}

func TestRequestRunRejectsKeitaroSync(t *testing.T) {
	e := newEnv(t)
	if err := RequestRun(e.r.paths.Config, JobKeitaroSync); err == nil {
		t.Fatal("expected rejection")
	}
}

func TestMissingStatusNoArchive(t *testing.T) {
	e := newEnv(t)
	statusPath := StatusPath(e.r.paths.Config)
	if _, err := os.Stat(statusPath + ".bad"); !os.IsNotExist(err) {
		t.Fatalf(".bad must not exist: %v", err)
	}
}

func TestCorruptStatusFileRepair(t *testing.T) {
	e := newEnv(t)
	statusPath := StatusPath(e.r.paths.Config)
	if err := os.WriteFile(statusPath, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := New(e.r.paths, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	bad := statusPath + ".bad"
	if _, err := os.Stat(bad); err != nil {
		t.Fatalf("archived status missing: %v", err)
	}
	if _, err := os.Stat(statusPath); !os.IsNotExist(err) {
		t.Fatal("corrupt status must be moved aside")
	}
	cfg := Default()
	cfg.Conversions.Enabled = false
	cfg.IPLists.Enabled = false
	cfg.GeoDB.Enabled = false
	cfg.VirusTotal.Enabled = false
	cfg.Disk.Enabled = false
	cfg.Cleanup.Enabled = false
	if err := Save(r.paths.Config, cfg); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	r.Tick(context.Background())
	for _, job := range Jobs {
		js := r.st.Jobs[job]
		if js.NextRun.IsZero() {
			t.Fatalf("%s: next_run still zero", job)
		}
		wantCap := 10 * time.Minute
		d := cfg.Interval(job)
		if d > wantCap {
			d = wantCap
		}
		if !js.NextRun.Equal(now.Add(d)) {
			t.Fatalf("%s: next_run=%v want %v", job, js.NextRun, now.Add(d))
		}
	}
	_ = New(e.r.paths, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := os.Stat(bad); err != nil {
		t.Fatalf("second New must not remove archive: %v", err)
	}
}

func TestCorruptStatusNonJSONReadErrorNoArchive(t *testing.T) {
	if os.Getenv("GOOS") == "windows" {
		t.Skip("device path unreadable check is unix-specific")
	}
	e := newEnv(t)
	statusPath := StatusPath(e.r.paths.Config)
	if err := os.WriteFile(statusPath, []byte(`{"jobs":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(statusPath, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(statusPath, 0o644) })
	_ = New(e.r.paths, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := os.Stat(statusPath + ".bad"); !os.IsNotExist(err) {
		t.Fatal("permission error must not archive status")
	}
}
