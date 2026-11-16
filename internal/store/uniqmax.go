package store

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/egerkuzma/emtds/internal/config"
)

const (
	// UniqMaxEnv names the variable with the ceiling of the uniqueness window, in seconds.
	UniqMaxEnv = config.EnvPrefix + "UNIQ_MAX_SECONDS"
	// DefaultUniqMax is the ceiling when the variable is empty or 0, and the window of a
	// campaign that has none. A longer campaign window still counts a visitor once through
	// the cookie method; in Redis the key just lives for the ceiling.
	DefaultUniqMax = 24 * time.Hour
	// maxUniqSeconds bounds a configured ceiling (ten years) so the conversion to a Duration cannot overflow.
	maxUniqSeconds = 10 * 365 * 86400
)

// ParseUniqMax turns the raw value of EMTDS_UNIQ_MAX_SECONDS into a ceiling.
//
// raw is the text of the variable. An empty text or "0" gives DefaultUniqMax.
// A negative number, a fraction or any other text gives DefaultUniqMax together
// with an error, so the caller can log the mistake and still start.
func ParseUniqMax(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultUniqMax, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return DefaultUniqMax, fmt.Errorf("%s=%q is not a whole number of seconds, using %d", UniqMaxEnv, raw, int64(DefaultUniqMax/time.Second))
	}
	if n == 0 {
		return DefaultUniqMax, nil
	}
	if n > maxUniqSeconds {
		n = maxUniqSeconds
	}
	return time.Duration(n) * time.Second, nil
}

// UniqMaxFromEnv reads the ceiling of the uniqueness window from the environment.
//
// It returns the ceiling (DefaultUniqMax for an unset, 0 or malformed value) and
// a non-nil error only for a malformed value; engine and admin share it so both
// show the same window.
func UniqMaxFromEnv() (time.Duration, error) {
	return ParseUniqMax(config.Env(UniqMaxEnv))
}

// capWindow returns the lifetime of a uniqueness or sticky key.
//
// ttl is the window of the campaign and max the ceiling. A non-positive ttl is
// a day; a non-positive max means no ceiling. The result is min(ttl, max).
func capWindow(ttl, max time.Duration) time.Duration {
	if ttl <= 0 {
		ttl = DefaultUniqMax
	}
	if max > 0 && ttl > max {
		return max
	}
	return ttl
}

// EffectiveUniqWindow returns the window in seconds that Redis really keeps for a
// campaign with uniq_seconds = seconds under the ceiling max.
//
// seconds is the campaign field (0 means a day); max is the ceiling from
// UniqMaxFromEnv. The result is never above max's whole seconds.
func EffectiveUniqWindow(seconds int, max time.Duration) int {
	return int(capWindow(time.Duration(seconds)*time.Second, max) / time.Second)
}
