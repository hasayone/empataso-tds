package cron

import (
	"fmt"
	"os"
	"regexp"
	"time"
)

// Probe is a point-in-time snapshot of the cron service status used by the
// updater to decide whether cron can be safely recreated.
type Probe struct {
	// HeartbeatAge is the number of whole seconds since the last heartbeat,
	// or -1 when the heartbeat field is zero (service has never run or the
	// status file has just been created).
	HeartbeatAge int64
	// Busy is true when geo_db or virustotal is running, meaning cron
	// must not be recreated until it finishes.
	Busy bool
	// Version is the build version string read from the status file.
	Version string
	// Commit is the build commit SHA read from the status file.
	Commit string
}

// reProbeField matches the characters allowed in Version and Commit output.
// The root script that calls -probe expects clean, shell-safe values.
var reProbeField = regexp.MustCompile(`^[A-Za-z0-9._+\-]+$`)

// safeProbeField sanitises s for inclusion in the probe key=value output
// consumed by root scripts. Returns s when it passes the allowlist and fits
// within 64 characters, otherwise returns an empty string.
func safeProbeField(s string) string {
	if s == "" || len(s) > 64 || !reProbeField.MatchString(s) {
		return ""
	}
	return s
}

// ProbeStatus reads the cron status file at path and returns a Probe snapshot.
// A missing file is an error; the updater needs to know whether cron has
// ever started. now is used to compute HeartbeatAge as whole seconds.
//
// path is the absolute path to the cron status file (cron.json.status.json).
// Returns the Probe and any read or parse error.
func ProbeStatus(path string, now time.Time) (Probe, error) {
	if _, err := os.Lstat(path); err != nil {
		return Probe{}, err
	}
	st, err := ReadStatus(path)
	if err != nil {
		return Probe{}, err
	}
	var age int64 = -1
	if !st.Heartbeat.IsZero() {
		d := now.Sub(st.Heartbeat)
		if d < 0 {
			d = 0
		}
		age = int64(d.Seconds())
	}
	return Probe{
		HeartbeatAge: age,
		Busy:         st.Jobs[JobGeoDB].Running || st.Jobs[JobVirusTotal].Running,
		Version:      st.Version,
		Commit:       st.Commit,
	}, nil
}

// Format returns the probe as four newline-terminated key=value lines in the
// format expected by the updater script. busy is 1 when Busy is true.
// Version and Commit are passed through safeProbeField: only [A-Za-z0-9._+-],
// length ≤64, otherwise written as an empty value.
func (p Probe) Format() string {
	busy := 0
	if p.Busy {
		busy = 1
	}
	return fmt.Sprintf("heartbeat_age=%d\nbusy=%d\nversion=%s\ncommit=%s\n",
		p.HeartbeatAge, busy,
		safeProbeField(p.Version),
		safeProbeField(p.Commit),
	)
}
