package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"log/slog"
	"sync"
	"time"

	"github.com/egerkuzma/emtds/internal/config"
)

// authResult is the outcome of one postback key check.
type authResult int

const (
	// authPass means the request is allowed to proceed.
	authPass authResult = iota
	// authSkipCompat means the key was absent in compat mode: the request is
	// let through (preserving the old open-endpoint behaviour), but a warn log
	// is emitted at most once per minute.
	authSkipCompat
	// authDenyBadKey means the key was present but wrong, or absent in require
	// mode; the caller writes 403 "bad key".
	authDenyBadKey
	// authDenyNoSecret means require mode is active but no secret is configured;
	// the caller writes 403 "postback secret not set".
	authDenyNoSecret
)

// postbackWarn rate-limits the compat missing-key log line.
// It is a pointer inside postbackAuth so the auth value can be copied.
type postbackWarn struct {
	mu   sync.Mutex
	last time.Time
}

// postbackAuth holds the postback secret and its enforcement mode.
// The zero value behaves as compat with an empty secret: every request passes.
type postbackAuth struct {
	secret string
	mode   string // "compat" (default) or "require"
	warn   *postbackWarn
}

// loadPostbackAuth reads EMTDS_POSTBACK_SECRET and EMTDS_POSTBACK_AUTH.
// An unrecognized EMTDS_POSTBACK_AUTH value is treated as "require" and an
// error is logged at startup. When mode is "require" and the secret is empty,
// an error is logged; the engine still starts, but every postback returns 403
// with "postback secret not set".
func loadPostbackAuth(log *slog.Logger) postbackAuth {
	secret := config.Env("EMTDS_POSTBACK_SECRET")
	rawMode := config.Env("EMTDS_POSTBACK_AUTH")

	mode := "compat"
	switch rawMode {
	case "", "compat":
		// default
	case "require":
		mode = "require"
	default:
		mode = "require"
		log.Error("EMTDS_POSTBACK_AUTH: unknown value, using require", "value", rawMode)
	}

	if mode == "require" && secret == "" {
		log.Error("postback auth is require but EMTDS_POSTBACK_SECRET is not set: all postbacks will return 403")
	}

	return postbackAuth{secret: secret, mode: mode, warn: &postbackWarn{}}
}

// check returns the authentication outcome for the given key.
// key is taken from the request: either from the "key" query/form parameter or
// from the URL path segment in the Keitaro /{key}/postback format. log is used
// for the rate-limited compat warning and may be nil.
func (a *postbackAuth) check(key string, log *slog.Logger) authResult {
	switch {
	case a.secret == "" && a.mode != "require":
		// No secret in compat mode → pass all requests unchanged.
		return authPass

	case a.secret == "" && a.mode == "require":
		return authDenyNoSecret

	case key == "" && a.mode == "require":
		return authDenyBadKey

	case key == "":
		// compat mode, key absent → let through, warn at most once per minute.
		if log != nil && a.shouldWarn() {
			log.Warn("postback: key parameter absent (EMTDS_POSTBACK_SECRET is set); set EMTDS_POSTBACK_AUTH=require to enforce it")
		}
		return authSkipCompat

	default:
		// Both key and secret are set: compare with hmac.Equal to prevent timing attacks.
		kSum := sha256.Sum256([]byte(key))
		sSum := sha256.Sum256([]byte(a.secret))
		if hmac.Equal(kSum[:], sSum[:]) {
			return authPass
		}
		return authDenyBadKey
	}
}

// matchesSecret reports whether value equals the configured secret using a
// constant-time comparison. Returns false when the secret is empty.
func (a *postbackAuth) matchesSecret(value string) bool {
	if a.secret == "" {
		return false
	}
	vSum := sha256.Sum256([]byte(value))
	sSum := sha256.Sum256([]byte(a.secret))
	return hmac.Equal(vSum[:], sSum[:])
}

// shouldWarn reports whether the compat missing-key warning should fire now.
// It fires at most once per minute.
func (a *postbackAuth) shouldWarn() bool {
	if a == nil || a.warn == nil {
		return false
	}
	a.warn.mu.Lock()
	defer a.warn.mu.Unlock()
	if time.Since(a.warn.last) < time.Minute {
		return false
	}
	a.warn.last = time.Now()
	return true
}
