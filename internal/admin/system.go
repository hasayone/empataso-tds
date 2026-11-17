package admin

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hasayone/empataso-tds/internal/cron"
)

// systemPingTimeout bounds each service check of GET /api/system.
var systemPingTimeout = 2 * time.Second

// Values of systemService.State.
const (
	serviceOK            = "ok"
	serviceDown          = "down"
	serviceNotConfigured = "not_configured"
)

// defaultStateDir is the state volume of the container when no configured path says otherwise.
const defaultStateDir = "/var/lib/emtds"

// systemView is the body of GET /api/system.
type systemView struct {
	Host     systemHost     `json:"host"`
	Services systemServices `json:"services"`
}

// systemHost holds the figures of the machine. Cpus is always set. Every pointer is nil, and the
// field is left out of the JSON, when its source is not readable: there is no /proc, no
// matching line, or no filesystem statistics. Sizes are bytes.
type systemHost struct {
	Cpus         int         `json:"cpus"`
	Load         *[3]float64 `json:"load,omitempty"`
	MemTotal     *uint64     `json:"mem_total,omitempty"`
	MemAvailable *uint64     `json:"mem_available,omitempty"`
	UptimeS      *int64      `json:"uptime_s,omitempty"`
	DiskTotal    *uint64     `json:"disk_total,omitempty"`
	DiskFree     *uint64     `json:"disk_free,omitempty"`
}

// systemServices holds the services around the admin. Cron is nil without a readable status file.
type systemServices struct {
	ClickHouse systemService `json:"clickhouse"`
	Redis      systemService `json:"redis"`
	Cron       *systemCron   `json:"cron,omitempty"`
}

// systemService is the check of one service. State is ok, down or not_configured.
// Ms is the answer time in milliseconds and is set only for ok and down.
type systemService struct {
	State string `json:"state"`
	Ms    *int64 `json:"ms,omitempty"`
}

// systemCron is the cron service as the status file shows it. HeartbeatAgeS is nil when the
// service has not written a heartbeat yet. Busy is true while a job that blocks a restart runs.
type systemCron struct {
	HeartbeatAgeS *int64 `json:"heartbeat_age_s,omitempty"`
	Busy          bool   `json:"busy"`
}

// pinger is a store that can tell whether its server answers.
type pinger interface {
	Ping(ctx context.Context) error
}

// pingerOf returns v as a pinger, or nil when v is not one or is a nil pointer inside the
// interface (a store that was never opened). A nil result means not configured.
func pingerOf(v any) pinger {
	p, ok := v.(pinger)
	if !ok {
		return nil
	}
	if rv := reflect.ValueOf(v); rv.Kind() == reflect.Pointer && rv.IsNil() {
		return nil
	}
	return p
}

// checkService pings p within systemPingTimeout, derived from ctx.
// A nil p is not_configured with no time. Otherwise the state is ok or down and Ms is the
// time the ping took.
func checkService(ctx context.Context, p pinger) systemService {
	if p == nil {
		return systemService{State: serviceNotConfigured}
	}
	ctx, cancel := context.WithTimeout(ctx, systemPingTimeout)
	defer cancel()
	start := time.Now()
	err := p.Ping(ctx)
	ms := time.Since(start).Milliseconds()
	if err != nil {
		return systemService{State: serviceDown, Ms: &ms}
	}
	return systemService{State: serviceOK, Ms: &ms}
}

// handleSystem serves GET /api/system.
// It returns systemView: the host figures, ClickHouse and Redis checked in parallel, and the
// cron heartbeat. Only administrators may call it, and only with a browser session.
// The versions stay in GET /api/version. A service that does not answer is state down in a
// 200 answer, never an error status.
func (s *Server) handleSystem(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(sessionFrom(r.Context())) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	var out systemView
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		out.Services.ClickHouse = checkService(r.Context(), pingerOf(s.cfg.Stats))
	}()
	go func() {
		defer wg.Done()
		out.Services.Redis = checkService(r.Context(), pingerOf(s.cfg.Sessions))
	}()
	out.Host = s.collectHost()
	out.Services.Cron = s.collectCron(time.Now())
	wg.Wait()
	writeJSON(w, http.StatusOK, out)
}

