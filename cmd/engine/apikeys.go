package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"time"

	"github.com/egerkuzma/emtds/internal/security"
)

// apiKeys is the set of credentials accepted by ?api= mode.
//
// env is EMTDS_API_KEY and may be empty. set is the file-backed collection
// and may be nil when no key file is configured. Neither value is logged.
type apiKeys struct {
	env string
	set *security.KeySet
}

// verify reports whether raw may call the engine API.
//
// raw is the key_api from the request. The result is true only when raw is
// non-empty and either env is non-empty and security.EqualTokens matches it,
// or set.Verify accepts raw for security.ScopeEngine. An empty raw, a nil
// set with no env match, a revoked key, and a key without the engine scope
// are false. There is no error.
func (k apiKeys) verify(raw string) bool {
	if raw == "" {
		return false
	}
	if k.env != "" && security.EqualTokens(raw, k.env) {
		return true
	}
	if k.set == nil {
		return false
	}
	_, ok := k.set.Verify(raw, security.ScopeEngine)
	return ok
}

// loadAPIKeys reads the key file at path.
//
// An empty path returns nil and does not log, so only the env key can match.
// A missing file is an empty set, which is what LoadKeySet returns. Any other
// error is logged with Warn and an empty set is returned; the in-memory set
// is then safe for the watcher to fill later. The raw key is not logged.
// A nil log drops the warning. There is no error return.
func loadAPIKeys(path string, log *slog.Logger) *security.KeySet {
	if path == "" {
		return nil
	}
	set, err := security.LoadKeySet(path)
	if err != nil {
		warnAPIKeys(log, "api keys file not loaded", err)
		return security.NewKeySet()
	}
	return set
}

// apiKeyStamp is the last fingerprint of the key file seen by the watcher.
//
// seen is false when the file was absent. stamp is mtime and size, the same
// pair groups.json uses, so a rewrite that keeps mtime still reloads when
// the size changes. There is no error.
type apiKeyStamp struct {
	seen  bool
	stamp fileStamp
}

// keysWatcher re-reads the key file when its fingerprint changes until ctx
// is cancelled.
//
// path is the key file. set is the live collection verify already holds; a
// nil set or an empty path returns without a ticker. interval is the groups
// reload period; zero or negative becomes one minute. log receives warnings.
// A missing file is handed to set.Reload, which clears the set. A read or
// JSON error is logged and the previous keys stay. The raw key and the env
// key are never logged. There is no error return.
func keysWatcher(ctx context.Context, path string, set *security.KeySet, interval time.Duration, log *slog.Logger) {
	if path == "" || set == nil {
		return
	}
	if interval <= 0 {
		interval = time.Minute
	}
	prev := apiKeyStamp{}
	if st, err := statFile(path); err == nil {
		prev = apiKeyStamp{seen: true, stamp: *st}
	} else if errors.Is(err, os.ErrNotExist) {
		// LoadKeySet already produced an empty set for a missing file. Reload
		// again so a file that vanished after a successful load cannot leave
		// keys behind. Reload itself clears; this does not empty the set by hand.
		if err := set.Reload(path); err != nil {
			warnAPIKeys(log, "api keys reload failed", err)
		}
	} else {
		warnAPIKeys(log, "api keys file stat failed", err)
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			prev = reloadAPIKeys(path, set, prev, log)
		}
	}
}

// reloadAPIKeys is one watcher step.
//
// path is the key file and set is the live collection. prev is the fingerprint
// from the previous step. When mtime or size differs, set.Reload replaces the
// keys. A missing file calls Reload, which clears the set, and the returned
// stamp is not seen. A read or JSON error is logged, Reload leaves the set
// unchanged, and prev is returned so the next step retries. An unchanged
// file does not read the body. A nil set or an empty path returns prev.
// There is no error return.
func reloadAPIKeys(path string, set *security.KeySet, prev apiKeyStamp, log *slog.Logger) apiKeyStamp {
	if path == "" || set == nil {
		return prev
	}
	st, err := statFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			warnAPIKeys(log, "api keys file stat failed", err)
			return prev
		}
		if !prev.seen {
			return prev
		}
		if err := set.Reload(path); err != nil {
			warnAPIKeys(log, "api keys reload failed", err)
			return prev
		}
		if log != nil {
			log.Info("api keys cleared", "path", path)
		}
		return apiKeyStamp{}
	}
	if prev.seen && st.mod.Equal(prev.stamp.mod) && st.size == prev.stamp.size {
		return prev
	}
	if err := set.Reload(path); err != nil {
		warnAPIKeys(log, "api keys reload failed", err)
		return prev
	}
	if log != nil {
		log.Info("api keys reloaded", "path", path)
	}
	return apiKeyStamp{seen: true, stamp: *st}
}

// warnAPIKeys writes a reload warning.
//
// msg is a fixed phrase and err is the stat, read, or JSON error. A nil log
// or a nil err does nothing. The raw key and the env key are not parameters
// and are not written. There is no error return.
func warnAPIKeys(log *slog.Logger, msg string, err error) {
	if log == nil || err == nil {
		return
	}
	log.Warn(msg, "err", err)
}
