package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/cron"
)

func writeCronStatus(t *testing.T, cronFile string, st cron.Status) {
	t.Helper()
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cron.StatusPath(cronFile), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readNotices(t *testing.T, s *Server) []string {
	t.Helper()
	rec := httptest.NewRecorder()
	s.handleNotices(rec, httptest.NewRequest(http.MethodGet, "/api/notices", nil))
	var lines []string
	if err := json.Unmarshal(rec.Body.Bytes(), &lines); err != nil || lines == nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	return lines
}

func TestNoticesCarryRedisAlert(t *testing.T) {
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`["2026-11-15T12:00:00Z engine warning"]`))
	}))
	t.Cleanup(engine.Close)
	cronFile := filepath.Join(t.TempDir(), "cron.json")
	s := &Server{cfg: Config{EngineURL: engine.URL, CronFile: cronFile}}

	if lines := readNotices(t, s); len(lines) != 1 {
		t.Fatalf("no cron status: %q", lines)
	}
	writeCronStatus(t, cronFile, cron.Status{Redis: &cron.RedisMemState{
		CheckedAt: time.Now(), UsedBytes: 450 << 20, MaxBytes: 512 << 20, Percent: 87, Alerted: true,
	}})
	lines := readNotices(t, s)
	if len(lines) != 2 || !strings.HasSuffix(lines[1], "redis memory 87% (450 MiB of 512 MiB)") {
		t.Fatalf("raised alert: %q", lines)
	}
	writeCronStatus(t, cronFile, cron.Status{Redis: &cron.RedisMemState{
		CheckedAt: time.Now(), UsedBytes: 100 << 20, MaxBytes: 512 << 20, Percent: 19,
	}})
	if lines := readNotices(t, s); len(lines) != 1 {
		t.Fatalf("cleared alert: %q", lines)
	}
	s.cfg.EngineURL = ""
	writeCronStatus(t, cronFile, cron.Status{Redis: &cron.RedisMemState{
		CheckedAt: time.Now(), UsedBytes: 500 << 20, MaxBytes: 512 << 20, Percent: 97, Alerted: true,
	}})
	if lines := readNotices(t, s); len(lines) != 1 {
		t.Fatalf("alert without engine: %q", lines)
	}
}

// TestNoticesCarryLossAlert checks that a raised event loss alert reaches the
// notices as the line the SPA banner parses, beside the Redis alert, and that a
// cleared or stale one does not.
func TestNoticesCarryLossAlert(t *testing.T) {
	cronFile := filepath.Join(t.TempDir(), "cron.json")
	s := &Server{cfg: Config{CronFile: cronFile}}
	now := time.Now()
	raised := &cron.LossState{CheckedAt: now, Alerted: true, Lost: 120, Instances: []string{"engine"}}
	writeCronStatus(t, cronFile, cron.Status{Loss: raised, Redis: &cron.RedisMemState{
		CheckedAt: now, UsedBytes: 450 << 20, MaxBytes: 512 << 20, Percent: 87, Alerted: true,
	}})
	lines := readNotices(t, s)
	if len(lines) != 2 || !strings.HasSuffix(lines[0], "redis memory 87% (450 MiB of 512 MiB)") || !strings.HasSuffix(lines[1], " event loss 120 (engine)") {
		t.Fatalf("raised alerts: %q", lines)
	}
	writeCronStatus(t, cronFile, cron.Status{Loss: &cron.LossState{CheckedAt: now, Lost: 120, Instances: []string{"engine"}}})
	if lines := readNotices(t, s); len(lines) != 0 {
		t.Fatalf("cleared alert: %q", lines)
	}
	stale := *raised
	stale.CheckedAt = now.Add(-cron.LossStateFresh - time.Minute)
	writeCronStatus(t, cronFile, cron.Status{Loss: &stale})
	if lines := readNotices(t, s); len(lines) != 0 {
		t.Fatalf("stale alert: %q", lines)
	}
}