// collectCron reads the cron status file named by Config.CronFile.
// now dates the heartbeat. It returns nil when no file is configured or it cannot be read.
func (s *Server) collectCron(now time.Time) *systemCron {
	if s.cfg.CronFile == "" {
		return nil
	}
	probe, err := cron.ProbeStatus(cron.StatusPath(s.cfg.CronFile), now)
	if err != nil {
		return nil
	}
	out := &systemCron{Busy: probe.Busy}
	if probe.HeartbeatAge >= 0 {
		out.HeartbeatAgeS = &probe.HeartbeatAge
	}
	return out
}

// collectHost reads the figures of the machine: the CPU count, the three files under the proc
// root and the disk of the state directory. A source that cannot be read leaves its fields nil.
func (s *Server) collectHost() systemHost {
	out := systemHost{Cpus: runtime.NumCPU()}
	root := s.procRoot
	if root == "" {
		root = "/proc"
	}
	if text, ok := readProcFile(root, "loadavg"); ok {
		out.Load = parseLoadavg(text)
	}
	if text, ok := readProcFile(root, "meminfo"); ok {
		out.MemTotal, out.MemAvailable = parseMeminfo(text)
	}
	if text, ok := readProcFile(root, "uptime"); ok {
		out.UptimeS = parseUptime(text)
	}
	stat := s.diskStat
	if stat == nil {
		stat = cron.DiskFree
	}
	if free, total, err := stat(s.stateDir()); err == nil && total > 0 {
		out.DiskFree, out.DiskTotal = &free, &total
	}
	return out
}

// stateDir returns the directory whose free space GET /api/system reports: the directory of the
// password file, otherwise of the cron file (both live on the state volume), otherwise defaultStateDir.
func (s *Server) stateDir() string {
	for _, file := range []string{s.cfg.PasswordFile, s.cfg.CronFile} {
		if file != "" {
			return filepath.Dir(file)
		}
	}
	return defaultStateDir
}

// readProcFile returns the text of name under root and whether it could be read.
// The files of /proc are a few hundred bytes, so the whole file is read.
func readProcFile(root, name string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		return "", false
	}
	return string(b), true
}

// parseLoadavg returns the 1, 5 and 15 minute load from the text of /proc/loadavg,
// or nil when the first three fields are not numbers.
func parseLoadavg(text string) *[3]float64 {
	fields := strings.Fields(text)
	if len(fields) < 3 {
		return nil
	}
	var load [3]float64
	for i := range load {
		v, err := strconv.ParseFloat(fields[i], 64)
		if err != nil || v < 0 {
			return nil
		}
		load[i] = v
	}
	return &load
}

// parseMeminfo returns MemTotal and MemAvailable of /proc/meminfo in bytes.
// A value whose line is missing or damaged is nil; the other is still returned.
func parseMeminfo(text string) (total, available *uint64) {
	for _, line := range strings.Split(text, "\n") {
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch name {
		case "MemTotal":
			total = parseKiB(rest)
		case "MemAvailable":
			available = parseKiB(rest)
		}
	}
	return total, available
}

// parseKiB converts the value of a meminfo line such as " 8388608 kB" to bytes.
// It returns nil when the first field is not a number.
func parseKiB(rest string) *uint64 {
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return nil
	}
	n, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return nil
	}
	bytes := n << 10
	return &bytes
}

// parseUptime returns the whole seconds since boot from the text of /proc/uptime,
// or nil when the first field is not a non-negative number.
func parseUptime(text string) *int64 {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return nil
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || v < 0 {
		return nil
	}
	secs := int64(v)
	return &secs
}
