package logbuf

import (
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/hasayone/empataso-tds/internal/config"
)

// SpoolConfig describes a spool. Root and Instance are required.
// MaxBytes <= 0 takes the default. MinFreePct is the share of the volume that
// must stay free, 0 to 90; 0 means no floor and any other value outside the
// range is an error, so a caller that wants the default sets
// DefaultSpoolMinFreePct itself. Log may be nil.
// Free replaces the disk statistics read for the volume of a directory; nil
// uses the operating system. CheckMount makes OpenSpool log a warning when Root
// is not a mount point (a queue in the layer of a container is lost when the
// container is recreated) or when that cannot be told; it never stops the open.
// IsMount replaces the check, nil uses IsMountPoint. Free and IsMount are hooks
// for tests.
type SpoolConfig struct {
	Root       string
	Instance   string
	MaxBytes   int64
	MinFreePct int
	Log        *slog.Logger
	Free       func(dir string) (free, total uint64, err error)
	CheckMount bool
	IsMount    func(dir string) (bool, error)
}

// SpoolConfigFromEnv reads the spool settings from the environment:
// EMTDS_SPOOL_DIR, EMTDS_INSTANCE, EMTDS_SPOOL_MAX_BYTES and
// EMTDS_SPOOL_MIN_FREE_PCT. explicit is true when EMTDS_SPOOL_DIR is set, which
// tells the caller that a spool that cannot be opened is a deployment error and
// not a developer machine without the volume; cfg.CheckMount is then set. A
// size that is not a positive number and a floor that is not 0 to 90 are
// reported in the error and replaced by the defaults.
func SpoolConfigFromEnv() (cfg SpoolConfig, explicit bool, err error) {
	root := config.Env("EMTDS_SPOOL_DIR")
	cfg = SpoolConfig{
		Root:       config.EnvOr("EMTDS_SPOOL_DIR", DefaultSpoolDir),
		Instance:   config.Instance(),
		MaxBytes:   DefaultSpoolMaxBytes,
		MinFreePct: DefaultSpoolMinFreePct,
	}
	var errs []error
	if v := config.Env("EMTDS_SPOOL_MAX_BYTES"); v != "" {
		if n, perr := strconv.ParseInt(strings.TrimSpace(v), 10, 64); perr != nil || n <= 0 {
			errs = append(errs, fmt.Errorf("EMTDS_SPOOL_MAX_BYTES=%q is not a positive number of bytes", v))
		} else {
			cfg.MaxBytes = n
		}
	}
	if v := config.Env("EMTDS_SPOOL_MIN_FREE_PCT"); v != "" {
		if n, perr := strconv.Atoi(strings.TrimSpace(v)); perr != nil || n < 0 || n > 90 {
			errs = append(errs, fmt.Errorf("EMTDS_SPOOL_MIN_FREE_PCT=%q is not a number from 0 to 90", v))
		} else {
			cfg.MinFreePct = n
		}
	}
	cfg.CheckMount = root != ""
	return cfg, root != "", errors.Join(errs...)
}
